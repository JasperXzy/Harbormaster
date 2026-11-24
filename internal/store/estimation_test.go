package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func estimateFixture(t *testing.T) (*Store, string, string, time.Time) {
	t.Helper()
	s := powerStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(10 * time.Second).Add(-3 * time.Minute)
	code, _, err := s.AgentGrant(ctx, "ecs", base)
	if err != nil {
		t.Fatal(err)
	}
	reg := registration(code, "i-test")
	grant, err := s.RegisterAgent(ctx, reg, base)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, reg.Nonce)
	for i := int64(0); i <= 12; i++ {
		r := agentproto.Report{Version: 1, AgentID: grant.AgentID, CounterEpoch: "epoch", Sequence: i + 1, SampledAt: base.Add(time.Duration(i) * 10 * time.Second), UptimeMS: 1000 + i*10000, RXBytes: i * 100, TXBytes: i * 1000, RulesVersion: "v1", ReportIntervalSeconds: 10}
		if _, err = s.AcceptReport(ctx, token, r, r.SampledAt); err != nil {
			t.Fatal(err)
		}
	}
	sub := map[string]any{"id": "subject", "metering_rule": "outbound_only", "instance_ids": []string{"ecs"}, "counts_toward_cdt": nil, "cdt_scope": "mainland", "bandwidth_mbps": int64(8)}
	raw, _ := json.Marshal(sub)
	if _, err = s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','subject','subject','cn-shanghai',?,1,?)", string(raw), timestamp(base)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO metering_topology VALUES('a','cn-shanghai',1,'f',?,?,1,'observed')", timestamp(base), timestamp(base.Add(120*time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveEstimationSettings(ctx, model.EstimationSettings{LagSeconds: 60, BandwidthIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	j, err := s.EnqueueJob(ctx, "refresh", "cdt:a", []string{"a"}, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,overseas_bytes,details_json) VALUES('a',1,?,1,?,1000,0,'[]')", j.ID, timestamp(base.Add(60*time.Second))); err != nil {
		t.Fatal(err)
	}
	return s, grant.AgentID, token, base
}
func estimateRow(t *testing.T, s *Store, now time.Time) *model.TrafficEstimation {
	t.Helper()
	ctx := context.Background()
	if err := s.RefreshEstimates(ctx, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	a, err := s.Account(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Quotas(ctx, []model.Account{a}, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Estimation == nil {
		t.Fatalf("missing cache: %+v", rows)
	}
	return rows[0].Estimation
}
func TestDualEstimateNeverPromotesInferredBasis(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	now := base.Add(120 * time.Second)
	v := estimateRow(t, s, now)
	if v.Mode != "unverified" || v.Status != "ready" || v.Increment == nil || *v.Increment != 12000 || v.Estimated == nil || *v.Estimated != 13000 || v.CurrentRate == nil || *v.CurrentRate != 100 || v.Qualified || !slices.Contains(v.Reasons, "cdt_eligibility_unknown") {
		t.Fatalf("%+v", v)
	}
	a, _ := s.Account(context.Background(), "a")
	q, _ := s.Quotas(context.Background(), []model.Account{a}, time.UTC, now)
	if q[0].EstimatedBytes != nil || q[0].OfficialAsOf != nil {
		t.Fatal("unverified estimate leaked into aligned fields")
	}
	cached, err := s.cachedEstimate(context.Background(), a, "mainland", v.BasisID, now.Add(31*time.Second))
	if err != nil || cached.Status != "stale" || cached.Qualified {
		t.Fatal(cached, err)
	}
	// Unknown topology is not interpreted as an empty billing graph.
	if _, err = s.DB.Exec("UPDATE metering_topology SET observed_since=?", timestamp(base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if v.Status != "incomplete" || v.Estimated != nil || v.KnownIncrement != 12000 {
		t.Fatal(v)
	}
}

func TestEmptyPackageNeedsAddressAwareHistoryBeforeRealtimeZero(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	now := base.Add(120 * time.Second)
	object := map[string]any{"id": "subject", "kind": "cbwp_dominant_traffic", "metering_rule": "max_in_out_per_period", "counts_toward_cdt": true, "cdt_scope": "mainland", "instance_ids": []string{}, "public_ips": []string{}}
	raw, _ := json.Marshal(object)
	if _, err := s.DB.Exec("UPDATE resource_snapshot SET payload_json=? WHERE kind='subject'", string(raw)); err != nil {
		t.Fatal(err)
	}
	v := estimateRow(t, s, now)
	if v.Status != "incomplete" || v.Estimated != nil {
		t.Fatal("legacy hash proved zero", v)
	}
	history, _ := json.Marshal([]any{object})
	if _, err := s.DB.Exec("INSERT INTO daily_topology(account_id,region,credential_version,fingerprint,observed_from,last_seen,subjects_json) VALUES('a','cn-shanghai',1,'empty',?,?,?)", timestamp(base.Add(time.Second)), timestamp(now), string(history)); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if v.Status != "incomplete" {
		t.Fatal("backdated proof of empty", v)
	}
	if _, err := s.DB.Exec("UPDATE daily_topology SET observed_from=?", timestamp(base)); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if v.Status != "ready" || v.Increment == nil || *v.Increment != 0 || v.Estimated == nil || *v.Estimated != 1000 || v.Qualified {
		t.Fatal(v)
	}
	object["public_ips"] = []string{"8.8.8.8"}
	raw, _ = json.Marshal(object)
	if _, err := s.DB.Exec("UPDATE resource_snapshot SET payload_json=? WHERE kind='subject'", string(raw)); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if v.Status != "incomplete" {
		t.Fatal("address without member became zero", v)
	}
}

func TestTopologyCoverageIsIsolatedByCDTScope(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	ctx := context.Background()
	now := base.Add(120 * time.Second)
	a, err := s.Account(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	a.RegionIDs = append(a.RegionIDs, "cn-hongkong")
	if _, err = s.DB.Exec("INSERT INTO metering_topology VALUES('a','cn-hongkong',1,'new',?,?,1,'observed')", timestamp(base.Add(time.Second)), timestamp(now)); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	settings, err := readEstimationSettings(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	mainland, err := calculateEstimate(ctx, tx, a, "mainland", settings, time.UTC, now)
	if err != nil || mainland.Status != "ready" {
		t.Fatal(mainland, err)
	}
	overseas, err := calculateEstimate(ctx, tx, a, "overseas", settings, time.UTC, now)
	if err != nil || overseas.Status != "incomplete" || !slices.Contains(overseas.Reasons, "topology_history_incomplete") {
		t.Fatal(overseas, err)
	}
}
func TestTrustedCutoffAndPeriodAreNecessaryForAlignedPath(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	now := base.Add(120 * time.Second)
	start, end := ReferenceCycle(now, time.UTC)
	_, err := s.DB.Exec("UPDATE cdt_observation SET source_quality='verified',covered_until=?,period_start=?,period_end=?,source='fixture-watermark'", timestamp(base.Add(60*time.Second)), timestamp(start), timestamp(end))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE metering_topology SET quality='verified'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE resource_snapshot SET payload_json=json_set(payload_json,'$.counts_toward_cdt',json('true')) WHERE kind='subject'"); err != nil {
		t.Fatal(err)
	}
	v := estimateRow(t, s, now)
	if v.Mode != "aligned" || !v.Qualified || v.Increment == nil || *v.Increment != 6000 || *v.Estimated != 7000 {
		t.Fatalf("%+v", v)
	}
	// A wrong monthly period cannot qualify even when marked verified.
	if _, err = s.DB.Exec("UPDATE cdt_observation SET period_start=?", timestamp(start.AddDate(0, -1, 0))); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if v.Mode != "unverified" || v.Qualified {
		t.Fatal(v)
	}
}
func TestOfflineProjectionIsReplacedByRecoveredCounters(t *testing.T) {
	s, id, token, base := estimateFixture(t)
	now := base.Add(200 * time.Second)
	v := estimateRow(t, s, now)
	if v.WorstCase == nil || *v.WorstCase != 80_000_000 || *v.Increment != 80_012_000 {
		t.Fatalf("%+v", v)
	}
	report := agentproto.Report{Version: 1, AgentID: id, CounterEpoch: "epoch", Sequence: 22, SampledAt: base.Add(210 * time.Second), UptimeMS: 211000, RXBytes: 2100, TXBytes: 21000, RulesVersion: "v1", ReportIntervalSeconds: 10}
	if _, err := s.AcceptReport(context.Background(), token, report, report.SampledAt); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, report.SampledAt)
	if v.WorstCase == nil || *v.WorstCase != 0 || *v.Increment != 21000 {
		t.Fatal("projection added to recovery", v)
	}
}
func TestEstimateSettingsAndAccountFences(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	now := base.Add(120 * time.Second)
	v := estimateRow(t, s, now)
	ctx := context.Background()
	settings, _ := s.EstimationSettings(ctx)
	settings.LagSeconds = 120
	updated, err := s.SaveEstimationSettings(ctx, settings)
	if err != nil || updated.Revision != settings.Revision+1 {
		t.Fatal(updated, err)
	}
	if _, err = s.SaveEstimationSettings(ctx, settings); err != ErrEstimationRevision {
		t.Fatal(err)
	}
	a, _ := s.Account(ctx, "a")
	got, err := s.cachedEstimate(ctx, a, "mainland", v.BasisID, now)
	if err != nil || got != nil {
		t.Fatal("old settings cache retained", got, err)
	}
	estimateRow(t, s, now)
	if _, err = s.DB.Exec("UPDATE account_settings SET credential_version=2,revision=revision+1"); err != nil {
		t.Fatal(err)
	}
	a, _ = s.Account(ctx, "a")
	got, err = s.cachedEstimate(ctx, a, "mainland", v.BasisID, now)
	if err != nil || got != nil {
		t.Fatal("old credentials cache retained", got, err)
	}
}

func TestEstimatorRetainsBasisOnFailedReadAndNeverCountsUnknownAgentAsZero(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	ctx := context.Background()
	now := base.Add(120 * time.Second)
	old := estimateRow(t, s, now)
	if _, err := s.DB.Exec("INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,details_json,error_code) SELECT account_id,credential_version,job_id,2,?,'[]','permission_denied' FROM cdt_observation LIMIT 1", timestamp(now)); err != nil {
		t.Fatal(err)
	}
	got := estimateRow(t, s, now)
	if got.BasisID != old.BasisID || got.Estimated == nil || *got.Estimated != *old.Estimated {
		t.Fatal(got)
	}
	if _, err := s.DB.Exec("UPDATE agent SET revoked_at=?", timestamp(now)); err != nil {
		t.Fatal(err)
	}
	got = estimateRow(t, s, now)
	if got.Estimated != nil || got.Status != "incomplete" || !slices.Contains(got.Reasons, "agent_data_unavailable") {
		t.Fatal(got)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = saveObservation(ctx, tx, model.Job{}, model.Account{}, model.CDTObservation{Quality: "verified"}); err == nil {
		t.Fatal("accepted a verified basis without timestamp evidence")
	}
}

func TestEstimateFailureDoesNotStarveOtherAccounts(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	ctx := context.Background()
	if _, err := s.DB.Exec("UPDATE cdt_observation SET mainland_bytes=9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	account := model.Account{ID: "b", Name: "b", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}}
	raw, _ := json.Marshal(account)
	if _, err := s.DB.Exec("INSERT INTO account VALUES('b','b','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO account_settings(account_id,config_json,enabled) VALUES('b',?,1)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshEstimates(ctx, time.UTC, base.Add(120*time.Second)); err == nil {
		t.Fatal("overflow became a finite estimate")
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM traffic_estimate WHERE account_id='b'").Scan(&count); err != nil || count != 2 {
		t.Fatal("one account starved another", count, err)
	}
}

func TestManualCDTConfirmationClearsUnknownAndRespectsExclusion(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	ctx := context.Background()
	now := base.Add(120 * time.Second)

	// Fixture subject has counts_toward_cdt = nil -> cdt_eligibility_unknown is reported.
	v := estimateRow(t, s, now)
	if !slices.Contains(v.Reasons, "cdt_eligibility_unknown") || v.Increment == nil || *v.Increment != 12000 {
		t.Fatalf("expected unknown eligibility initially, got %+v", v)
	}

	// Confirm CDT at account level -> cdt_eligibility_unknown disappears, increment stays 12000.
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) {
		a.CDTConfirmed = true
		return nil, nil
	}, now, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if slices.Contains(v.Reasons, "cdt_eligibility_unknown") || v.Increment == nil || *v.Increment != 12000 {
		t.Fatalf("expected confirmed eligibility, got %+v", v)
	}

	// Explicitly exclude subject via override -> subject is skipped (increment becomes 0) and no unknown reason.
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) {
		a.CDTSubjectOverrides["subject"] = false
		return nil, nil
	}, now, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	v = estimateRow(t, s, now)
	if slices.Contains(v.Reasons, "cdt_eligibility_unknown") || v.Increment == nil || *v.Increment != 0 {
		t.Fatalf("expected excluded subject increment 0, got %+v", v)
	}
}

func TestStoppedInstanceCoversWindowWithZeroTraffic(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	ctx := context.Background()
	// Mark instance stopped after its last sample at base+120s, and advance clock 2 hours later
	// with a fresh official observation so the entire 1h lag window falls after the stop time.
	later := base.Add(2 * time.Hour)
	if _, err := s.DB.Exec("UPDATE instance SET status='stopped' WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,details_json) SELECT account_id,credential_version,job_id,2,?,5000,'[]' FROM cdt_observation LIMIT 1", timestamp(later)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE metering_topology SET last_seen=?", timestamp(later)); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshEstimates(ctx, time.UTC, later); err != nil {
		t.Fatal(err)
	}
	v := estimateRow(t, s, later)
	if v.Status != "ready" || v.Increment == nil || *v.Increment != 0 || v.Estimated == nil || *v.Estimated != 5000 || v.CurrentRate == nil || *v.CurrentRate != 0 {
		t.Fatalf("expected stopped instance to produce ready 0-increment estimate, got %+v", v)
	}
}

package store

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func dailyFixture(t *testing.T) (*Store, model.Account, time.Time, func(time.Time, int64, int64)) {
	t.Helper()
	s := powerStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
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
	a, err := s.Account(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	sub := map[string]any{"id": "outbound", "kind": "ecs_pay_by_traffic", "metering_rule": "outbound_only", "instance_ids": []string{"ecs"}, "counts_toward_cdt": true, "cdt_scope": "mainland"}
	raw, _ := json.Marshal([]any{sub})
	if _, err = s.DB.Exec("INSERT INTO daily_topology(account_id,region,credential_version,fingerprint,observed_from,last_seen,subjects_json) VALUES('a','cn-shanghai',1,'test',?,?,?)", timestamp(base), timestamp(base.Add(48*time.Hour)), string(raw)); err != nil {
		t.Fatal(err)
	}
	var seq int64
	return s, a, base, func(at time.Time, rx, tx int64) {
		t.Helper()
		seq++
		r := agentproto.Report{Version: 1, AgentID: grant.AgentID, CounterEpoch: "day", Sequence: seq, SampledAt: at, UptimeMS: 1000 + at.Sub(base).Milliseconds(), RXBytes: rx, TXBytes: tx, RulesVersion: "v1", ReportIntervalSeconds: 10}
		if _, e := s.AcceptReport(ctx, token, r, at); e != nil {
			t.Fatal(e)
		}
	}
}

func dailyScope(t *testing.T, s *Store, a model.Account, day, now time.Time) model.DailyScope {
	t.Helper()
	tx, err := s.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	v, err := calculateDailyScope(context.Background(), tx, a, "mainland", day, day.AddDate(0, 0, 1), now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDailyCoverageAndMidnightSuccessor(t *testing.T) {
	s, a, base, report := dailyFixture(t)
	report(base, 0, 0)
	report(base.Add(12*time.Hour), 120, 1200)
	v := dailyScope(t, s, a, base, base.Add(24*time.Hour))
	if v.Bytes != nil || v.KnownBytes != 1200 || !v.HasData || v.CompleteDay || v.Status != "partial" {
		t.Fatal(v)
	}
	// A cumulative recovery crossing midnight repairs coverage. It is an upper
	// allocation to the day, not a fabricated uniform split.
	report(base.Add(24*time.Hour+time.Second), 250, 2500)
	v = dailyScope(t, s, a, base, base.Add(24*time.Hour+time.Second))
	if v.Bytes == nil || *v.Bytes != 2500 || !v.CompleteDay || !slices.Contains(v.Reasons, "boundary_upper_allocation") {
		t.Fatal(v)
	}
	if err := s.RefreshDaily(context.Background(), time.UTC, base.Add(24*time.Hour+time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err := s.DailyTraffic(context.Background(), a, time.UTC, base.Add(24*time.Hour+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Days[9].Scopes["mainland"]; !got.CompleteDay || got.Bytes == nil || *got.Bytes != 2500 {
		t.Fatal(got)
	}
	// History before installation remains unknown, not a run of zero days.
	if d.Days[0].Mainland != nil || d.Days[0].Scopes["mainland"].HasData {
		t.Fatal(d.Days[0])
	}
}

func TestDailyLateReceiptsRepairPersistedCache(t *testing.T) {
	s, a, base, report := dailyFixture(t)
	report(base, 0, 0)
	report(base.Add(12*time.Hour), 0, 100)
	now := base.Add(24*time.Hour + time.Minute)
	if err := s.RefreshDaily(context.Background(), time.UTC, now); err != nil {
		t.Fatal(err)
	}
	d, _ := s.DailyTraffic(context.Background(), a, time.UTC, now)
	if d.Days[9].Mainland != nil {
		t.Fatal("missing tail became complete")
	}
	report(now, 0, 200)
	if err := s.RefreshDaily(context.Background(), time.UTC, now); err != nil {
		t.Fatal(err)
	}
	d, _ = s.DailyTraffic(context.Background(), a, time.UTC, now)
	if d.Days[9].Mainland == nil || *d.Days[9].Mainland != 200 {
		t.Fatal(d.Days[9])
	}
	// Re-reading / restarting a worker replaces, never adds, cumulative recovery.
	if err := s.RefreshDaily(context.Background(), time.UTC, now); err != nil {
		t.Fatal(err)
	}
	d, _ = s.DailyTraffic(context.Background(), a, time.UTC, now)
	if *d.Days[9].Mainland != 200 {
		t.Fatal("double counted recovery")
	}
}

func TestDailyTopologyGapsAndKnownEmptyAreExplicit(t *testing.T) {
	s, a, base, report := dailyFixture(t)
	report(base, 0, 0)
	report(base.Add(24*time.Hour), 0, 2400)
	if _, err := s.DB.Exec("UPDATE daily_topology SET observed_from=?", timestamp(base.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	v := dailyScope(t, s, a, base, base.Add(24*time.Hour))
	if v.CompleteDay || v.Bytes != nil || v.KnownBytes != 2400 || !slices.Contains(v.Reasons, "topology_history_incomplete") {
		t.Fatal(v)
	}
	for _, tc := range []struct {
		ips   any
		known bool
	}{{[]string{}, true}, {nil, false}, {[]string{"8.8.8.8"}, false}} {
		raw, _ := json.Marshal([]any{map[string]any{"id": "empty", "kind": "cbwp_dominant_traffic", "metering_rule": "max_in_out_per_period", "counts_toward_cdt": true, "cdt_scope": "mainland", "instance_ids": []string{}, "public_ips": tc.ips}})
		if _, err := s.DB.Exec("UPDATE daily_topology SET observed_from=?,subjects_json=?", timestamp(base), string(raw)); err != nil {
			t.Fatal(err)
		}
		v = dailyScope(t, s, a, base, base.Add(24*time.Hour))
		if v.CompleteDay != tc.known || (v.Bytes != nil) != tc.known || tc.known && *v.Bytes != 0 {
			t.Fatal(tc, v)
		}
	}
}

func TestOfficialDailyRequiresExactVerifiedBoundaryPair(t *testing.T) {
	s, a, base, _ := dailyFixture(t)
	start, end := ReferenceCycle(base, time.UTC)
	j, err := s.EnqueueJob(context.Background(), "refresh", "cdt:a", []string{"a"}, base)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range []int64{100, 800} {
		if _, err = s.DB.Exec(`INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,details_json,covered_until,period_start,period_end,source_quality,source) VALUES('a',1,?,?,?,?,'[]',?,?,?,'verified','daily-test')`, j.ID, i+1, timestamp(base.Add(time.Duration(i)*24*time.Hour)), v, timestamp(base.Add(time.Duration(i)*24*time.Hour)), timestamp(start), timestamp(end)); err != nil {
			t.Fatal(err)
		}
	}
	v := dailyScope(t, s, a, base, base.Add(24*time.Hour))
	if v.Source != "official" || !v.CompleteDay || v.Bytes == nil || *v.Bytes != 700 {
		t.Fatal(v)
	}
	if _, err = s.DB.Exec("UPDATE cdt_observation SET covered_until=? WHERE attempt=2", timestamp(base.Add(24*time.Hour-time.Second))); err != nil {
		t.Fatal(err)
	}
	v = dailyScope(t, s, a, base, base.Add(24*time.Hour))
	if v.Source == "official" || v.Bytes != nil {
		t.Fatal("capture-time difference used as official daily", v)
	}
}

func TestForecastUsesOnlyCompletePriorCalendarDays(t *testing.T) {
	s, a, base, _ := dailyFixture(t)
	ctx := context.Background()
	now := base.Add(7*24*time.Hour + 12*time.Hour)
	start, end := ReferenceCycle(now, time.UTC)
	q := model.QuotaStatus{Scope: "mainland", CycleStart: start, CycleEnd: end, QuotaBytes: 1000, Estimation: &model.TrafficEstimation{Status: "ready", Mode: "unverified", To: now, CalculatedAt: now, Estimated: new(int64(100))}}
	if err := s.forecastQuota(ctx, a, &q, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	if q.Forecast != nil || q.ForecastStatus != "insufficient_data" {
		t.Fatal(q)
	}
	for i := 0; i <= 7; i++ {
		day := base.AddDate(0, 0, i)
		value := int64(10)
		full := true
		if i == 2 {
			full = false
			value = 99999
		}
		if i == 7 {
			value = 88888
		}
		v := model.DailyTrafficDay{Date: day.Format("2006-01-02"), Scopes: map[string]model.DailyScope{"mainland": {Bytes: &value, KnownBytes: value, CompleteDay: full, Source: "agent_estimate", Status: "complete"}}}
		raw, _ := json.Marshal(v)
		if _, err := s.DB.Exec("INSERT INTO daily_work VALUES('a','UTC',?,1,0)", v.Date); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB.Exec("INSERT INTO daily_traffic VALUES('a','UTC',?,?,?,1,?,?)", v.Date, a.CredentialVersion, a.Revision, timestamp(now), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.forecastQuota(ctx, a, &q, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	f := q.Forecast
	if f == nil || f.SampleDays != 6 || f.DailyAverage != 10 || f.CycleEndBytes != 235 || !f.Unverified || slices.Contains(f.SampleDates, now.Format("2006-01-02")) {
		t.Fatal(f)
	}
	if _, err := s.DB.Exec("UPDATE daily_work SET pending=1"); err != nil {
		t.Fatal(err)
	}
	q.Forecast = nil
	if err := s.forecastQuota(ctx, a, &q, time.UTC, now); err != nil {
		t.Fatal(err)
	}
	if q.Forecast != nil {
		t.Fatal("pending repair used for forecast")
	}
}

func TestForecastZeroAndOverflow(t *testing.T) {
	base := time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC)
	f := model.TrafficForecast{BaselineAt: base, BaselineBytes: 100, DailyAverage: 0}
	if err := projectForecast(&f, 50, base.Add(12*time.Hour), time.UTC); err != nil || f.CycleEndBytes != 100 || !f.AlreadyExceeded {
		t.Fatal(f, err)
	}
	f = model.TrafficForecast{BaselineAt: base, BaselineBytes: math.MaxInt64, DailyAverage: 2}
	if err := projectForecast(&f, math.MaxInt64, base.Add(12*time.Hour), time.UTC); err == nil {
		t.Fatal("wrapped overflow")
	}
}

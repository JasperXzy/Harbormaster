package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/JasperXzy/harbormaster/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func quotaInput() model.ProtectionRuleInput {
	return model.ProtectionRuleInput{Name: "quota", Enabled: true, AccountID: "a", Scope: "mainland", ThresholdKind: "bytes", Threshold: 19000, Action: "stop_notify", Targets: []string{"ecs"}, StopMode: "keep_charging", ReserveSeconds: 60, DryRun: true}
}
func saveQuota(t *testing.T, s *Store, in model.ProtectionRuleInput, at time.Time) model.ProtectionRule {
	t.Helper()
	r, err := s.SaveProtectionRule(context.Background(), "", in, at)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func evalQuota(t *testing.T, s *Store, policy model.ProtectionPolicy, at time.Time) model.QuotaDecision {
	t.Helper()
	if err := s.EvaluateQuotaProtection(context.Background(), policy, at); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.QuotaDecisions(context.Background(), "", 0, 200)
	if err != nil || len(rows) == 0 {
		t.Fatal(rows, err)
	}
	return rows[0]
}
func quotaJobs(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM quota_power_job").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func trustQuota(t *testing.T, s *Store, base time.Time) {
	t.Helper()
	start, end := ReferenceCycle(base, time.UTC)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE cdt_observation SET source_quality='verified',covered_until=?,period_start=?,period_end=?,source='trusted-fixture'", []any{timestamp(base.Add(60 * time.Second)), timestamp(start), timestamp(end)}},
		{"UPDATE metering_topology SET quality='verified'", nil},
		{"UPDATE resource_snapshot SET payload_json=json_set(payload_json,'$.counts_toward_cdt',json('true')) WHERE kind='subject'", nil},
	} {
		if _, err := s.DB.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}
func TestQuotaLeadThresholdRehearsalIsDurableAndCannotReplay(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	at := base.Add(120 * time.Second)
	p := model.ProtectionPolicy{SelfKnown: true, Timezone: "UTC"}
	in := quotaInput()
	r := saveQuota(t, s, in, at)
	d := evalQuota(t, s, p, at)
	if d.State != "triggered" || d.Value == nil || *d.Value != 19000 || len(d.Targets) != 1 || d.Targets[0].Outcome != "would_stop" {
		t.Fatalf("%+v", d)
	}
	for range 3 {
		evalQuota(t, s, p, at)
	}
	rows, _, _ := s.QuotaDecisions(context.Background(), r.ID, 0, 50)
	if len(rows) != 1 || len(alertHistory(t, s)) != 1 || quotaJobs(t, s) != 0 {
		t.Fatal("rehearsal duplicated or queued")
	}
	p.PowerEnabled = true
	trustQuota(t, s, base)
	evalQuota(t, s, p, at)
	if quotaJobs(t, s) != 0 {
		t.Fatal("old rehearsal replayed after flags/source changed")
	}
	var count int
	s.DB.QueryRow("SELECT count(*) FROM automatic_power_action").Scan(&count)
	if count != 0 || desired(t, s, "ecs") != "stopped" {
		t.Fatal("dry-run consumed action or intent")
	}
}
func TestQuotaBlocksUnknownSelfExcludedExemptDisabledAndUnqualified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy model.ProtectionPolicy
		exempt bool
		want   string
	}{
		{"unknown", model.ProtectionPolicy{}, false, "self_identity_unknown"},
		{"self", model.ProtectionPolicy{SelfKnown: true, SelfInstanceID: "ecs", PowerEnabled: true}, false, "self_excluded"},
		{"exempt", model.ProtectionPolicy{SelfKnown: true, PowerEnabled: true}, true, "exempt"},
		{"disabled", model.ProtectionPolicy{SelfKnown: true}, false, "power_disabled"},
		{"unqualified", model.ProtectionPolicy{SelfKnown: true, PowerEnabled: true}, false, "source_unqualified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, base := estimateFixture(t)
			at := base.Add(120 * time.Second)
			in := quotaInput()
			in.DryRun = false
			saveQuota(t, s, in, at)
			if tc.exempt {
				if _, err := s.SetQuotaExemption(context.Background(), "ecs", 60, at); err != nil {
					t.Fatal(err)
				}
			}
			p := tc.policy
			p.Timezone = "UTC"
			d := evalQuota(t, s, p, at)
			if len(d.Targets) != 1 || d.Targets[0].Outcome != tc.want || quotaJobs(t, s) != 0 {
				t.Fatalf("%+v", d)
			}
		})
	}
}
func TestQuotaSimulationExpiresWithoutChangingActualLedger(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	at := base.Add(120 * time.Second)
	in := quotaInput()
	in.DryRun = false
	in.Threshold = 100000
	saveQuota(t, s, in, at)
	sim, err := s.SaveQuotaSimulation(context.Background(), model.QuotaSimulation{AccountID: "a", Scope: "mainland", Usage: 100001, ExpiresAt: at.Add(10 * time.Second)}, at)
	if err != nil {
		t.Fatal(err)
	}
	p := model.ProtectionPolicy{SelfKnown: true, PowerEnabled: true, Timezone: "UTC"}
	d := evalQuota(t, s, p, at)
	if !d.Simulation || d.State != "triggered" || d.Targets[0].Outcome != "would_stop" {
		t.Fatal(d)
	}
	d = evalQuota(t, s, p, at.Add(10*time.Second))
	if d.Simulation || d.State != "below" || quotaJobs(t, s) != 0 {
		t.Fatal(d)
	}
	sim.Revision = 0
	sim.ExpiresAt = at.Add(time.Hour)
	updated, err := s.SaveQuotaSimulation(context.Background(), sim, at.Add(11*time.Second))
	if err != nil || updated.Revision != 2 {
		t.Fatal(updated, err)
	}
	var bytes int64
	if err = s.DB.QueryRow("SELECT mainland_bytes FROM cdt_observation LIMIT 1").Scan(&bytes); err != nil || bytes != 1000 {
		t.Fatal("simulation overwrote official", bytes, err)
	}
}
func TestQuotaFreshDispatchRechecksSourcesRuleAndExemption(t *testing.T) {
	for _, change := range []string{"none", "rule", "exemption", "source", "threshold"} {
		t.Run(change, func(t *testing.T) {
			s, _, _, base := estimateFixture(t)
			trustQuota(t, s, base)
			at := base.Add(120 * time.Second)
			in := quotaInput()
			in.Threshold = 6000
			in.DryRun = false
			r := saveQuota(t, s, in, at)
			policy := model.ProtectionPolicy{SelfKnown: true, PowerEnabled: true, Timezone: "UTC"}
			d := evalQuota(t, s, policy, at)
			if len(d.Targets) != 1 || d.Targets[0].Outcome != "queued" || d.Targets[0].JobID == nil {
				t.Fatalf("%+v", d)
			}
			if desired(t, s, "ecs") != "stopped" {
				t.Fatal("automatic queue prematurely changed intent")
			}
			op := claimPower(t, s, at)
			switch change {
			case "rule":
				r.Enabled = false
				if _, err := s.SaveProtectionRule(context.Background(), r.ID, r.ProtectionRuleInput, at); err != nil {
					t.Fatal(err)
				}
			case "exemption":
				if _, err := s.SetQuotaExemption(context.Background(), "ecs", 60, at); err != nil {
					t.Fatal(err)
				}
			case "source":
				if _, err := s.DB.Exec("UPDATE metering_topology SET quality='observed'"); err != nil {
					t.Fatal(err)
				}
			case "threshold":
				if _, err := s.DB.Exec("UPDATE cdt_observation SET mainland_bytes=0"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB.Exec("UPDATE traffic_sample SET tx_bytes=0,rx_bytes=0"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.BeginPowerDispatchWithPolicy(context.Background(), op.Job, policy, at)
			if change == "none" {
				if err != nil || desired(t, s, "ecs") != "quota_stopped" {
					t.Fatal(err)
				}
				var count int
				s.DB.QueryRow("SELECT count(*) FROM automatic_power_action").Scan(&count)
				if count != 1 {
					t.Fatal(count)
				}
			} else if err == nil {
				t.Fatal("stale authorization survived", change)
			}
		})
	}
}
func TestQuotaBudgetSurvivesReopenAndDryRunsDoNotConsumeIt(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	trustQuota(t, s, base)
	at := base.Add(120 * time.Second)
	in := quotaInput()
	in.DryRun = false
	in.Threshold = 6000
	policy := model.ProtectionPolicy{SelfKnown: true, PowerEnabled: true, Timezone: "UTC"}
	for i := 0; i < 3; i++ {
		r := saveQuota(t, s, in, at)
		d := evalQuota(t, s, policy, at)
		if d.Targets[0].Outcome != "queued" {
			t.Fatal(d)
		}
		op := claimPower(t, s, at)
		if _, err := s.BeginPowerDispatchWithPolicy(context.Background(), op.Job, policy, at); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordPowerDispatch(context.Background(), op.Job, "rejected", "power_request_rejected", at); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteProtectionRule(context.Background(), r.ID, r.Revision, at); err != nil {
			t.Fatal(err)
		}
	}
	var dbfile string
	s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbfile)
	other, err := Open(context.Background(), filepath.Dir(dbfile), false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	saveQuota(t, other, in, at)
	d := evalQuota(t, other, policy, at)
	if d.Targets[0].Outcome != "operation_limited" {
		t.Fatal(d)
	}
	var count int
	other.DB.QueryRow("SELECT count(*) FROM automatic_power_action").Scan(&count)
	if count != 3 {
		t.Fatal(count)
	}
}
func TestQuotaExemptionExpiryRearmsAndManualStartDefaultsOneHour(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	at := base.Add(120 * time.Second)
	in := quotaInput()
	saveQuota(t, s, in, at)
	p := model.ProtectionPolicy{SelfKnown: true, Timezone: "UTC"}
	if _, err := s.SetQuotaExemption(context.Background(), "ecs", 5, at); err != nil {
		t.Fatal(err)
	}
	if d := evalQuota(t, s, p, at); d.Targets[0].Outcome != "exempt" {
		t.Fatal(d)
	}
	if d := evalQuota(t, s, p, at.Add(5*time.Second)); d.Targets[0].Outcome != "would_stop" {
		t.Fatal(d)
	}
	if _, err := s.DB.Exec("UPDATE instance SET desired_state='quota_stopped'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueuePowerJob(context.Background(), "ecs", "start", "", model.NewID("manual_"), at); err != nil {
		t.Fatal(err)
	}
	var expires string
	if err := s.DB.QueryRow("SELECT expires_at FROM quota_exemption WHERE instance_id='ecs'").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	e, _ := time.Parse(time.RFC3339Nano, expires)
	if !e.Equal(at.Add(time.Hour)) {
		t.Fatal(expires)
	}
}
func TestQuotaRuleValidationAndRevision(t *testing.T) {
	s := powerStore(t)
	at := time.Now().UTC()
	in := quotaInput()
	r := saveQuota(t, s, in, at)
	if _, err := s.SaveProtectionRule(context.Background(), r.ID, in, at); !errors.Is(err, ErrEstimationRevision) {
		t.Fatal(err)
	}
	in.ThresholdKind = "percent"
	in.Threshold = 101
	if _, err := s.SaveProtectionRule(context.Background(), "", in, at); !errors.Is(err, ErrProtectionRule) {
		t.Fatal(err)
	}
	in = quotaInput()
	in.StopMode = "stop_charging"
	if _, err := s.SaveProtectionRule(context.Background(), "", in, at); !errors.Is(err, ErrProtectionRule) {
		t.Fatal("unknown charge type permitted economical stop", err)
	}
	raw, _ := json.Marshal(r)
	if !json.Valid(raw) {
		t.Fatal("invalid rule DTO")
	}
}

func TestQuotaMultiTargetRequestsStaySeparateAndSelfNeedsConfirmation(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	trustQuota(t, s, base)
	at := base.Add(120 * time.Second)
	if _, err := s.DB.Exec("INSERT INTO instance VALUES('ecs2','a','i-second','ecs','second','cn-shanghai','running','stopped','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','ecs','ecs2','cn-shanghai','{}',1,'now')"); err != nil {
		t.Fatal(err)
	}
	in := quotaInput()
	in.DryRun = false
	in.Threshold = 6000
	in.Targets = []string{"ecs", "ecs2"}
	in.ConfirmedSelfID = "ecs"
	saveQuota(t, s, in, at)
	d := evalQuota(t, s, model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, SelfInstanceID: "ecs", Timezone: "UTC"}, at)
	if len(d.Targets) != 2 || d.Targets[0].Outcome != "queued" || d.Targets[1].Outcome != "queued" || *d.Targets[0].JobID == *d.Targets[1].JobID || quotaJobs(t, s) != 2 {
		t.Fatal(d)
	}
}
func TestQuotaRestoreRetainsLedgerButQuarantinesAndSilencesOldCompletion(t *testing.T) {
	s, _, _, base := estimateFixture(t)
	trustQuota(t, s, base)
	at := base.Add(120 * time.Second)
	in := quotaInput()
	in.Threshold = 6000
	in.DryRun = false
	saveQuota(t, s, in, at)
	p := model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, Timezone: "UTC"}
	evalQuota(t, s, p, at)
	if _, err := s.PrepareRestoredCopy(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if err := s.finishQuotaNotifications(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	notices := alertHistory(t, s)
	if len(notices) != 1 {
		t.Fatal("restore replayed an old completion", notices)
	}
	var expires string
	if err := s.DB.QueryRow("SELECT value FROM app_meta WHERE key='protection_restore_until'").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	until, _ := time.Parse(time.RFC3339Nano, expires)
	if !until.Equal(at.Add(10 * time.Minute)) {
		t.Fatal(expires)
	}
}

func TestQuotaSWASOfficialAndSimulationNeverClaimCutoffAlignment(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	if _, err := s.DB.Exec("UPDATE resource_snapshot SET payload_json='{\"spec\":\"plan\",\"bandwidth_mbps\":8}' WHERE id='swas'"); err != nil {
		t.Fatal(err)
	}
	usage, _ := json.Marshal(model.SWASTrafficUsage{Total: 10000, Used: 8000, Remaining: 2000})
	if _, err := s.DB.Exec("INSERT INTO swas_traffic_cache VALUES('swas',1,'plan',?,?,NULL,1,?)", timestamp(now), timestamp(now), string(usage)); err != nil {
		t.Fatal(err)
	}
	in := quotaInput()
	in.Scope = "swas"
	in.SourceInstanceID = "swas"
	in.Targets = []string{"swas"}
	in.StopMode = ""
	in.ReserveSeconds = 0
	in.ThresholdKind = "percent"
	in.Threshold = 75
	in.DryRun = false
	saveQuota(t, s, in, now)
	d := evalQuota(t, s, model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, Timezone: "UTC"}, now)
	if d.State != "triggered" || d.Threshold == nil || *d.Threshold != 7500 || d.Targets[0].Outcome != "source_unqualified" || quotaJobs(t, s) != 0 {
		t.Fatal(d)
	}
}

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestCDTUsageTiersForecastRecoveryAndUnverifiedExecutionOptIn(t *testing.T) {
	s := powerStore(t)
	ctx := model.WithJobTrigger(context.Background(), "panel")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	setAlertAccount(t, s, "ok", 0)
	// Seed a CDT observation at 85% of 20 GiB mainland quota (17 GiB)
	if _, err := s.DB.Exec(`INSERT INTO job(id,type,scope,account_ids_json,status,created_at) VALUES('j-cdt-1','refresh','cdt:a','["a"]','succeeded',?)`, timestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,overseas_bytes,details_json) VALUES('a',1,'j-cdt-1',1,?,?,0,'[]')`, timestamp(now), int64(17)<<30); err != nil {
		t.Fatal(err)
	}

	// Seed one complete yesterday sample of 3 GiB/day -> remaining ~15.8 days * 3 GiB = ~47 GiB + 17 GiB > 20 GiB -> forecast exceed!
	d := model.DailyTrafficDay{
		Date: "2026-11-14",
		Scopes: map[string]model.DailyScope{
			"mainland": {Bytes: new(int64(3 << 30)), KnownBytes: 3 << 30, HasData: true, Source: "agent_estimate", Status: "complete", CompleteDay: true},
			"overseas": {Bytes: new(int64(0)), KnownBytes: 0, HasData: true, Source: "agent_estimate", Status: "complete", CompleteDay: true},
		},
	}
	rawDay, _ := json.Marshal(d)
	var rev int64
	_ = s.DB.QueryRow("SELECT revision FROM account_settings WHERE account_id='a'").Scan(&rev)
	if _, err := s.DB.Exec(`INSERT INTO daily_traffic(account_id,timezone,day,credential_version,account_revision,generation,calculated_at,payload_json) VALUES('a','Asia/Shanghai','2026-11-14',1,?,1,?,?)`, rev, timestamp(now), string(rawDay)); err != nil {
		t.Fatal(err)
	}

	evaluate(t, s, now)
	// Repeat evaluation in same cycle must not duplicate cdt_usage 80% or cdt_forecast_exceed
	evaluate(t, s, now.Add(time.Minute))
	hist := alertHistory(t, s)
	usage80, forecastFire := 0, 0
	for _, n := range hist {
		if n.RuleKey != nil && *n.RuleKey == "cdt_usage" && strings.Contains(n.Title, "80%") {
			usage80++
		}
		if n.RuleKey != nil && *n.RuleKey == "cdt_forecast_exceed" && n.Kind == "alert" {
			forecastFire++
		}
	}
	if usage80 != 1 || forecastFire != 1 {
		t.Fatal("expected 1 cdt_usage 80% and 1 cdt_forecast_exceed alert", usage80, forecastFire, hist)
	}

	// Update daily sample to 0 -> forecast returns within quota -> recovery!
	d.Scopes["mainland"] = model.DailyScope{Bytes: new(int64(0)), KnownBytes: 0, HasData: true, Source: "agent_estimate", Status: "complete", CompleteDay: true}
	rawDay, _ = json.Marshal(d)
	if _, err := s.DB.Exec(`UPDATE daily_traffic SET payload_json=? WHERE account_id='a' AND day='2026-11-14'`, string(rawDay)); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(2*time.Minute))
	hist = alertHistory(t, s)
	recovered := false
	for _, n := range hist {
		if n.RuleKey != nil && *n.RuleKey == "cdt_forecast_exceed" && n.Kind == "recovery" && strings.Contains(n.Title, "够用了") {
			recovered = true
		}
	}
	if !recovered {
		t.Fatal("expected cdt_forecast_exceed recovery", hist)
	}

	// Test QuotaProtection AllowUnverifiedExecution opt-in:
	// First rule without AllowUnverifiedExecution -> source_unqualified;
	// Second rule with AllowUnverifiedExecution=true -> queues real stop job when official (17 GiB) >= 80% (16 GiB)!
	policy := model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, Timezone: "UTC"}
	r1, err := s.SaveProtectionRule(ctx, "", model.ProtectionRuleInput{
		Name: "strict", Enabled: true, AccountID: "a", Scope: "mainland", ThresholdKind: "percent", Threshold: 80,
		Action: "stop_notify", Targets: []string{"ecs"}, StopMode: "keep_charging", ReserveSeconds: 0, DryRun: false, AllowUnverifiedExecution: false,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateQuotaProtection(ctx, policy, now); err != nil {
		t.Fatal(err)
	}
	decs, _, err := s.QuotaDecisions(ctx, r1.ID, 0, 10)
	if err != nil || len(decs) != 1 || decs[0].Targets[0].Outcome != "source_unqualified" {
		t.Fatal("expected source_unqualified without opt-in", err, decs)
	}

	r1.AllowUnverifiedExecution = true
	r1, err = s.SaveProtectionRule(ctx, r1.ID, r1.ProtectionRuleInput, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateQuotaProtection(ctx, policy, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	decs, _, err = s.QuotaDecisions(ctx, r1.ID, 0, 10)
	if err != nil || len(decs) == 0 || decs[0].Targets[0].Outcome != "queued" || decs[0].Targets[0].JobID == nil {
		t.Fatal("expected queued stop job with allow_unverified_execution", err, decs)
	}
	// Verify dispatch authorization succeeds too!
	op := claimPower(t, s, now.Add(3*time.Second))
	if _, err := s.BeginPowerDispatchWithPolicy(ctx, op.Job, policy, now.Add(3*time.Second)); err != nil {
		t.Fatal("expected unverified opt-in dispatch authorization to pass", err)
	}
}

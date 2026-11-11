package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestAgentCadenceRatesGapsAndReplay(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	code, _, err := s.AgentGrant(ctx, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	reg := registration(code, "i-test")
	g, err := s.RegisterAgent(ctx, reg, now)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, reg.Nonce)
	r := agentproto.Report{Version: 1, AgentID: g.AgentID, CounterEpoch: "cadence", Sequence: 1, UptimeMS: 1000, SampledAt: now}
	accept := func() {
		t.Helper()
		if _, err := s.AcceptReport(ctx, token, r, r.SampledAt); err != nil {
			t.Fatal(err)
		}
	}
	view := func(at time.Time) model.AgentView {
		t.Helper()
		vs, err := s.AgentViews(ctx, at)
		if err != nil {
			t.Fatal(err)
		}
		return vs["ecs"]
	}
	if view(now).Agent.ReportIntervalSeconds != nil {
		t.Fatal("cadence before first report")
	}
	accept()
	r.ReportIntervalSeconds = 10 // Explicit default is equivalent to legacy omission.
	if dup, err := s.AcceptReport(ctx, token, r, now); err != nil || !dup {
		t.Fatal(dup, err)
	}
	r.ReportIntervalSeconds = 60
	if _, err := s.AcceptReport(ctx, token, r, now); !errors.Is(err, ErrConflict) {
		t.Fatal("changed duplicate cadence accepted", err)
	}
	r.Sequence++
	r.UptimeMS += 60000
	r.SampledAt = now.Add(time.Minute)
	r.RXBytes = 6000
	accept()
	v := view(r.SampledAt)
	if v.Agent.ReportIntervalSeconds == nil || *v.Agent.ReportIntervalSeconds != 60 || v.Realtime == nil || v.Realtime.RX != 100 {
		t.Fatal(v)
	}
	// Cadence does not silently extend the operator's offline threshold.
	if view(r.SampledAt.Add(30*time.Second)).Agent.Status != "offline" {
		t.Fatal("offline threshold overridden")
	}
	threshold := 240
	if _, err := s.UpdateAlertRule(ctx, "agent_offline", model.AlertRuleUpdate{Params: &model.AlertParams{OfflineAfterSeconds: &threshold}}, now); err != nil {
		t.Fatal(err)
	}
	if view(r.SampledAt.Add(40*time.Second)).Realtime == nil {
		t.Fatal("valid minute cadence rate hidden after 30s")
	}
	if view(r.SampledAt.Add(181*time.Second)).Realtime != nil {
		t.Fatal("expired rate retained")
	}
	// Missing samples are a gap, then a normal interval recovers the live rate.
	r.Sequence++
	r.UptimeMS += 181000
	r.SampledAt = r.SampledAt.Add(181 * time.Second)
	r.RXBytes += 18100
	accept()
	v = view(r.SampledAt)
	if v.Realtime != nil || v.Agent.Quality == nil || *v.Agent.Quality != "gap" {
		t.Fatal(v)
	}
	r.Sequence++
	r.UptimeMS += 60000
	r.SampledAt = r.SampledAt.Add(time.Minute)
	r.RXBytes += 6000
	accept()
	if view(r.SampledAt).Realtime == nil {
		t.Fatal("normal interval did not recover")
	}
	// Stale buffered samples must not become heartbeats just because cadence is longer.
	r.Sequence++
	r.UptimeMS += 60000
	r.SampledAt = r.SampledAt.Add(time.Minute)
	if _, err := s.AcceptReport(ctx, token, r, r.SampledAt.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	v = view(r.SampledAt.Add(40 * time.Second))
	if v.Agent.Quality == nil || *v.Agent.Quality != "stale" || !v.Agent.LastReportAt.Equal(r.SampledAt.Add(-time.Minute)) {
		t.Fatal(v)
	}
	// Re-registration must hide the old identity's cadence until a new fresh report.
	code, _, err = s.AgentGrant(ctx, "ecs", r.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterAgent(ctx, registration(code, "i-test"), r.SampledAt); err != nil {
		t.Fatal(err)
	}
	if view(r.SampledAt).Agent.ReportIntervalSeconds != nil {
		t.Fatal("old registration cadence leaked")
	}
}

func TestInvalidAgentCadenceRejected(t *testing.T) {
	s := agentStore(t)
	now := time.Now()
	for _, interval := range []int{-1, 61} {
		r := agentproto.Report{Version: 1, AgentID: "invalid", CounterEpoch: "epoch", Sequence: 1, SampledAt: now, ReportIntervalSeconds: interval}
		if _, err := s.AcceptReport(context.Background(), "token-not-real-at-all", r, now); err == nil || err.Error() != "invalid_report_interval" {
			t.Fatal(interval, err)
		}
	}
}

func TestCadenceMigrationPreservesLegacyReplay(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := agentproto.Report{Version: 1, AgentID: agentproto.DemoAgentID, CounterEpoch: "legacy", Sequence: 1, SampledAt: now}
	if _, err := s.AcceptReport(ctx, agentproto.DemoToken, r, now); err != nil {
		t.Fatal(err)
	}
	// Recreate the preceding schema with an existing legacy sample and ledger.
	for _, query := range []string{
		"ALTER TABLE agent_counter_state DROP COLUMN report_interval_seconds",
		"ALTER TABLE traffic_sample_detail DROP COLUMN report_interval_seconds",
		"DELETE FROM schema_migration WHERE name='008_agent_cadence.sql'",
	} {
		if _, err := s.DB.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_counter_state", "traffic_sample_detail"} {
		var cadence int
		if err := s.DB.QueryRowContext(ctx, "SELECT report_interval_seconds FROM "+table+" WHERE agent_id=?", r.AgentID).Scan(&cadence); err != nil || cadence != 10 {
			t.Fatal(table, cadence, err)
		}
	}
	if dup, err := s.AcceptReport(ctx, agentproto.DemoToken, r, now); err != nil || !dup {
		t.Fatal(dup, err)
	}
}

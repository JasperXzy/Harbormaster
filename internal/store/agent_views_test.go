package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

func TestAgentViewsFreshnessRatesAndRevocation(t *testing.T) {
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
	views, err := s.AgentViews(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if !views["ecs"].Agent.Pending || views["ecs"].Agent.Status != "offline" || views["ecs"].Realtime != nil {
		t.Fatal(views)
	}
	report := agentproto.Report{Version: 1, AgentID: g.AgentID, CounterEpoch: "epoch", Sequence: 1, UptimeMS: 1000, SampledAt: now, RXBytes: 100, TXBytes: 50}
	if _, err = s.AcceptReport(ctx, token, report, now); err != nil {
		t.Fatal(err)
	}
	views, err = s.AgentViews(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if views["ecs"].Agent.Status != "online" || views["ecs"].Agent.Pending || views["ecs"].Realtime != nil {
		t.Fatal(views)
	}
	report.Sequence = 2
	report.UptimeMS = 11000
	report.SampledAt = now.Add(10 * time.Second)
	report.RXBytes = 1100
	report.TXBytes = 550
	if _, err = s.AcceptReport(ctx, token, report, report.SampledAt); err != nil {
		t.Fatal(err)
	}
	views, err = s.AgentViews(ctx, report.SampledAt)
	if err != nil {
		t.Fatal(err)
	}
	rate := views["ecs"].Realtime
	if rate == nil || rate.RX != 100 || rate.TX != 50 {
		t.Fatal(rate)
	}
	views, err = s.AgentViews(ctx, report.SampledAt.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if views["ecs"].Agent.Status != "offline" || views["ecs"].Realtime != nil || views["ecs"].Agent.OfflineSince == nil {
		t.Fatal(views)
	}
	// An exact duplicate must not refresh the online deadline.
	if _, err = s.AcceptReport(ctx, token, report, report.SampledAt.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err = s.AgentViews(ctx, report.SampledAt.Add(30*time.Second))
	if err != nil || views["ecs"].Agent.Status != "offline" {
		t.Fatal(views, err)
	}
	// Reset is a baseline even if its absolute counters exceed the last epoch.
	report.CounterEpoch = "next"
	report.Sequence = 1
	report.UptimeMS = 1
	if _, err = s.AcceptReport(ctx, token, report, report.SampledAt); err != nil {
		t.Fatal(err)
	}
	views, err = s.AgentViews(ctx, report.SampledAt)
	if err != nil || views["ecs"].Realtime != nil {
		t.Fatal(views, err)
	}
	if err = s.RevokeAgent(ctx, "ecs", report.SampledAt); err != nil {
		t.Fatal(err)
	}
	views, err = s.AgentViews(ctx, report.SampledAt)
	if err != nil || views["ecs"].Agent.Status != "not_installed" || views["ecs"].Realtime != nil {
		t.Fatal(views, err)
	}
}

func TestRateArithmeticDoesNotWrap(t *testing.T) {
	if _, ok := byteRate(math.MaxInt64, 1); ok {
		t.Fatal("overflow accepted")
	}
	if rate, ok := byteRate(math.MaxInt64, 1000); !ok || rate != math.MaxInt64 {
		t.Fatal(rate, ok)
	}
}

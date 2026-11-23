package store

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestHourlyProrationAttributionPricingAndRetention(t *testing.T) {
	s := agentStore(t)
	ctx := model.WithJobTrigger(context.Background(), "panel")
	base := time.Date(2026, 11, 10, 9, 59, 50, 0, time.UTC)

	code, _, err := s.AgentGrant(ctx, "ecs", base)
	if err != nil {
		t.Fatal(err)
	}
	reg := registration(code, "i-test")
	res, err := s.RegisterAgent(ctx, reg, base)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, reg.Nonce)

	// Baseline at 09:59:50, crossing boundary to 10:00:10 (20s span: 10s in 09:00, 10s in 10:00),
	// then pure sample at 10:00:20.
	reports := []agentproto.Report{
		{Version: 1, AgentID: res.AgentID, AgentVersion: "v0.2.0", CounterEpoch: "ep1", Sequence: 1, SampledAt: base, UptimeMS: 10000, RXBytes: 0, TXBytes: 0, RulesVersion: "v1", ReportIntervalSeconds: 10},
		{Version: 1, AgentID: res.AgentID, AgentVersion: "v0.2.0", CounterEpoch: "ep1", Sequence: 2, SampledAt: base.Add(20 * time.Second), UptimeMS: 30000, RXBytes: 2000, TXBytes: 4000, RulesVersion: "v1", ReportIntervalSeconds: 10},
		{Version: 1, AgentID: res.AgentID, AgentVersion: "v0.2.0", CounterEpoch: "ep1", Sequence: 3, SampledAt: base.Add(30 * time.Second), UptimeMS: 40000, RXBytes: 2500, TXBytes: 5000, RulesVersion: "v1", ReportIntervalSeconds: 10},
	}
	for _, r := range reports {
		if _, err := s.AcceptReport(ctx, token, r, r.SampledAt); err != nil {
			t.Fatal(err)
		}
	}

	now := base.Add(10 * 24 * time.Hour)
	if err := s.RefreshHourly(ctx, now); err != nil {
		t.Fatal(err)
	}
	buckets, err := s.AgentHourlyHistory(ctx, "ecs", base.Add(-time.Hour), base.Add(2*time.Hour))
	if err != nil || len(buckets) != 2 {
		t.Fatal("expected 2 hourly buckets", err, buckets)
	}
	// Hour 09:00 gets 50% of 2000/4000 = 1000/2000; Hour 10:00 gets 1000/2000 + 500/1000 = 1500/3000.
	if buckets[0].RXBytes != 1000 || buckets[0].TXBytes != 2000 || !buckets[0].Estimated {
		t.Fatal("unexpected hour 09 bucket", buckets[0])
	}
	if buckets[1].RXBytes != 1500 || buckets[1].TXBytes != 3000 || !buckets[1].Estimated {
		t.Fatal("unexpected hour 10 bucket", buckets[1])
	}

	// Attribution overlay test on subject and package
	subjects := []map[string]any{
		{
			"id":                "subj-ecs",
			"account_id":        "a",
			"kind":              "ecs_public_ip_traffic",
			"metering_rule":     "outbound_only",
			"counts_toward_cdt": true,
			"cdt_scope":         "mainland",
			"instance_ids":      []any{"ecs"},
			"data_source":       "none",
		},
	}
	if err := s.OverlaySubjectAndPackageTraffic(ctx, subjects, "subject", time.UTC, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cMet, ok := subjects[0]["cycle_metered_bytes"].(*int64)
	if !ok || cMet == nil || *cMet != 5000 || subjects[0]["data_source"] != "agent" || subjects[0]["cycle_share_percent"] != 100.0 {
		t.Fatal("unexpected subject attribution", subjects[0])
	}

	// Tiered overage pricing test
	pricing, err := s.TrafficPricing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cost := model.ComputeTieredCost(15*(1<<30), pricing.China.MainlandTiers)
	if cost != "12.00" { // 15 GiB * 0.80 = 12.00
		t.Fatal("unexpected tiered cost", cost)
	}
	pricing.China.MainlandTiers = []model.PriceTier{{UpToGiB: nil, UnitPrice: "0.75"}}
	saved, err := s.SaveTrafficPricing(ctx, pricing, now)
	if err != nil || saved.Revision != 1 {
		t.Fatal("failed saving pricing", err, saved)
	}

	// Retention test: raw samples are 10 days old (> 7d default) and rolled up -> first 2 pruned, latest counter baseline (seq 3) retained!
	ret := model.DefaultRetentionSettings(30)
	pruned, err := s.PruneDataHistory(ctx, now, ret, 500)
	if err != nil || pruned.RawAgentSamples != 2 {
		t.Fatal("unexpected raw sample prune result", err, pruned)
	}
	// Hourly buckets remain intact after raw sample pruning!
	bucketsAfter, err := s.AgentHourlyHistory(ctx, "ecs", base.Add(-time.Hour), base.Add(2*time.Hour))
	if err != nil || len(bucketsAfter) != 2 || bucketsAfter[1].TXBytes != 3000 {
		t.Fatal("hourly buckets lost after raw prune", err, bucketsAfter)
	}
}

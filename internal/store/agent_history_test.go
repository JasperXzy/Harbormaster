package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func historyAgent(t *testing.T) (*Store, string, string, time.Time) {
	t.Helper()
	s := agentStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	code, _, err := s.AgentGrant(ctx, "ecs", base)
	if err != nil {
		t.Fatal(err)
	}
	registration := registration(code, "i-test")
	g, err := s.RegisterAgent(ctx, registration, base)
	if err != nil {
		t.Fatal(err)
	}
	return s, g.AgentID, agentproto.DeriveToken(code, registration.Nonce), base
}
func TestAgentHistoryDelayedIntervalsGapsResetsAndClockRegression(t *testing.T) {
	s, id, token, base := historyAgent(t)
	ctx := context.Background()
	received := time.Now().UTC()
	samples := []agentproto.Report{
		{CounterEpoch: "old", Sequence: 1, SampledAt: base, UptimeMS: 1000, RXBytes: 100, TXBytes: 200},
		{CounterEpoch: "old", Sequence: 2, SampledAt: base.Add(10 * time.Second), UptimeMS: 11000, RXBytes: 1100, TXBytes: 2200},
		{CounterEpoch: "old", Sequence: 4, SampledAt: base.Add(20 * time.Second), UptimeMS: 21000, RXBytes: 2100, TXBytes: 3200},
		{CounterEpoch: "new", Sequence: 1, SampledAt: base.Add(30 * time.Second), UptimeMS: 1000, RXBytes: 50, TXBytes: 60},
		{CounterEpoch: "new", Sequence: 2, SampledAt: base.Add(25 * time.Second), UptimeMS: 11000, RXBytes: 1050, TXBytes: 1060},
		{CounterEpoch: "new", Sequence: 3, SampledAt: base.Add(40 * time.Second), UptimeMS: 21000, RXBytes: 2050, TXBytes: 2060},
		{CounterEpoch: "new", Sequence: 4, SampledAt: base.Add(50 * time.Second), UptimeMS: 31000, RXBytes: 3050, TXBytes: 3060},
	}
	for i := range samples {
		samples[i].Version = 1
		samples[i].AgentID = id
		samples[i].RulesVersion = "rules-v1"
	}
	if _, err := s.AcceptReports(ctx, token, samples, received); err != nil {
		t.Fatal(err)
	}
	f := model.AgentNetworkHistoryFilter{InstanceID: "ecs", From: base.Add(5 * time.Second), To: received}
	rows, err := s.AgentNetworkHistory(ctx, f, 100)
	if err != nil || len(rows) != 6 {
		t.Fatal(rows, err)
	}
	for _, v := range rows {
		switch {
		case v.CounterEpoch == "old" && v.Sequence == 2:
			if v.Quality != "stale" || v.IntervalStatus != "continuous" || v.Interval == nil || v.Interval.RXBytes != 1000 || v.Interval.TXBytes != 2000 || v.Interval.RXRate != 100 || v.Interval.TXRate != 200 || !v.Interval.StartAt.Before(f.From) {
				t.Fatal("delayed/boundary interval", v)
			}
		case v.CounterEpoch == "old" && v.Sequence == 4:
			if v.IntervalStatus != "gap" || v.Interval == nil || v.Interval.PreviousSequence != 2 {
				t.Fatal("missing sequence hidden", v)
			}
		case v.CounterEpoch == "new" && v.Sequence == 1:
			if v.Interval != nil || v.IntervalUnavailableReason == nil || *v.IntervalUnavailableReason != "no_previous_sample" {
				t.Fatal("bridged reboot", v)
			}
		case v.CounterEpoch == "new" && (v.Sequence == 2 || v.Sequence == 3):
			if v.Interval != nil || v.IntervalUnavailableReason == nil || *v.IntervalUnavailableReason != "clock_skew" {
				t.Fatal("clock jump used", v)
			}
			if v.Sequence == 2 && v.Quality != "clock_skew" {
				t.Fatal("delay overwrote backward-clock quality", v)
			}
		case v.CounterEpoch == "new" && v.Sequence == 4:
			if v.IntervalStatus != "continuous" || v.Interval == nil {
				t.Fatal("failed to recover", v)
			}
		}
	}
	// Old versions stored stale over backward-clock quality. Reconstruct the
	// predecessor's order as well, instead of trusting that lost annotation.
	if _, err = s.DB.Exec("UPDATE traffic_sample_detail SET quality='stale' WHERE agent_id=? AND counter_epoch='new' AND sequence=2", id); err != nil {
		t.Fatal(err)
	}
	rows, err = s.AgentNetworkHistory(ctx, f, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rows {
		if v.CounterEpoch == "new" && v.Sequence == 3 && v.Interval != nil {
			t.Fatal("legacy clock jump bridged")
		}
	}
	if err = s.RevokeAgent(ctx, "ecs", received); err != nil {
		t.Fatal(err)
	}
	rows, err = s.AgentNetworkHistory(ctx, f, 100)
	if err != nil || len(rows) != 6 {
		t.Fatal("revoke erased history", err)
	}
	f.InstanceID = "swas"
	rows, err = s.AgentNetworkHistory(ctx, f, 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("uninstalled agent fabricated", rows, err)
	}
	f.InstanceID = "missing"
	if _, err = s.AgentNetworkHistory(ctx, f, 100); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestAgentHistoryPaginationAcrossEpochsAndEqualTimestamps(t *testing.T) {
	s, id, token, base := historyAgent(t)
	ctx := context.Background()
	received := time.Now().UTC()
	reports := []agentproto.Report{{Version: 1, AgentID: id, CounterEpoch: "a", Sequence: 1, SampledAt: base, UptimeMS: 1}, {Version: 1, AgentID: id, CounterEpoch: "a", Sequence: 2, SampledAt: base, UptimeMS: 2}, {Version: 1, AgentID: id, CounterEpoch: "b", Sequence: 1, SampledAt: base, UptimeMS: 1}}
	if _, err := s.AcceptReports(ctx, token, reports, received); err != nil {
		t.Fatal(err)
	}
	f := model.AgentNetworkHistoryFilter{InstanceID: "ecs", From: base, To: received}
	seen := map[string][]int64{}
	for range 3 {
		rows, err := s.AgentNetworkHistory(ctx, f, 1)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		v := rows[0]
		seen[v.CounterEpoch] = append(seen[v.CounterEpoch], v.Sequence)
		f.BeforeTime = v.SampledAt
		f.BeforeEpoch = v.CounterEpoch
		f.BeforeSequence = v.Sequence
	}
	rows, err := s.AgentNetworkHistory(ctx, f, 1)
	if err != nil || len(rows) != 0 || len(seen["a"]) != 2 || len(seen["b"]) != 1 {
		t.Fatal("pagination lost or repeated points", rows, seen, err)
	}
}
func TestAgentIntervalReasonsAndOverflow(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Second)
	rules := "r1"
	cadence := 10
	valid := sql.NullInt64{Valid: true}
	p := agentHistoryPrevious{Sequence: sql.NullInt64{Int64: 1, Valid: true}, Uptime: valid, RX: valid, TX: valid, At: &start, Rules: sql.NullString{String: rules, Valid: true}, Quality: sql.NullString{String: "ok", Valid: true}}
	for _, tc := range []struct {
		name, want string
		mutate     func(*model.AgentNetworkSample, *agentHistoryPrevious)
	}{
		{"legacy", "metadata_unknown", func(v *model.AgentNetworkSample, p *agentHistoryPrevious) { v.RulesVersion = nil }},
		{"rules", "rules_changed", func(v *model.AgentNetworkSample, p *agentHistoryPrevious) { p.Rules.String = "old" }},
		{"counter", "invalid_counter", func(v *model.AgentNetworkSample, p *agentHistoryPrevious) { p.RX.Int64 = 2 }},
		{"overflow", "rate_overflow", func(v *model.AgentNetworkSample, p *agentHistoryPrevious) { v.UptimeMS = 1; v.RXBytes = math.MaxInt64 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := p
			v := model.AgentNetworkSample{Sequence: 2, SampledAt: now, UptimeMS: 1000, RXBytes: 1, TXBytes: 1, Quality: "ok", RulesVersion: &rules, ReportIntervalSeconds: &cadence}
			tc.mutate(&v, &prior)
			deriveAgentHistoryInterval(&v, prior)
			if v.Interval != nil || v.IntervalStatus != "unavailable" || v.IntervalUnavailableReason == nil || *v.IntervalUnavailableReason != tc.want {
				t.Fatal(v)
			}
		})
	}
}

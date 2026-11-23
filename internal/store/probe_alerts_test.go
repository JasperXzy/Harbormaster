package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type probeAlertFixture struct {
	s    *Store
	plan model.ProbePlan
	now  time.Time
	t    *testing.T
}

func newProbeAlertFixture(t *testing.T) *probeAlertFixture {
	t.Helper()
	s := agentStore(t)
	now := time.Now().UTC()
	plan := createProbePlan(t, s, "target_cloudflare_dns", now)
	plan.Count = 20
	var err error
	plan, err = s.SaveProbePlan(context.Background(), plan.ID, plan, now)
	if err != nil {
		t.Fatal(err)
	}
	entries := []model.ProbeThreshold{{PlanID: plan.ID, LatencyMS: new(float64(100))}}
	if _, err = s.UpdateAlertRule(context.Background(), "network_latency", model.AlertRuleUpdate{Params: &model.AlertParams{ProbeThresholds: &entries, DurationSeconds: new(600), RecoverySamples: new(3)}}, now); err != nil {
		t.Fatal(err)
	}
	return &probeAlertFixture{s: s, plan: plan, now: now, t: t}
}
func (f *probeAlertFixture) run(ms float64, failed int, code string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.s.ScheduleProbePlans(ctx, f.now); err != nil {
		f.t.Fatal(err)
	}
	claim, err := f.s.ClaimServerProbe(ctx, f.now)
	if err != nil {
		f.t.Fatal(err)
	}
	result := netprobe.Result{StartedAt: f.now, FinishedAt: f.now.Add(time.Second), ResolvedAddresses: []string{}, Samples: []netprobe.Sample{}}
	if code == "" {
		result.Address = claim.Request.Host
		result.ResolvedAddresses = []string{result.Address}
		for i := 0; i < claim.Request.Count; i++ {
			sample := netprobe.Sample{Sequence: i + 1, StartedAt: f.now}
			if i < failed {
				sample.ErrorCode = new("probe_timeout")
			} else {
				sample.RTTMS = new(ms)
			}
			result.Samples = append(result.Samples, sample)
		}
	}
	if err = f.s.FinishServerProbe(ctx, claim, result, code, f.now.Add(time.Second)); err != nil {
		f.t.Fatal(err)
	}
	if err = f.s.EvaluateAlerts(ctx, f.now.Add(2*time.Second)); err != nil {
		f.t.Fatal(err)
	}
	f.now = f.now.Add(5 * time.Minute)
}
func (f *probeAlertFixture) notices(key string) []model.Notification {
	f.t.Helper()
	rows, err := f.s.DB.Query("SELECT "+notificationColumns+" FROM notification WHERE rule_key=? AND probe_plan_id=? ORDER BY seq", key, f.plan.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := []model.Notification{}
	for rows.Next() {
		n, err := readNotification(rows)
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, n)
	}
	if err = rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}
func TestProbeLatencyDurationRecoveryGapsAndNoRepeatedCounting(t *testing.T) {
	f := newProbeAlertFixture(t)
	ctx := context.Background()
	f.run(150, 0, "")
	f.run(150, 0, "")
	if len(f.notices("network_latency")) != 0 {
		t.Fatal("fired before ten minutes")
	}
	f.run(150, 0, "")
	items := f.notices("network_latency")
	if len(items) != 1 || items[0].Kind != "alert" || items[0].Urgent || items[0].ProbePlanID == nil {
		t.Fatal(items)
	}
	for i := 0; i < 5; i++ {
		if err := f.s.EvaluateAlerts(ctx, f.now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.notices("network_latency")) != 1 {
		t.Fatal("polling counted one result repeatedly")
	}
	f.run(90, 0, "")
	f.run(90, 0, "")
	if len(f.notices("network_latency")) != 1 {
		t.Fatal("premature recovery")
	}
	f.run(0, 0, "probe_unavailable") // Unknown breaks the recovery streak.
	f.run(90, 0, "")
	f.run(90, 0, "")
	if len(f.notices("network_latency")) != 1 {
		t.Fatal("unknown recovered")
	}
	f.run(90, 0, "")
	items = f.notices("network_latency")
	if len(items) != 2 || items[1].Kind != "recovery" {
		t.Fatal(items)
	}
	// A missing period breaks the sampled ten-minute continuity.
	f.run(150, 0, "")
	f.now = f.now.Add(5 * time.Minute)
	f.run(150, 0, "")
	f.run(150, 0, "")
	if len(f.notices("network_latency")) != 2 {
		t.Fatal("bridged missing period")
	}
	f.run(150, 0, "")
	if len(f.notices("network_latency")) != 3 {
		t.Fatal("new episode missing")
	}
}
func TestProbeLossStrictThresholdAndUnreachableCount(t *testing.T) {
	f := newProbeAlertFixture(t)
	f.run(10, 1, "") // Exactly 5%, not greater than 5%.
	if len(f.notices("network_loss")) != 0 {
		t.Fatal("inclusive loss threshold")
	}
	f.run(10, 2, "")
	if len(f.notices("network_loss")) != 1 {
		t.Fatal("loss not raised")
	}
	f.run(0, 20, "")
	f.run(0, 20, "")
	if len(f.notices("network_unreachable")) != 0 {
		t.Fatal("unreachable before three complete runs")
	}
	f.run(0, 20, "")
	if len(f.notices("network_unreachable")) != 1 {
		t.Fatal("unreachable not raised")
	}
	if len(f.notices("network_latency")) != 0 {
		t.Fatal("missing RTT treated as latency")
	}
	f.run(10, 0, "")
	f.run(10, 0, "")
	if len(f.notices("network_loss")) != 1 || len(f.notices("network_unreachable")) != 1 {
		t.Fatal("premature recovery")
	}
	f.run(10, 0, "")
	if len(f.notices("network_loss")) != 2 || len(f.notices("network_unreachable")) != 2 {
		t.Fatal("recovery missing")
	}
}
func TestProbeAlertQuietRecordsNoCatchUpAndDisableNoRecovery(t *testing.T) {
	f := newProbeAlertFixture(t)
	channelFixture(t, f.s, 0, f.now)
	ctx := context.Background()
	start := f.now.Add(-time.Minute)
	end := f.now.Add(time.Hour)
	if _, err := f.s.SaveQuietHours(ctx, model.QuietHours{Start: "23:00", End: "08:00", Maintenance: &model.MaintenanceWindow{StartAt: start, EndAt: end}}, f.now); err != nil {
		t.Fatal(err)
	}
	f.run(10, 20, "")
	items := f.notices("network_loss")
	if len(items) != 1 || items[0].DeliveryStatus != "not_sent" || items[0].SuppressionReason == nil || *items[0].SuppressionReason != "maintenance" {
		t.Fatal(items)
	}
	if _, err := f.s.SaveQuietHours(ctx, model.QuietHours{Revision: 1, Start: "23:00", End: "08:00"}, f.now); err != nil {
		t.Fatal(err)
	}
	f.run(10, 20, "")
	if len(f.notices("network_loss")) != 1 {
		t.Fatal("quiet event replayed")
	}
	f.plan.Enabled = false
	if _, err := f.s.SaveProbePlan(ctx, f.plan.ID, f.plan, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.s.EvaluateAlerts(ctx, f.now); err != nil {
		t.Fatal(err)
	}
	if len(f.notices("network_loss")) != 1 {
		t.Fatal("disabling fabricated recovery")
	}
	var active int
	if err := f.s.DB.QueryRow("SELECT active FROM alert_state WHERE rule_key='network_loss' AND target_id=?", f.plan.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal(active, err)
	}
}
func TestProbeRuleOverrideBaselinesAndOtherPlanEdits(t *testing.T) {
	f := newProbeAlertFixture(t)
	ctx := context.Background()
	f.run(150, 0, "")
	f.run(150, 0, "")
	other := createProbePlan(t, f.s, "target_google_dns", f.now)
	entries := []model.ProbeThreshold{{PlanID: f.plan.ID, LatencyMS: new(float64(100))}, {PlanID: other.ID, LatencyMS: new(float64(300))}}
	_, err := f.s.UpdateAlertRule(ctx, "network_latency", model.AlertRuleUpdate{Params: &model.AlertParams{ProbeThresholds: &entries, DurationSeconds: new(600), RecoverySamples: new(3)}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the other plan out of this test's execution queue.
	other.Enabled = false
	if _, err = f.s.SaveProbePlan(ctx, other.ID, other, f.now); err != nil {
		t.Fatal(err)
	}
	f.run(150, 0, "")
	if len(f.notices("network_latency")) != 1 {
		t.Fatal("editing another plan reset existing streak")
	}
	overrides := []model.ProbeThreshold{{PlanID: f.plan.ID, Enabled: new(false)}}
	if _, err = f.s.UpdateAlertRule(ctx, "network_loss", model.AlertRuleUpdate{Params: &model.AlertParams{ProbeThresholds: &overrides, RecoverySamples: new(3)}}, f.now); err != nil {
		t.Fatal(err)
	}
	f.run(0, 20, "")
	if len(f.notices("network_loss")) != 0 {
		t.Fatal("disabled override fired")
	}
	for _, items := range [][]model.ProbeThreshold{{{PlanID: "missing"}}, {{PlanID: f.plan.ID}, {PlanID: f.plan.ID}}, {{PlanID: f.plan.ID, LossPercent: new(float64(101))}}, {{PlanID: f.plan.ID, LatencyMS: new(float64(10))}}} {
		_, err = f.s.UpdateAlertRule(ctx, "network_loss", model.AlertRuleUpdate{Params: &model.AlertParams{ProbeThresholds: &items, RecoverySamples: new(3)}}, f.now)
		if err == nil {
			t.Fatal("invalid network rule accepted")
		}
	}
	if strings.Contains(f.notices("network_latency")[0].Body, "1.1.1.1") {
		t.Fatal("unnecessary target address in placeholder message")
	}
}

func TestAgentProbeDuplicateResultDoesNotAdvanceAlertStreak(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"1.1.1.1"}, nil)}}, now)
	if _, err := s.AcceptReport(ctx, token, agentproto.Report{Version: 1, AgentID: agent, CounterEpoch: "epoch", Sequence: 1, SampledAt: now, UptimeMS: 10000}, now); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 64)
	if _, err := s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now); err != nil {
		t.Fatal(err)
	}
	plan := model.DefaultProbePlan()
	plan.Name = "agent alert"
	plan.PointKind = "agent"
	plan.InstanceID = new("ecs")
	plan.TargetID = "target_cloudflare_dns"
	plan.Family = "ipv4"
	plan.Method = "icmp"
	plan.Count = 1
	plan.Enabled = true
	plan, err := s.SaveProbePlan(ctx, "", plan, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now)
	if err != nil || claimed.Task == nil {
		t.Fatal(claimed, err)
	}
	report := agentproto.ProbeReport{AgentID: agent, TaskID: claimed.Task.ID, Owner: owner, Result: netprobe.Result{StartedAt: now, FinishedAt: now.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: now, ErrorCode: new("probe_timeout")}}}}
	for i := 0; i < 4; i++ {
		ack, err := s.AcceptProbeResult(ctx, token, report, now.Add(time.Second))
		if err != nil || ack.Duplicate != (i > 0) {
			t.Fatal(ack, err)
		}
	}
	var bad, notices int
	if err = s.DB.QueryRow("SELECT bad_count FROM probe_alert_progress WHERE rule_key='network_unreachable' AND plan_id=?", plan.ID).Scan(&bad); err != nil || bad != 1 {
		t.Fatal(bad, err)
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM notification WHERE rule_key='network_loss' AND probe_plan_id=?", plan.ID).Scan(&notices); err != nil || notices != 1 {
		t.Fatal(notices, err)
	}
}
func TestProbeAlertMissingDataAndIdentityChangesNeverFakeRecovery(t *testing.T) {
	f := newProbeAlertFixture(t)
	ctx := context.Background()
	channelFixture(t, f.s, 0, f.now)
	f.run(0, 20, "")
	f.now = f.now.Add(time.Hour)
	if err := f.s.EvaluateAlerts(ctx, f.now); err != nil {
		t.Fatal(err)
	}
	var active, bad int
	if err := f.s.DB.QueryRow("SELECT active FROM alert_state WHERE rule_key='network_loss' AND target_id=?", f.plan.ID).Scan(&active); err != nil || active != 1 {
		t.Fatal(active, err)
	}
	if err := f.s.DB.QueryRow("SELECT bad_count FROM probe_alert_progress WHERE rule_key='network_loss' AND plan_id=?", f.plan.ID).Scan(&bad); err != nil || bad != 0 {
		t.Fatal(bad, err)
	}
	if len(f.notices("network_loss")) != 1 {
		t.Fatal("missing data recovered")
	}
	target, err := f.s.NetworkTarget(ctx, f.plan.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	target.Host = new("8.8.8.8")
	if _, err = f.s.SaveNetworkTarget(ctx, target.ID, target, f.now); err != nil {
		t.Fatal(err)
	}
	items := f.notices("network_loss")
	if len(items) != 1 || items[0].DeliveryStatus != "not_sent" {
		t.Fatal("old endpoint notice not cancelled", items)
	}
	if err = f.s.DB.QueryRow("SELECT active FROM alert_state WHERE rule_key='network_loss' AND target_id=?", f.plan.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal(active, err)
	}
}

func TestProbeNewEndpointEpisodeUpdatesAccountContext(t *testing.T) {
	f := newProbeAlertFixture(t)
	ctx := context.Background()
	f.run(0, 20, "")
	first := f.notices("network_loss")
	if len(first) != 1 || first[0].AccountID != nil {
		t.Fatal(first)
	}
	setAlertAccount(t, f.s, "ok", 0)
	saveEventScan(t, f.s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"1.1.1.1"}, nil)}}, f.now)
	target, err := f.s.NetworkTarget(ctx, f.plan.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	target.Kind = "instance"
	target.Host = nil
	target.InstanceID = new("ecs")
	if _, err = f.s.SaveNetworkTarget(ctx, target.ID, target, f.now); err != nil {
		t.Fatal(err)
	}
	f.run(0, 20, "")
	items := f.notices("network_loss")
	if len(items) != 2 || items[1].Kind != "alert" || items[1].AccountID == nil || *items[1].AccountID != "a" {
		t.Fatal(items)
	}
	var account string
	if err = f.s.DB.QueryRow("SELECT account_id FROM alert_state WHERE rule_key='network_loss' AND target_id=?", f.plan.ID).Scan(&account); err != nil || account != "a" {
		t.Fatal(account, err)
	}
}

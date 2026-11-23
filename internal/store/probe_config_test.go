package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func createProbePlan(t *testing.T, s *Store, target string, now time.Time) model.ProbePlan {
	t.Helper()
	v := model.DefaultProbePlan()
	v.Name = "test"
	v.PointKind = "server"
	v.TargetID = target
	v.Family = "ipv4"
	v.Method = "tcp"
	v.Count = 1
	v.Enabled = true
	out, err := s.SaveProbePlan(context.Background(), "", v, now)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestProbeConfigurationSchedulingMissedPeriodsAndHistory(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	host := "1.1.1.1"
	port := 443
	target, err := s.SaveNetworkTarget(ctx, "", model.NetworkTarget{Name: "custom", Kind: "custom", Host: &host, TCPPort: &port, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := createProbePlan(t, s, target.ID, now)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ScheduleProbePlans(ctx, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	runs, err := s.ProbeRuns(ctx, plan.ID, now.Add(-time.Second), now.Add(time.Second), 0, 201)
	if err != nil || len(runs) != 1 || runs[0].Status != "queued" || runs[0].Request.Host != host {
		t.Fatal(runs, err)
	}
	claim, err := s.ClaimServerProbe(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	result := netprobe.Result{StartedAt: now, FinishedAt: now, Address: host, ResolvedAddresses: []string{host}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: now, RTTMS: new(float64(2))}}}
	if err = s.FinishServerProbe(ctx, claim, result, "", now); err != nil {
		t.Fatal(err)
	}
	// A one-hour outage creates exactly one current run, not twelve past runs.
	later := now.Add(time.Hour)
	if err = s.ScheduleProbePlans(ctx, later); err != nil {
		t.Fatal(err)
	}
	runs, err = s.ProbeRuns(ctx, plan.ID, now.Add(-time.Second), later.Add(time.Second), 0, 201)
	if err != nil || len(runs) != 2 || !runs[0].CreatedAt.Equal(later) || runs[1].Result == nil || *runs[1].Result.Summary.AverageMS != 2 {
		t.Fatal(runs, err)
	}
	view, err := s.ProbePlan(ctx, plan.ID)
	if err != nil || view.LastRun == nil || view.LastRun.ID != runs[0].ID || view.NextRunAt.Unix() != later.Add(5*time.Minute).Unix() {
		t.Fatal(view, err)
	}
	// Changing a target cancels only its queued old-address work.
	target.Host = new("8.8.8.8")
	updated, err := s.SaveNetworkTarget(ctx, target.ID, target, later)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Job(ctx, *runs[0].JobID)
	if err != nil || old.Status != "failed" || old.Error.Code != "probe_configuration_changed" {
		t.Fatal(old, err)
	}
	if _, err = s.SaveNetworkTarget(ctx, target.ID, target, later); !errors.Is(err, ErrNetworkChanged) {
		t.Fatal("stale target revision accepted", err)
	}
	if err = s.DeleteNetworkTarget(ctx, target.ID, updated.Revision, later); !errors.Is(err, ErrTargetInUse) {
		t.Fatal(err)
	}
	if err = s.DeleteProbePlan(ctx, plan.ID, plan.Revision, later); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNetworkTarget(ctx, target.ID, updated.Revision, later); err != nil {
		t.Fatal(err)
	}
	retained, err := s.ProbeRuns(ctx, plan.ID, now.Add(-time.Second), later.Add(time.Second), 0, 1)
	if err != nil || len(retained) != 1 {
		t.Fatal(retained, err)
	}
	before, _ := strconv.ParseInt(retained[0].ID, 10, 64)
	rest, err := s.ProbeRuns(ctx, plan.ID, now.Add(-time.Second), later.Add(time.Second), before, 201)
	if err != nil || len(rest) != 1 || rest[0].Status != "succeeded" {
		t.Fatal(rest, err)
	}
}
func TestManagedProbeTargetTracksCompleteUniqueFreshAddresses(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	port := 443
	instance := "ecs"
	target, err := s.SaveNetworkTarget(ctx, "", model.NetworkTarget{Name: "follow instance", Kind: "instance", InstanceID: &instance, TCPPort: &port, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := createProbePlan(t, s, target.ID, now)
	scan := func(v model.Snapshot) {
		saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{v}, CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}}, now)
	}
	scan(eventSnapshot("running", []string{"1.1.1.1"}, nil))
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	first, _ := s.ProbePlan(ctx, plan.ID)
	if first.LastRun == nil || first.LastRun.JobID == nil || first.LastRun.Request.Host != "1.1.1.1" {
		t.Fatal(first)
	}
	now = now.Add(time.Second)
	scan(eventSnapshot("running", []string{"8.8.8.8"}, nil))
	if _, err = s.ClaimServerProbe(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale address dispatched", err)
	}
	old, _ := s.Job(ctx, *first.LastRun.JobID)
	if old.Error == nil || old.Error.Code != "probe_target_changed" {
		t.Fatal(old)
	}
	for _, tc := range []struct {
		addresses []string
		complete  bool
		reason    string
	}{
		{[]string{"8.8.8.8"}, true, ""},
		{[]string{"8.8.8.8"}, false, "target_discovery_incomplete"},
		{[]string{}, true, "target_no_public_address"},
		{[]string{"1.1.1.1", "8.8.8.8"}, true, "target_ambiguous_address"},
	} {
		now = now.Add(5 * time.Minute)
		v := eventSnapshot("running", tc.addresses, nil)
		v.IPv4Complete = tc.complete
		scan(v)
		if err = s.ScheduleProbePlans(ctx, now); err != nil {
			t.Fatal(err)
		}
		view, _ := s.ProbePlan(ctx, plan.ID)
		if tc.reason != "" {
			if view.LastRun.Status != "skipped" || view.LastRun.SkipReason == nil || *view.LastRun.SkipReason != tc.reason || view.LastRun.Result != nil {
				t.Fatal(view.LastRun)
			}
		} else {
			if view.LastRun.Request == nil || view.LastRun.Request.Host != "8.8.8.8" {
				t.Fatal(view.LastRun)
			}
			// Expire the unexecuted test job before the next observation opportunity.
			if err = s.ExpireNetworkProbes(ctx, now.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	now = now.Add(5 * time.Minute)
	scan(eventSnapshot("running", []string{"1.1.1.1"}, nil))
	now = now.Add(11 * time.Minute)
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	view, _ := s.ProbePlan(ctx, plan.ID)
	if view.LastRun.SkipReason == nil || *view.LastRun.SkipReason != "target_discovery_stale" {
		t.Fatal(view.LastRun)
	}
}
func TestProbePointAvailabilityAndCredentialRotation(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"1.1.1.1"}, []string{"2606:4700:4700::1111"})}}, now)
	plan := model.DefaultProbePlan()
	plan.Name = "agent"
	plan.PointKind = "agent"
	plan.InstanceID = new("ecs")
	plan.TargetID = "target_cloudflare_dns"
	plan.Family = "ipv4"
	plan.Method = "icmp"
	plan.Enabled = true
	plan, err := s.SaveProbePlan(ctx, "", plan, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	view, _ := s.ProbePlan(ctx, plan.ID)
	if view.LastRun.SkipReason == nil || *view.LastRun.SkipReason != "agent_offline" {
		t.Fatal(view.LastRun)
	}
	now = now.Add(5 * time.Minute)
	_, err = s.AcceptReport(ctx, token, agentproto.Report{Version: 1, AgentID: agent, CounterEpoch: "epoch", Sequence: 1, SampledAt: now, UptimeMS: 10000}, now)
	if err != nil {
		t.Fatal(err)
	}
	points, err := s.ProbePoints(ctx, "", 201, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range points {
		if point.ID == "ecs" && (point.Available || point.UnavailableReason == nil || *point.UnavailableReason != "agent_probe_unavailable") {
			t.Fatal("traffic-only agent advertised probing", point)
		}
	}
	if _, err = s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: strings.Repeat("c", 64)}, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	view, _ = s.ProbePlan(ctx, plan.ID)
	if view.LastRun.JobID == nil || view.LastRun.Status != "queued" {
		t.Fatal(view.LastRun)
	}
	if _, err = s.DB.Exec("UPDATE account_settings SET credential_version=credential_version+1"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	view, _ = s.ProbePlan(ctx, plan.ID)
	if view.LastRun.SkipReason == nil || *view.LastRun.SkipReason != "point_discovery_unverified" {
		t.Fatal(view.LastRun)
	}
}

func TestProbePointsHideReleasedInstancesUnlessPlanned(t *testing.T) {
	s, _, _, now := historyAgent(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"1.1.1.1"}, nil)}}, now)

	// While present=1, "ecs" is returned alongside "server".
	points, err := s.ProbePoints(ctx, "", 201, now)
	if err != nil {
		t.Fatal(err)
	}
	hasECS := false
	for _, pt := range points {
		if pt.ID == "ecs" {
			hasECS = true
		}
	}
	if !hasECS {
		t.Fatalf("expected present ecs in probe points, got %+v", points)
	}

	// Simulate cloud release (present=0) with no active probe_plan -> hidden from ProbePoints.
	if _, err = s.DB.Exec("UPDATE resource_snapshot SET present=0 WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	points, err = s.ProbePoints(ctx, "", 201, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range points {
		if pt.ID == "ecs" {
			t.Fatalf("released instance without active probe plan should be hidden, got %+v", pt)
		}
	}

	// If an active probe_plan references it, ProbePoints keeps it with instance_unavailable.
	plan := model.DefaultProbePlan()
	plan.Name = "still-bound"
	plan.PointKind = "agent"
	plan.InstanceID = new("ecs")
	plan.TargetID = "target_cloudflare_dns"
	plan.Family = "ipv4"
	plan.Method = "icmp"
	plan.Enabled = true
	if _, err = s.SaveProbePlan(ctx, "", plan, now); err != nil {
		t.Fatal(err)
	}
	points, err = s.ProbePoints(ctx, "", 201, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pt := range points {
		if pt.ID == "ecs" {
			found = true
			if pt.Available || pt.UnavailableReason == nil || *pt.UnavailableReason != "instance_unavailable" {
				t.Fatalf("expected instance_unavailable for planned released instance, got %+v", pt)
			}
		}
	}
	if !found {
		t.Fatalf("expected planned released instance to remain visible, got %+v", points)
	}
}

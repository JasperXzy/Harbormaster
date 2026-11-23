package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func traceSubmission() model.TraceSubmission {
	in := model.DefaultTraceSubmission()
	in.RequestKey = strings.Repeat("t", 32)
	in.PointKind = "server"
	in.Host = new("trace.invalid")
	in.Family = "ipv4"
	in.Method = "icmp"
	in.Count = 1
	in.MaxHops = 3
	return in
}
func echoTraceResult(at time.Time) netprobe.TraceResult {
	return netprobe.TraceResult{StartedAt: at, FinishedAt: at.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: at, Address: new("1.1.1.1"), RTTMS: new(float64(12)), Reply: new("echo_reply"), ICMPType: new(0), ICMPCode: new(0)}}}}}
}
func TestTraceHistoryAtomicSubmissionDedupAndStoredProjection(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	in := traceSubmission()
	var wg sync.WaitGroup
	answers := make(chan model.TraceAccepted, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.SubmitTrace(ctx, in, now)
			if err != nil {
				t.Error(err)
			}
			answers <- v
		}()
	}
	wg.Wait()
	close(answers)
	var accepted model.TraceAccepted
	for a := range answers {
		if accepted.JobID != "" && a != accepted {
			t.Fatal("duplicate jobs", a, accepted)
		}
		accepted = a
	}
	var jobs int
	if err := s.DB.QueryRow("SELECT count(*) FROM network_trace_run").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal(jobs, err)
	}
	detail, err := s.Trace(ctx, accepted.TraceID)
	if err != nil || detail.Status != "queued" || detail.Request.Host != "trace.invalid" || detail.Result != nil || detail.Outcome != nil {
		t.Fatal(detail, err)
	}
	changed := in
	changed.Count = 2
	if _, err = s.SubmitTrace(ctx, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal("different input reused key", err)
	}
	claim, err := s.ClaimServerTrace(ctx, now)
	if err != nil || claim.Job.ID != accepted.JobID {
		t.Fatal(claim, err)
	}
	if err = s.FinishServerTrace(ctx, claim, echoTraceResult(now), "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	detail, err = s.Trace(ctx, accepted.TraceID)
	if err != nil || detail.Outcome == nil || !detail.Outcome.Reached || detail.Outcome.StopReason != "reached" || detail.Outcome.SampleCount != 1 || detail.Result == nil || detail.Status != "succeeded" {
		t.Fatal(detail, err)
	}
	raw, _ := json.Marshal(detail)
	for _, private := range []string{claim.Job.Owner, "token_hash", "request_key", "claimed_by"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private task state exposed", private)
		}
	}
	again, err := s.SubmitTrace(ctx, in, now.Add(time.Hour))
	if err != nil || again != accepted {
		t.Fatal("completed idempotency lost", again, err)
	}
	f := model.TraceFilter{From: now, To: now.Add(2 * time.Second), Limit: 2}
	// A list reads the persisted small projection, not all raw packets per row.
	if _, err = s.DB.Exec("UPDATE network_probe_job SET result_json='invalid raw JSON' WHERE job_id=?", accepted.JobID); err != nil {
		t.Fatal(err)
	}
	items, err := s.Traces(ctx, f)
	if err != nil || len(items) != 1 || !items[0].Outcome.Reached {
		t.Fatal(items, err)
	}
}

func TestTraceHistorySavedTargetSnapshotsAndQueueInvalidation(t *testing.T) {
	for _, mode := range []string{"edit", "delete", "running_edit", "managed_ip", "managed_incomplete", "managed_stale"} {
		t.Run(mode, func(t *testing.T) {
			s := agentStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			target := model.NetworkTarget{Name: "original", Kind: "custom", Host: new("1.1.1.1"), Enabled: true, TCPPort: new(443)}
			if strings.HasPrefix(mode, "managed") {
				setAlertAccount(t, s, "ok", 0)
				saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"1.1.1.1"}, nil)}}, now)
				target.Kind = "instance"
				target.Host = nil
				target.InstanceID = new("ecs")
			}
			target, err := s.SaveNetworkTarget(ctx, "", target, now)
			if err != nil {
				t.Fatal(err)
			}
			in := traceSubmission()
			in.Host = nil
			in.TargetID = &target.ID
			in.TargetRevision = &target.Revision
			accepted, err := s.SubmitTrace(ctx, in, now)
			if err != nil {
				t.Fatal(err)
			}
			var claim ServerTraceClaim
			if mode == "running_edit" {
				claim, err = s.ClaimServerTrace(ctx, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "edit", "running_edit":
				target.Host = new("8.8.8.8")
				target.Name = "changed"
				_, err = s.SaveNetworkTarget(ctx, target.ID, target, now.Add(time.Second))
			case "delete":
				err = s.DeleteNetworkTarget(ctx, target.ID, target.Revision, now.Add(time.Second))
			case "managed_ip":
				_, err = s.DB.Exec(`UPDATE instance_probe_address SET ipv4_json='["8.8.8.8"]'`)
			case "managed_incomplete":
				_, err = s.DB.Exec("UPDATE instance_probe_address SET ipv4_json=NULL")
			case "managed_stale":
				_, err = s.DB.Exec("UPDATE instance_probe_address SET observed_at=?", timestamp(now.Add(-11*time.Minute)))
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "running_edit" {
				err = s.FinishServerTrace(ctx, claim, echoTraceResult(now), "", now.Add(time.Second))
			} else {
				_, err = s.ClaimServerTrace(ctx, now.Add(time.Second))
				if !errors.Is(err, ErrNotFound) {
					t.Fatal("stale queued request claimed", err)
				}
				err = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			detail, err := s.Trace(ctx, accepted.TraceID)
			if err != nil || detail.Target == nil || detail.Target.Name != "original" || detail.Request.Host != "1.1.1.1" {
				t.Fatal("history rewritten", detail, err)
			}
			if mode == "running_edit" {
				if detail.Status != "succeeded" || !detail.Outcome.Reached {
					t.Fatal(detail)
				}
			} else if detail.Status != "failed" || detail.Result != nil || detail.Outcome != nil {
				t.Fatal("invented result for invalidated request", detail)
			}
			again, err := s.SubmitTrace(ctx, in, now.Add(time.Minute))
			if err != nil || again != accepted {
				t.Fatal("retry changed frozen request", again, err)
			}
		})
	}
}

func TestTraceSourceNeedsSeparateAuthenticatedCapabilityAndCachesAgentResult(t *testing.T) {
	s, agent, token, _ := historyAgent(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"1.1.1.1"}, nil)}}, now)
	if _, err := s.AcceptReport(ctx, token, agentproto.Report{Version: 1, AgentID: agent, CounterEpoch: "trace", Sequence: 1, SampledAt: now, UptimeMS: 10000}, now); err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 64)
	claim := agentproto.ProbeClaim{AgentID: agent, Owner: owner}
	if _, err := s.ClaimAgentProbe(ctx, token, claim, now); err != nil {
		t.Fatal(err)
	}
	point, err := probePoint(ctx, s.DB, "ecs", now)
	if err != nil || !point.Available || point.Trace.Available || *point.Trace.UnavailableReason != "agent_trace_unavailable" {
		t.Fatal(point, err)
	}
	in := traceSubmission()
	in.PointKind = "agent"
	in.InstanceID = new("ecs")
	in.Method = "tcp"
	in.Port = 443
	if _, err = s.SubmitTrace(ctx, in, now); err == nil {
		t.Fatal("old agent accepted for trace")
	}
	if _, err = s.ClaimAgentTrace(ctx, token, claim, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("DELETE FROM agent_probe_contact"); err != nil {
		t.Fatal(err)
	}
	point, err = probePoint(ctx, s.DB, "ecs", now)
	if err != nil || point.Available || !point.Trace.Available || point.Trace.LastPollAt == nil {
		t.Fatal("trace poll faked ordinary probe support", point, err)
	}
	accepted, err := s.SubmitTrace(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.ClaimAgentTrace(ctx, token, claim, now)
	if err != nil || work.Task == nil || work.Task.ID != accepted.JobID {
		t.Fatal(work, err)
	}
	if _, err = s.AcceptTraceResult(ctx, token, traceReport(work.Task.ID, owner, agent, now), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Trace(ctx, accepted.TraceID)
	if err != nil || detail.Outcome == nil || !detail.Outcome.Reached || detail.Result == nil || detail.PointID != "ecs" {
		t.Fatal(detail, err)
	}
	enrichment, err := s.ClaimTraceEnrichment(ctx, now.Add(2*time.Second))
	if err != nil || enrichment.TraceID != accepted.TraceID {
		t.Fatal(enrichment, err)
	}
	if err = s.FinishTraceEnrichment(ctx, enrichment, &model.TraceEnrichment{Status: "complete", EnrichedAt: now.Add(2 * time.Second)}, "", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	ack, err := s.AcceptTraceResult(ctx, token, traceReport(work.Task.ID, owner, agent, now), now.Add(3*time.Second))
	if err != nil || !ack.Duplicate {
		t.Fatal("enrichment changed canonical duplicate ACK", ack, err)
	}
	var enrichments int
	if err = s.DB.QueryRow("SELECT count(*) FROM trace_enrichment").Scan(&enrichments); err != nil || enrichments != 1 {
		t.Fatal("duplicate queued extra enrichment", enrichments, err)
	}
	if _, err = s.DB.Exec("UPDATE agent_trace_contact SET token_hash=zeroblob(32)"); err != nil {
		t.Fatal(err)
	}
	point, err = probePoint(ctx, s.DB, "ecs", now)
	if err != nil || point.Trace.Available || point.Trace.LastPollAt != nil {
		t.Fatal("old token capability retained", point, err)
	}
}

func TestTraceHistoryRangesFiltersAndNoResultAfterExpiry(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first := traceSubmission()
	a, err := s.SubmitTrace(ctx, first, now)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.RequestKey = strings.Repeat("b", 32)
	b, err := s.SubmitTrace(ctx, second, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	f := model.TraceFilter{From: now, To: now.Add(time.Second), Limit: 1}
	rows, err := s.Traces(ctx, f)
	if err != nil || len(rows) != 1 || rows[0].ID != a.TraceID {
		t.Fatal("range is not inclusive/exclusive", rows, err)
	}
	f.To = now.Add(2 * time.Second)
	rows, err = s.Traces(ctx, f)
	if err != nil || rows[0].ID != b.TraceID {
		t.Fatal(rows, err)
	}
	f.Before = 2
	rows, err = s.Traces(ctx, f)
	if err != nil || len(rows) != 1 || rows[0].ID != a.TraceID {
		t.Fatal(rows, err)
	}
	f.PointID = "unknown"
	rows, err = s.Traces(ctx, f)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if err = s.ExpireNetworkProbes(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Trace(ctx, a.TraceID)
	if err != nil || detail.Status != "failed" || detail.Error.Code != "probe_expired" || detail.Result != nil || detail.Outcome != nil {
		t.Fatal(detail, err)
	}
	if _, err = s.Trace(ctx, "missing"); err != ErrNotFound {
		t.Fatal(err)
	}
	var violations int
	if err = s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Fatal(violations, err)
	}
}

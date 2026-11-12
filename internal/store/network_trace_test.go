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
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func traceRequest() netprobe.TraceRequest {
	return netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, MaxHops: 30, Count: 1, TimeoutMS: 1000}
}
func traceReport(id, owner, agent string, now time.Time) agentproto.TraceReport {
	return agentproto.TraceReport{AgentID: agent, TaskID: id, Owner: owner, Result: netprobe.TraceResult{StartedAt: now, FinishedAt: now.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Reached: false, StopReason: "untrusted", Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: now, Address: new("1.1.1.1"), RTTMS: new(float64(12)), Reply: new("tcp_reset")}}}}}}
}

func TestTraceQueueSharesLeasesButNotPayloadsWithProbes(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	owner := strings.Repeat("a", 64)
	q, key := traceRequest(), strings.Repeat("t", 32)
	j, err := s.EnqueueAgentTrace(ctx, "ecs", key, q, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnqueueAgentTrace(ctx, "ecs", key, q, now)
	if err != nil || again.ID != j.ID {
		t.Fatal(again, err)
	}
	q.Count = 2
	if _, err = s.EnqueueAgentTrace(ctx, "ecs", key, q, now); err != ErrConflict {
		t.Fatal("changed request", err)
	}
	if _, err = s.EnqueueAgentProbe(ctx, "ecs", key, probeRequest(), now); err != ErrConflict {
		t.Fatal("key crossed action", err)
	}
	if _, err = s.EnqueueServerProbe(ctx, key, probeRequest(), now); err != ErrConflict {
		t.Fatal("key crossed point", err)
	}
	in := agentproto.ProbeClaim{AgentID: agent, Owner: owner}
	old, err := s.ClaimAgentProbe(ctx, token, in, now)
	if err != nil || old.Task != nil {
		t.Fatal("old agent consumed trace", old, err)
	}
	p, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("p", 32), probeRequest(), now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = s.ClaimAgentProbe(ctx, token, in, now)
	if err != nil || old.Task == nil || old.Task.ID != p.ID {
		t.Fatal(old, err)
	}
	busy, err := s.ClaimAgentTrace(ctx, token, in, now)
	if err != nil || busy.Task != nil {
		t.Fatal("trace overlapped probe", busy, err)
	}
	if _, err = s.AcceptProbeResult(ctx, token, probeReport(p.ID, owner, agent, now), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimAgentTrace(ctx, token, in, now.Add(2*time.Second))
	if err != nil || claim.Task == nil || claim.Task.ID != j.ID {
		t.Fatal(claim, err)
	}
	repeat, err := s.ClaimAgentTrace(ctx, token, in, now.Add(3*time.Second))
	if err != nil || repeat.Task == nil || !repeat.Task.ExpiresAt.Equal(claim.Task.ExpiresAt) {
		t.Fatal(repeat, err)
	}
	old, err = s.ClaimAgentProbe(ctx, token, in, now.Add(3*time.Second))
	if err != nil || old.Task != nil {
		t.Fatal("trace payload returned to probe claimant", old, err)
	}
	live, err := s.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RenewJob(ctx, live, now); err != ErrLeaseLost {
		t.Fatal("generic renew", err)
	}
	if err = s.FinishJob(ctx, live, nil, "", now); err != ErrLeaseLost {
		t.Fatal("generic finish", err)
	}
	if _, err = s.AcceptProbeResult(ctx, token, probeReport(j.ID, owner, agent, now), now.Add(4*time.Second)); err != ErrNotFound {
		t.Fatal("wrong result protocol", err)
	}
	report := traceReport(j.ID, owner, agent, now.Add(2*time.Second))
	ack, err := s.AcceptTraceResult(ctx, token, report, now.Add(4*time.Second))
	if err != nil || ack.Duplicate {
		t.Fatal(ack, err)
	}
	report.Result.StopReason = "another ignored caller assertion"
	report.Result.Reached = true
	ack, err = s.AcceptTraceResult(ctx, token, report, now.Add(5*time.Second))
	if err != nil || !ack.Duplicate {
		t.Fatal(ack, err)
	}
	var raw string
	if err = s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", j.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored agentproto.TraceReport
	if json.Unmarshal([]byte(raw), &stored) != nil || !stored.Result.Reached || stored.Result.StopReason != "reached" {
		t.Fatal("untrusted conclusion persisted", raw)
	}
	report.Result.Hops[0].Samples[0].RTTMS = new(float64(13))
	if _, err = s.AcceptTraceResult(ctx, token, report, now.Add(6*time.Second)); err != ErrConflict {
		t.Fatal("changed retry accepted", err)
	}
	ended, err := s.Job(ctx, j.ID)
	if err != nil || ended.Status != "succeeded" || ended.Attempts != 1 || ended.Owner != "" {
		t.Fatal(ended, err)
	}
	var notifications int
	if err = s.DB.QueryRow("SELECT count(*) FROM notification WHERE job_id=?", j.ID).Scan(&notifications); err != nil || notifications != 0 {
		t.Fatal("trace entered probe alerts", notifications, err)
	}
}

func TestTraceQueueInvalidationAndIncompleteReports(t *testing.T) {
	for _, mode := range []string{"queued_expired", "running_expired", "revoke", "credential", "disabled", "missing", "token", "restore", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			s, agent, token, now := historyAgent(t)
			ctx := context.Background()
			owner := strings.Repeat("a", 64)
			j, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("t", 32), traceRequest(), now)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "queued_expired" {
				if _, err = s.ClaimAgentTrace(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "queued_expired", "running_expired":
				now = now.Add(5 * time.Minute)
			case "revoke":
				err = s.RevokeAgent(ctx, "ecs", now)
			case "credential":
				_, err = s.DB.Exec("UPDATE account_settings SET credential_version=credential_version+1")
			case "disabled":
				_, err = s.DB.Exec("UPDATE account_settings SET enabled=0")
			case "missing":
				_, err = s.DB.Exec("UPDATE resource_snapshot SET present=0")
			case "token":
				_, err = s.DB.Exec("UPDATE agent SET token_hash=zeroblob(32)")
			case "restore":
				_, err = s.PrepareRestoredCopy(ctx, now)
			case "interrupted":
				report := agentproto.TraceReport{AgentID: agent, TaskID: j.ID, Owner: owner, ErrorCode: new("probe_interrupted"), Result: netprobe.TraceResult{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Hops: []netprobe.TraceHop{}}}
				_, err = s.AcceptTraceResult(ctx, token, report, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ExpireNetworkProbes(ctx, now); err != nil {
				t.Fatal(err)
			}
			ended, err := s.Job(ctx, j.ID)
			if err != nil || ended.Status != "failed" || ended.Owner != "" {
				t.Fatal(ended, err)
			}
			if _, err = s.AcceptTraceResult(ctx, token, traceReport(j.ID, owner, agent, now), now.Add(time.Second)); err == nil {
				t.Fatal("invalidated result accepted")
			}
			next, _ := s.ClaimAgentTrace(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now)
			if next.Task != nil {
				t.Fatal("replayed trace")
			}
		})
	}
}

func TestTraceAndProbeClaimsSerializeAndBadTraceDoesNotFinish(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	owner := strings.Repeat("a", 64)
	_, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("t", 32), traceRequest(), now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("p", 32), probeRequest(), now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, action := range []string{"probe", "trace"} {
		wg.Add(1)
		go func(action string) {
			defer wg.Done()
			_, err := s.claimAgentNetwork(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, action, now)
			if err != nil {
				t.Error(err)
			}
		}(action)
	}
	wg.Wait()
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM job WHERE status='running'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// An isolated source tests structural rejection independently of the race's winner.
	s2, agent2, token2, now2 := historyAgent(t)
	j, err := s2.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("k", 32), traceRequest(), now2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s2.ClaimAgentTrace(ctx, "bad-token-00000000", agentproto.ProbeClaim{AgentID: agent2, Owner: owner}, now2); err != ErrUnauthorized {
		t.Fatal(err)
	}
	if _, err = s2.ClaimAgentTrace(ctx, token2, agentproto.ProbeClaim{AgentID: agent2, Owner: owner}, now2); err != nil {
		t.Fatal(err)
	}
	bad := traceReport(j.ID, owner, agent2, now2)
	bad.Result.Hops[0].Samples[0].Reply = new("shell output")
	if _, err = s2.AcceptTraceResult(ctx, token2, bad, now2.Add(time.Second)); !errors.Is(err, netprobe.ErrInvalid) {
		t.Fatal(err)
	}
	active, _ := s2.Job(ctx, j.ID)
	if active.Status != "running" {
		t.Fatal("bad report finished job", active)
	}
}

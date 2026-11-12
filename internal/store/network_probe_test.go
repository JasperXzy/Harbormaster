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

func probeRequest() netprobe.Request {
	return netprobe.Request{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, IntervalMS: 100}
}
func probeReport(id, owner, agent string, now time.Time) agentproto.ProbeReport {
	ms := 12.0
	return agentproto.ProbeReport{AgentID: agent, TaskID: id, Owner: owner, Result: netprobe.Result{StartedAt: now, FinishedAt: now.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: now, RTTMS: &ms}}, Summary: &netprobe.Summary{LossPercent: 999}}}
}
func TestProbeQueueAuthIdempotencyAndCanonicalResults(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	q := probeRequest()
	key := strings.Repeat("k", 32)
	owner := strings.Repeat("a", 64)
	j, err := s.EnqueueAgentProbe(ctx, "ecs", key, q, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnqueueAgentProbe(ctx, "ecs", key, q, now)
	if err != nil || again.ID != j.ID {
		t.Fatal(again, err)
	}
	changed := q
	changed.Port = 80
	if _, err = s.EnqueueAgentProbe(ctx, "ecs", key, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.ClaimCollectionJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("collection claimed probe", err)
	}
	in := agentproto.ProbeClaim{AgentID: agent, Owner: owner}
	if _, err = s.ClaimAgentProbe(ctx, "wrong-token-000000000", in, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAgentProbe(ctx, token, in, now)
	if err != nil || claimed.Task == nil || claimed.Task.ID != j.ID {
		t.Fatal(claimed, err)
	}
	repeated, err := s.ClaimAgentProbe(ctx, token, in, now.Add(time.Second))
	if err != nil || repeated.Task == nil || !repeated.Task.ExpiresAt.Equal(claimed.Task.ExpiresAt) {
		t.Fatal(repeated, err)
	}
	other, err := s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: strings.Repeat("b", 64)}, now)
	if err != nil || other.Task != nil {
		t.Fatal("two owners claimed", other, err)
	}
	live, _ := s.Job(ctx, j.ID)
	if err = s.RenewJob(ctx, live, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("generic renew bypass", err)
	}
	if err = s.FinishJob(ctx, live, nil, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("generic finish bypass", err)
	}
	report := probeReport(j.ID, owner, agent, now)
	ack, err := s.AcceptProbeResult(ctx, token, report, now.Add(time.Second))
	if err != nil || ack.Duplicate {
		t.Fatal(ack, err)
	}
	ack, err = s.AcceptProbeResult(ctx, token, report, now.Add(2*time.Second))
	if err != nil || !ack.Duplicate {
		t.Fatal(ack, err)
	}
	var raw string
	if err = s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", j.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored agentproto.ProbeReport
	if json.Unmarshal([]byte(raw), &stored) != nil || stored.Result.Summary == nil || stored.Result.Summary.LossPercent != 0 || *stored.Result.Summary.AverageMS != 12 {
		t.Fatal("untrusted summary persisted", raw)
	}
	report.Result.Samples[0].RTTMS = new(float64(13))
	if _, err = s.AcceptProbeResult(ctx, token, report, now.Add(3*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
	ended, _ := s.Job(ctx, j.ID)
	if ended.Status != "succeeded" || ended.Attempts != 1 || ended.Owner != "" {
		t.Fatal(ended)
	}
}
func TestProbeQueueExpiryRevocationCredentialChangesAndRestore(t *testing.T) {
	for _, mode := range []string{"queued_expired", "running_expired", "revoke", "credential", "disabled", "missing", "token", "restore"} {
		t.Run(mode, func(t *testing.T) {
			s, agent, token, now := historyAgent(t)
			ctx := context.Background()
			owner := strings.Repeat("a", 64)
			j, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("k", 32), probeRequest(), now)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "queued_expired" {
				if _, err = s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now); err != nil {
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
			if _, err = s.AcceptProbeResult(ctx, token, probeReport(j.ID, owner, agent, now), now.Add(time.Second)); err == nil {
				t.Fatal("expired/binding-invalid result accepted")
			}
			next, _ := s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now)
			if next.Task != nil {
				t.Fatal("old job replayed")
			}
		})
	}
}
func TestProbeQueueSerializesCompetingAgentsAndRejectsBadReports(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	j, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("k", 32), probeRequest(), now)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	owners := []string{}
	for _, char := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(ch string) {
			defer wg.Done()
			owner := strings.Repeat(ch, 64)
			out, err := s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, now)
			if err != nil {
				t.Error(err)
			}
			if out.Task != nil {
				mu.Lock()
				owners = append(owners, owner)
				mu.Unlock()
			}
		}(char)
	}
	wg.Wait()
	if len(owners) != 1 {
		t.Fatal(owners)
	}
	for _, mutate := range []func(*agentproto.ProbeReport){func(r *agentproto.ProbeReport) { r.AgentID = "other" }, func(r *agentproto.ProbeReport) { r.Owner = strings.Repeat("f", 64) }, func(r *agentproto.ProbeReport) { r.Result.Address = "100.100.100.200" }, func(r *agentproto.ProbeReport) { r.Result.Samples[0].RTTMS = new(float64(-1)) }, func(r *agentproto.ProbeReport) { r.ErrorCode = new("raw sensitive failure") }} {
		r := probeReport(j.ID, owners[0], agent, now)
		mutate(&r)
		if _, err = s.AcceptProbeResult(ctx, token, r, now.Add(time.Second)); err == nil {
			t.Fatal("bad report accepted")
		}
	}
	current, _ := s.Job(ctx, j.ID)
	if current.Status != "running" {
		t.Fatal("invalid report changed job")
	}
}

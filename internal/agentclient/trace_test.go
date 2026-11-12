package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentmeter"
	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func clientTraceRequest() netprobe.TraceRequest {
	return netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, MaxHops: 30, Count: 1, TimeoutMS: 1000}
}
func successfulTrace() netprobe.TraceResult {
	now := time.Now().UTC()
	return netprobe.TraceResult{StartedAt: now, FinishedAt: now.Add(time.Millisecond), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: now, Address: new("1.1.1.1"), RTTMS: new(float64(1)), Reply: new("tcp_reset")}}}}}
}
func TestTraceLostClaimAndResultResponsesSurviveRestartWithoutReexecution(t *testing.T) {
	c, s, dir, box := setupProbeClient(t)
	ctx := context.Background()
	job, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("k", 32), clientTraceRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	c.traceRun = func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
		runs++
		if c.Journal.Meta().Trace.Task == nil {
			t.Fatal("no durable start marker")
		}
		return successfulTrace(), nil
	}
	transport := c.HTTP.Transport
	claimLost, resultLost := false, false
	c.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		res, err := transport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if r.URL.Path == agentproto.TraceClaimPath && !claimLost {
			claimLost = true
			res.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		if r.URL.Path == agentproto.TraceResultPath && !resultLost {
			resultLost = true
			res.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return res, nil
	})
	if _, err = c.TraceOnce(ctx); err != ErrTransport || runs != 0 {
		t.Fatal(runs, err)
	}
	owner := c.Journal.Meta().Trace.Owner
	if owner == "" || c.Journal.Meta().Trace.Task != nil {
		t.Fatal("claim retry state")
	}
	c.Journal.Close()
	c.Journal, err = agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.TraceOnce(ctx); err != ErrTransport || runs != 1 {
		t.Fatal(runs, err)
	}
	if c.Journal.Meta().Trace.Report == nil || c.Journal.Meta().Trace.Owner != owner {
		t.Fatal("result not durable")
	}
	ended, err := s.Job(ctx, job.ID)
	if err != nil || ended.Status != "succeeded" {
		t.Fatal(ended, err)
	}
	c.Journal.Close()
	c.Journal, err = agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := c.TraceOnce(ctx); err != nil || !worked || runs != 1 || c.Journal.Meta().Trace != nil {
		t.Fatal(worked, runs, err)
	}
	var rows int
	if err = s.DB.QueryRow("SELECT count(*) FROM network_probe_job WHERE result_json IS NOT NULL").Scan(&rows); err != nil || rows != 1 {
		t.Fatal(rows, err)
	}
}
func TestRecoveredStartedTraceReportsInterruptedAndNeverRuns(t *testing.T) {
	c, s, dir, box := setupProbeClient(t)
	ctx := context.Background()
	owner := strings.Repeat("a", 64)
	job, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("k", 32), clientTraceRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Journal.BeginTrace(owner); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimAgentTrace(ctx, c.Journal.Meta().Identity.Token, agentproto.ProbeClaim{AgentID: c.Journal.Meta().Identity.AgentID, Owner: owner}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Journal.StartTrace(owner, *claim.Task); err != nil {
		t.Fatal(err)
	}
	c.Journal.Close()
	c.Journal, err = agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	c.traceRun = func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
		t.Fatal("replayed started probe")
		return netprobe.TraceResult{}, nil
	}
	if worked, err := c.TraceOnce(ctx); err != nil || !worked || c.Journal.Meta().Trace != nil {
		t.Fatal(worked, err)
	}
	ended, err := s.Job(ctx, job.ID)
	if err != nil || ended.Status != "failed" || ended.Error == nil || ended.Error.Code != "probe_interrupted" {
		t.Fatal(ended, err)
	}
}
func TestBlockedTraceDoesNotBlockTrafficAndCancellationPreservesResult(t *testing.T) {
	c, s, _, _ := setupProbeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("k", 32), clientTraceRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	var runs atomic.Int32
	c.traceRun = func(ctx context.Context, _ netprobe.TraceRequest) (netprobe.TraceResult, error) {
		runs.Add(1)
		close(started)
		now := time.Now().UTC()
		<-ctx.Done()
		return netprobe.TraceResult{StartedAt: now, FinishedAt: time.Now().UTC(), Hops: []netprobe.TraceHop{}, ResolvedAddresses: []string{}}, netprobe.ErrCancelled
	}
	go func() { _, err := c.TraceOnce(ctx); result <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	if err = c.Sample(agentmeter.Sample{Generation: "kernel", UptimeMS: 10000, IPv4RX: 10}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if sent, err := c.Flush(ctx); err != nil || !sent {
		t.Fatal("traffic blocked by probe", sent, err)
	}
	var samples int
	if err = s.DB.QueryRow("SELECT count(*) FROM traffic_sample").Scan(&samples); err != nil || samples != 1 {
		t.Fatal(samples, err)
	}
	cancel()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe ignored cancellation")
	}
	if c.Journal.Meta().Trace.Report == nil {
		t.Fatal("cancelled result lost")
	}
	if _, err = c.TraceOnce(context.Background()); err != nil || runs.Load() != 1 {
		t.Fatal(runs.Load(), err)
	}
	ended, err := s.Job(context.Background(), job.ID)
	if err != nil || ended.Error == nil || ended.Error.Code != "probe_cancelled" {
		t.Fatal(ended, err)
	}
}
func TestTraceUntrustedTasksAndUnavailableProtocolNeverExecute(t *testing.T) {
	for _, mode := range []string{"old_server", "private_target", "wrong_owner", "unknown_command", "wrong_id", "result_conflict", "result_expired"} {
		t.Run(mode, func(t *testing.T) {
			c := testClient(t)
			if err := c.Journal.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "test-token-never-real"}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			c.traceRun = func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
				runs++
				return successfulTrace(), nil
			}
			c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == agentproto.TraceResultPath {
					if mode == "wrong_id" {
						return response(200, agentproto.ProbeAck{TaskID: "different"}), nil
					}
					code := "probe_result_conflict"
					if mode == "result_expired" {
						code = "probe_expired"
					}
					return response(409, map[string]any{"error": map[string]string{"code": code}}), nil
				}
				if mode == "old_server" {
					return response(404, map[string]string{}), nil
				}
				var in agentproto.ProbeClaim
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Fatal("claim body")
				}
				now := time.Now().UTC()
				task := agentproto.TraceTask{ID: "job_test", Owner: in.Owner, Request: clientTraceRequest(), ExpiresAt: now.Add(5 * time.Minute)}
				if mode == "private_target" {
					task.Request.Host = "100.100.100.200"
				}
				if mode == "wrong_owner" {
					task.Owner = strings.Repeat("f", 64)
				}
				if mode == "unknown_command" {
					return response(200, map[string]any{"task": task, "server_time": now, "command": "arbitrary"}), nil
				}
				return response(200, agentproto.TraceClaimed{Task: &task, ServerTime: now}), nil
			})}
			_, err := c.TraceOnce(context.Background())
			if mode == "old_server" {
				if !errors.Is(err, ErrProbeUnsupported) || runs != 0 {
					t.Fatal(runs, err)
				}
			} else if mode == "result_expired" {
				if err != nil || runs != 1 || c.Journal.Meta().Trace != nil {
					t.Fatal(runs, err)
				}
			} else {
				if err != ErrProtocol {
					t.Fatal(err)
				}
				if mode != "wrong_id" && mode != "result_conflict" && runs != 0 {
					t.Fatal("bad task ran")
				}
				if (mode == "wrong_id" || mode == "result_conflict") && c.Journal.Meta().Trace.Report == nil {
					t.Fatal("unconfirmed result discarded")
				}
			}
		})
	}
}

package agentclient

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestNetworkWorkerAlternatesQueuedActions(t *testing.T) {
	c, s, _, _ := setupProbeClient(t)
	ctx := context.Background()
	for _, key := range []string{"a", "b"} {
		if _, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("p"+key, 16), clientProbeRequest(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("t"+key, 16), clientTraceRequest(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	order := []string{}
	c.probeRun = func(context.Context, netprobe.Request) (netprobe.Result, error) {
		order = append(order, "probe")
		return successfulProbe(), nil
	}
	c.traceRun = func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
		order = append(order, "trace")
		return successfulTrace(), nil
	}
	for i := 0; i < 4; i++ {
		if worked, err := c.NetworkOnce(ctx); err != nil || !worked {
			t.Fatal(i, worked, err)
		}
	}
	if strings.Join(order, ",") != "probe,trace,probe,trace" {
		t.Fatal("action starved", order)
	}
	if c.Journal.Meta().Probe != nil || c.Journal.Meta().Trace != nil {
		t.Fatal("ACK state not cleared")
	}
}

func TestDirectProbeAndTraceCallsShareExecutionLock(t *testing.T) {
	c, s, _, _ := setupProbeClient(t)
	ctx := context.Background()
	if _, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("t", 32), clientTraceRequest(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("p", 32), clientProbeRequest(), time.Now()); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var active atomic.Int32
	c.traceRun = func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
		if active.Add(1) != 1 {
			t.Error("overlapping trace")
		}
		close(started)
		<-release
		active.Add(-1)
		return successfulTrace(), nil
	}
	c.probeRun = func(context.Context, netprobe.Request) (netprobe.Result, error) {
		if active.Add(1) != 1 {
			t.Error("overlapping probe")
		}
		active.Add(-1)
		return successfulProbe(), nil
	}
	traceDone, probeDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := c.TraceOnce(ctx); traceDone <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("trace failed to start")
	}
	go func() { _, err := c.ProbeOnce(ctx); probeDone <- err }()
	select {
	case err := <-probeDone:
		close(release)
		t.Fatal("probe ran during trace", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for _, done := range []chan error{traceDone, probeDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("worker did not finish")
		}
	}
}

func TestTraceLeaseBudgetUsesServerDurationAndIsBounded(t *testing.T) {
	for _, mode := range []string{"cap", "short", "spent", "overlong"} {
		t.Run(mode, func(t *testing.T) {
			c := testClient(t)
			if err := c.Journal.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "test-token-never-real"}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			code := ""
			c.traceRun = func(ctx context.Context, _ netprobe.TraceRequest) (netprobe.TraceResult, error) {
				runs++
				deadline, ok := ctx.Deadline()
				left := time.Until(deadline)
				if !ok || left > 125*time.Second {
					t.Fatal("unbounded execution", left)
				}
				if mode == "short" && (left > 2*time.Second || left < time.Second) {
					t.Fatal("clock skew extended lease", left)
				}
				if mode == "cap" && left < 124*time.Second {
					t.Fatal("wrong hard cap", left)
				}
				return successfulTrace(), nil
			}
			c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == agentproto.TraceResultPath {
					var report agentproto.TraceReport
					if json.NewDecoder(r.Body).Decode(&report) != nil {
						t.Fatal("report decode")
					}
					if report.ErrorCode != nil {
						code = *report.ErrorCode
					}
					return response(200, agentproto.ProbeAck{TaskID: report.TaskID}), nil
				}
				var claim agentproto.ProbeClaim
				if json.NewDecoder(r.Body).Decode(&claim) != nil {
					t.Fatal("claim decode")
				}
				// Server wall clock is deliberately one hour behind the local clock.
				now := time.Now().Add(-time.Hour).UTC()
				lease := 5 * time.Minute
				if mode == "short" {
					lease = 7 * time.Second
				}
				if mode == "spent" {
					lease = 4 * time.Second
				}
				if mode == "overlong" {
					lease += time.Second
				}
				return response(200, agentproto.TraceClaimed{ServerTime: now, Task: &agentproto.TraceTask{ID: "job-test", Owner: claim.Owner, ExpiresAt: now.Add(lease), Request: clientTraceRequest()}}), nil
			})}
			_, err := c.TraceOnce(context.Background())
			if mode == "overlong" {
				if err != ErrProtocol || runs != 0 {
					t.Fatal(runs, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "spent" {
				if runs != 0 || code != "probe_cancelled" {
					t.Fatal(runs, code)
				}
			} else if runs != 1 || code != "" {
				t.Fatal(runs, code)
			}
		})
	}
}

func TestTraceDurabilityFailurePreventsNetworkOrExecution(t *testing.T) {
	for _, mode := range []string{"before_claim", "before_start"} {
		t.Run(mode, func(t *testing.T) {
			c := testClient(t)
			if err := c.Journal.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "test-token-never-real"}); err != nil {
				t.Fatal(err)
			}
			calls, runs := 0, 0
			c.traceRun = func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
				runs++
				return successfulTrace(), nil
			}
			c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				var in agentproto.ProbeClaim
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Fatal("claim decode")
				}
				c.Journal.Close()
				now := time.Now()
				return response(200, agentproto.TraceClaimed{ServerTime: now, Task: &agentproto.TraceTask{ID: "job-test", Owner: in.Owner, ExpiresAt: now.Add(time.Minute), Request: clientTraceRequest()}}), nil
			})}
			if mode == "before_claim" {
				c.Journal.Close()
			}
			if _, err := c.TraceOnce(context.Background()); err != agentstate.ErrState || runs != 0 {
				t.Fatal(runs, err)
			}
			if mode == "before_claim" && calls != 0 || mode == "before_start" && calls != 1 {
				t.Fatal(calls)
			}
		})
	}
}

package probes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func traceRequest() netprobe.TraceRequest {
	return netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, MaxHops: 30}
}
func TestServerTraceWorkerPersistsSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := database(t)
		ctx := context.Background()
		j, err := s.EnqueueServerTrace(ctx, strings.Repeat("k", 32), traceRequest(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		runner := Runner{Store: s, ExecuteTrace: func(ctx context.Context, q netprobe.TraceRequest) (netprobe.TraceResult, error) {
			calls++
			if q != traceRequest() {
				t.Fatal(q)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 125*time.Second {
				t.Fatal("unbounded execute")
			}
			now := time.Now().UTC()
			out := netprobe.TraceResult{StartedAt: now, FinishedAt: now, Hops: []netprobe.TraceHop{}, ResolvedAddresses: []string{}}
			if fail {
				return out, fmt.Errorf("private detail: %w", netprobe.ErrUnavailable)
			}
			out.Address = "1.1.1.1"
			out.ResolvedAddresses = []string{out.Address}
			out.Hops = []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: now, Address: new(out.Address), RTTMS: new(float64(3)), Reply: new("tcp_reset")}}}}
			return out, nil
		}}
		worked, err := runner.RunTraceOne(ctx)
		if err != nil || !worked || calls != 1 {
			t.Fatal(worked, calls, err)
		}
		job, err := s.Job(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		expected := "succeeded"
		if fail {
			expected = "failed"
		}
		if job.Status != expected || job.Attempts != 1 {
			t.Fatal(job)
		}
		var raw string
		if err = s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", j.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var result store.ServerTraceResult
		if json.Unmarshal([]byte(raw), &result) != nil || strings.Contains(raw, "private detail") {
			t.Fatal(raw)
		}
		if fail {
			if result.Result.StopReason != "incomplete" || result.ErrorCode == nil || *result.ErrorCode != "probe_unavailable" {
				t.Fatal(result)
			}
		} else if !result.Result.Reached || result.Result.StopReason != "reached" || *result.Result.Hops[0].Samples[0].RTTMS != 3 {
			t.Fatal(result)
		}
		if worked, err = runner.RunTraceOne(ctx); worked || err != nil || calls != 1 {
			t.Fatal("replayed finished probe", worked, err)
		}
	}
}
func TestServerTraceCancellationIsDurablyRecorded(t *testing.T) {
	s := database(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j, err := s.EnqueueServerTrace(ctx, strings.Repeat("k", 32), traceRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	runner := Runner{Store: s, ExecuteTrace: func(ctx context.Context, _ netprobe.TraceRequest) (netprobe.TraceResult, error) {
		at := time.Now().UTC()
		close(started)
		<-ctx.Done()
		return netprobe.TraceResult{StartedAt: at, FinishedAt: time.Now().UTC(), Hops: []netprobe.TraceHop{}, ResolvedAddresses: []string{}}, netprobe.ErrCancelled
	}}
	go func() { _, err := runner.RunTraceOne(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("not stopped")
	}
	job, err := s.Job(context.Background(), j.ID)
	if err != nil || job.Status != "failed" || job.Error.Code != "probe_cancelled" {
		t.Fatal(job, err)
	}
}
func TestServerTraceDoesNotReplayLostClaim(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	now := time.Now()
	j, err := s.EnqueueServerTrace(ctx, strings.Repeat("k", 32), traceRequest(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimServerTrace(ctx, now); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Store: s, ExecuteTrace: func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
		t.Fatal("replayed existing claim")
		return netprobe.TraceResult{}, nil
	}}
	if worked, err := runner.RunTraceOne(ctx); worked || err != nil {
		t.Fatal(worked, err)
	}
	if err = s.ExpireNetworkProbes(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if worked, err := runner.RunTraceOne(ctx); worked || err != nil {
		t.Fatal(worked, err)
	}
	job, _ := s.Job(ctx, j.ID)
	if job.Status != "failed" || job.Error.Code != "probe_expired" {
		t.Fatal(job)
	}
}

func TestServerNetworkLoopAlternatesAndRecordsBothKinds(t *testing.T) {
	s := database(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, key := range []string{"a", "b"} {
		if _, err := s.EnqueueServerProbe(ctx, strings.Repeat("p"+key, 16), request(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EnqueueServerTrace(ctx, strings.Repeat("t"+key, 16), traceRequest(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	order := []string{}
	runner := Runner{Store: s,
		Execute: func(context.Context, netprobe.Request) (netprobe.Result, error) {
			order = append(order, "probe")
			now := time.Now().UTC()
			return netprobe.Result{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Samples: []netprobe.Sample{}}, netprobe.ErrUnavailable
		},
		ExecuteTrace: func(context.Context, netprobe.TraceRequest) (netprobe.TraceResult, error) {
			order = append(order, "trace")
			if len(order) == 4 {
				cancel()
			}
			now := time.Now().UTC()
			return netprobe.TraceResult{StartedAt: now, FinishedAt: now, ResolvedAddresses: []string{}, Hops: []netprobe.TraceHop{}}, netprobe.ErrUnavailable
		},
	}
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("network worker failed to drain queues")
	}
	if strings.Join(order, ",") != "probe,trace,probe,trace" {
		t.Fatal(order)
	}
	var ended int
	if err := s.DB.QueryRow("SELECT count(*) FROM job WHERE status='failed' AND error_code='probe_unavailable'").Scan(&ended); err != nil || ended != 4 {
		t.Fatal("cancelled loop lost terminal result", ended, err)
	}
}

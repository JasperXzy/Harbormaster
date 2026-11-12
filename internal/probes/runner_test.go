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

func request() netprobe.Request {
	return netprobe.Request{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, IntervalMS: 100}
}
func database(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestServerProbeWorkerPersistsSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s := database(t)
		ctx := context.Background()
		j, err := s.EnqueueServerProbe(ctx, strings.Repeat("k", 32), request(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		runner := Runner{Store: s, Execute: func(ctx context.Context, q netprobe.Request) (netprobe.Result, error) {
			calls++
			if q != request() {
				t.Fatal(q)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 65*time.Second {
				t.Fatal("unbounded execute")
			}
			now := time.Now().UTC()
			out := netprobe.Result{StartedAt: now, FinishedAt: now, Samples: []netprobe.Sample{}, ResolvedAddresses: []string{}}
			if fail {
				return out, fmt.Errorf("private detail: %w", netprobe.ErrUnavailable)
			}
			out.Address = "1.1.1.1"
			out.ResolvedAddresses = []string{out.Address}
			out.Samples = []netprobe.Sample{{Sequence: 1, StartedAt: now, RTTMS: new(float64(3))}}
			return out, nil
		}}
		worked, err := runner.RunOne(ctx)
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
		var result store.ServerProbeResult
		if json.Unmarshal([]byte(raw), &result) != nil || strings.Contains(raw, "private detail") {
			t.Fatal(raw)
		}
		if fail {
			if result.Result.Summary != nil || result.ErrorCode == nil || *result.ErrorCode != "probe_unavailable" {
				t.Fatal(result)
			}
		} else if result.Result.Summary == nil || *result.Result.Summary.AverageMS != 3 {
			t.Fatal(result)
		}
		if worked, err = runner.RunOne(ctx); worked || err != nil || calls != 1 {
			t.Fatal("replayed finished probe", worked, err)
		}
	}
}
func TestServerProbeCancellationIsDurablyRecorded(t *testing.T) {
	s := database(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j, err := s.EnqueueServerProbe(ctx, strings.Repeat("k", 32), request(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	runner := Runner{Store: s, Execute: func(ctx context.Context, _ netprobe.Request) (netprobe.Result, error) {
		at := time.Now().UTC()
		close(started)
		<-ctx.Done()
		return netprobe.Result{StartedAt: at, FinishedAt: time.Now().UTC(), Samples: []netprobe.Sample{}, ResolvedAddresses: []string{}}, netprobe.ErrCancelled
	}}
	go func() { _, err := runner.RunOne(ctx); done <- err }()
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
func TestServerProbeDoesNotReplayLostClaim(t *testing.T) {
	s := database(t)
	ctx := context.Background()
	now := time.Now()
	j, err := s.EnqueueServerProbe(ctx, strings.Repeat("k", 32), request(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimServerProbe(ctx, now); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Store: s, Execute: func(context.Context, netprobe.Request) (netprobe.Result, error) {
		t.Fatal("replayed existing claim")
		return netprobe.Result{}, nil
	}}
	if worked, err := runner.RunOne(ctx); worked || err != nil {
		t.Fatal(worked, err)
	}
	if err = s.ExpireNetworkProbes(ctx, now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if worked, err := runner.RunOne(ctx); worked || err != nil {
		t.Fatal(worked, err)
	}
	job, _ := s.Job(ctx, j.ID)
	if job.Status != "failed" || job.Error.Code != "probe_expired" {
		t.Fatal(job)
	}
}

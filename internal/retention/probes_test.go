package retention

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/store"
)

type pruneFunc func(context.Context, time.Time, int, int) (store.ProbePruneResult, error)

func (f pruneFunc) PruneProbeHistory(ctx context.Context, now time.Time, days, limit int) (store.ProbePruneResult, error) {
	return f(ctx, now, days, limit)
}

func TestRunnerStartsImmediatelyAndCatchesUpInBoundedBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	r := Runner{Days: 7, Store: pruneFunc(func(step context.Context, now time.Time, days, limit int) (store.ProbePruneResult, error) {
		calls++
		deadline, ok := step.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || now.Location() != time.UTC || days != 7 || limit != 500 {
			t.Error("unbounded cleanup step")
		}
		if calls == 1 {
			return store.ProbePruneResult{Payloads: 500, More: true}, nil
		}
		cancel()
		return store.ProbePruneResult{}, nil
	})}
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not catch up or stop")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestRunnerCancellationInterruptsStoreAndPreCancelledRunDoesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	r := Runner{Store: pruneFunc(func(step context.Context, _ time.Time, days, _ int) (store.ProbePruneResult, error) {
		if days != 30 {
			t.Error(days)
		}
		close(started)
		<-step.Done()
		return store.ProbePruneResult{}, step.Err()
	})}
	go func() { defer close(done); r.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("store was not cancelled")
	}
	r.Store = pruneFunc(func(context.Context, time.Time, int, int) (store.ProbePruneResult, error) {
		t.Error("pre-cancelled cleanup ran")
		return store.ProbePruneResult{}, nil
	})
	r.Run(ctx)
}

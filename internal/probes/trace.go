package probes

import (
	"context"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (r Runner) RunTraceOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	claim, err := r.Store.ClaimServerTrace(dbCtx, time.Now().UTC())
	cancel()
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dbCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	err = r.Store.AuthorizeServerTrace(dbCtx, claim, time.Now().UTC())
	cancel()
	if err != nil {
		return true, err
	}
	timeout := min(125*time.Second, time.Until(claim.ExpiresAt)-5*time.Second)
	if timeout <= 0 {
		return true, store.ErrLeaseLost
	}
	execute := r.ExecuteTrace
	if execute == nil {
		execute = netprobe.Trace
	}
	probeCtx, stop := context.WithTimeout(ctx, timeout)
	result, probeErr := execute(probeCtx, claim.Request)
	stop()
	// Record cancellation after stopping measurement; this bounded local write
	// is independent of the cancelled service context, not another probe.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	return true, r.Store.FinishServerTrace(finishCtx, claim, result, netprobe.FailureCode(probeErr), time.Now().UTC())
}

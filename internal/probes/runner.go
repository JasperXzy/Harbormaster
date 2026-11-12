// Package probes runs the server's fixed local measurement queue. It does not
// consume agent tasks or perform cloud operations.
package probes

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type Runner struct {
	Store   *store.Store
	Log     *slog.Logger
	Execute func(context.Context, netprobe.Request) (netprobe.Result, error)
}

func (r Runner) RunOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	claim, err := r.Store.ClaimServerProbe(dbCtx, time.Now().UTC())
	cancel()
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dbCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	err = r.Store.AuthorizeServerProbe(dbCtx, claim, time.Now().UTC())
	cancel()
	if err != nil {
		return true, err
	}
	timeout := min(65*time.Second, time.Until(claim.ExpiresAt)-5*time.Second)
	if timeout <= 0 {
		return true, store.ErrLeaseLost
	}
	execute := r.Execute
	if execute == nil {
		execute = netprobe.Run
	}
	probeCtx, stop := context.WithTimeout(ctx, timeout)
	result, probeErr := execute(probeCtx, claim.Request)
	stop()
	// Record cancellation after stopping measurement; this bounded local write
	// is independent of the cancelled service context, not another probe.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	return true, r.Store.FinishServerProbe(finishCtx, claim, result, netprobe.FailureCode(probeErr), time.Now().UTC())
}
func (r Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := r.RunOne(ctx)
		if err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Warn("server_probe_step_failed")
		}
		wait := 2 * time.Second
		if worked && err == nil {
			wait = time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

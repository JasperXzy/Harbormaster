package traceinfo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type Runner struct {
	Store    *store.Store
	Enricher Enricher
	Log      *slog.Logger
}

func (r Runner) RunOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	c, err := r.Store.ClaimTraceEnrichment(dbCtx, time.Now().UTC())
	cancel()
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var request netprobe.TraceRequest
	var wrapped struct {
		Result    netprobe.TraceResult `json:"result"`
		ErrorCode *string              `json:"error_code"`
	}
	invalid := json.Unmarshal([]byte(c.RequestJSON), &request) != nil || json.Unmarshal([]byte(c.ResultJSON), &wrapped) != nil
	if !invalid {
		invalid = netprobe.ValidateTraceResult(request, &wrapped.Result, wrapped.ErrorCode, time.Now().UTC()) != nil
	}
	if invalid {
		dbCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return true, r.Store.FinishTraceEnrichment(dbCtx, c, nil, "trace_enrichment_invalid", time.Now().UTC())
	}
	workCtx, stop := context.WithTimeout(ctx, min(30*time.Second, time.Until(c.ExpiresAt)-5*time.Second))
	result, err := r.Enricher.Enrich(workCtx, wrapped.Result, time.Now().UTC())
	stop()
	if err != nil {
		if errors.Is(err, netprobe.ErrInvalid) {
			dbCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return true, r.Store.FinishTraceEnrichment(dbCtx, c, nil, "trace_enrichment_invalid", time.Now().UTC())
		}
		// A cancelled read-only enrichment can be reclaimed after expiry; the
		// network measurement is never replayed, and no partial version is shown.
		return true, err
	}
	result.EnrichedAt = time.Now().UTC()
	dbCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return true, r.Store.FinishTraceEnrichment(dbCtx, c, &result, "", time.Now().UTC())
}

func (r Runner) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := r.RunOne(ctx)
		if err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Warn("trace_enrichment_step_failed")
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

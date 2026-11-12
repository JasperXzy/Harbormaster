package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type ServerTraceClaim struct {
	Job       model.Job
	Request   netprobe.TraceRequest
	ExpiresAt time.Time
}
type ServerTraceResult struct {
	Result    netprobe.TraceResult `json:"result"`
	ErrorCode *string              `json:"error_code"`
}

func (s *Store) EnqueueServerTrace(ctx context.Context, key string, q netprobe.TraceRequest, now time.Time) (model.Job, error) {
	return s.enqueueServerNetwork(ctx, key, "trace", q, now)
}
func (s *Store) ClaimServerTrace(ctx context.Context, now time.Time) (ServerTraceClaim, error) {
	claimed, err := s.claimServerNetwork(ctx, "trace", now)
	claim := ServerTraceClaim{Job: claimed.Job, ExpiresAt: claimed.ExpiresAt}
	if err == nil && json.Unmarshal([]byte(claimed.RequestRaw), &claim.Request) != nil {
		return claim, netprobe.ErrInvalid
	}
	return claim, err
}
func (s *Store) AuthorizeServerTrace(ctx context.Context, claim ServerTraceClaim, now time.Time) error {
	return s.authorizeServerNetwork(ctx, claim.Job, claim.Request, "trace", now)
}
func (s *Store) FinishServerTrace(ctx context.Context, claim ServerTraceClaim, result netprobe.TraceResult, code string, now time.Time) error {
	var failure *string
	if code != "" {
		failure = &code
	}
	if err := netprobe.ValidateTraceResult(claim.Request, &result, failure, now); err != nil {
		return err
	}
	if claim.Job.StartedAt == nil || result.StartedAt.Before(claim.Job.StartedAt.Add(-2*time.Minute)) {
		return netprobe.ErrInvalid
	}
	raw, err := json.Marshal(ServerTraceResult{Result: result, ErrorCode: failure})
	if err != nil {
		return netprobe.ErrInvalid
	}
	return s.finishServerNetwork(ctx, claim.Job, claim.Request, "trace", raw, failure, now)
}

package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type Pinger interface {
	Send(context.Context, string) error
}
type Runner struct {
	Store   *store.Store
	Box     *security.Box
	Sender  Pinger
	Healthy func(context.Context) bool
	Log     *slog.Logger
}

func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := r.RunOne(ctx); err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Warn("heartbeat_step_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (r *Runner) RunOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if r.Store == nil || r.Box == nil || r.Sender == nil || r.Healthy == nil {
		return false, errors.New("heartbeat_unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	claim, err := r.Store.ClaimHeartbeat(callCtx, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	code := "heartbeat_unhealthy"
	if r.Healthy(callCtx) {
		plain, openErr := r.Box.Decrypt(claim.Ciphertext, model.HeartbeatCredentialOwner)
		if openErr != nil {
			code = "heartbeat_credential_invalid"
		} else {
			defer clear(plain)
			if err = r.Store.AuthorizeHeartbeat(callCtx, claim, time.Now().UTC()); err != nil {
				return true, err
			}
			if !time.Now().Before(claim.ExpiresAt) || callCtx.Err() != nil {
				code = "heartbeat_cancelled"
			} else {
				code = ErrorCode(r.Sender.Send(callCtx, string(plain)))
			}
		}
	}
	// Cleanup is local only and bounded; it never performs another send.
	finishCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	return true, r.Store.FinishHeartbeat(finishCtx, claim, code, time.Now().UTC())
}

package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/store"
)

// Evaluator creates local notification records only. Dispatch is a separate
// worker, gated on the user's still-pending delivery policy and configuration.
type Evaluator struct {
	Store *store.Store
	Log   *slog.Logger
}

func (e Evaluator) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := e.Store.EvaluateAlerts(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil && e.Log != nil {
			e.Log.Warn("alert_evaluation_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

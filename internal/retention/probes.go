package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/store"
)

type probeStore interface {
	PruneProbeHistory(context.Context, time.Time, int, int) (store.ProbePruneResult, error)
}

type Runner struct {
	Store probeStore
	Days  int
	Log   *slog.Logger
}

// Run catches up in bounded batches, yielding between them, then checks hourly.
// It is started only by production serve and is joined before SQLite closes.
func (r Runner) Run(ctx context.Context) {
	if r.Store == nil {
		return
	}
	days := r.Days
	if days == 0 {
		days = 30
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		step, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := r.Store.PruneProbeHistory(step, time.Now().UTC(), days, 500)
		cancel()
		if ctx.Err() != nil {
			return
		}
		delay := time.Hour
		if err != nil {
			delay = time.Minute
			if r.Log != nil {
				r.Log.Warn("probe_history_cleanup_failed")
			}
		} else {
			if result.More {
				delay = time.Second
			}
			if r.Log != nil && (result.Payloads > 0 || result.HistoryRows > 0) {
				r.Log.Info("probe_history_cleaned", "payloads", result.Payloads, "history_rows", result.HistoryRows, "retention_days", days)
			}
		}
		timer.Reset(delay)
	}
}

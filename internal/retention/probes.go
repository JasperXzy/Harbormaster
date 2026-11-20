package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type probeStore interface {
	PruneProbeHistory(context.Context, time.Time, int, int) (store.ProbePruneResult, error)
}

type historyPruneStore interface {
	Settings(context.Context, string, int) (string, model.RetentionSettings, error)
	PruneDataHistory(context.Context, time.Time, model.RetentionSettings, int) (store.HistoryPruneResult, error)
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
		now := time.Now().UTC()
		probeDays := days
		ret := model.DefaultRetentionSettings(days)
		hs, hasHistory := r.Store.(historyPruneStore)
		if hasHistory {
			if _, configured, sErr := hs.Settings(step, "", days); sErr == nil {
				ret = configured
				probeDays = configured.LatencyProbeDays
			}
		}
		result, err := r.Store.PruneProbeHistory(step, now, probeDays, 500)
		var histResult store.HistoryPruneResult
		if err == nil && hasHistory && step.Err() == nil {
			var hErr error
			histResult, hErr = hs.PruneDataHistory(step, now, ret, 500)
			if hErr != nil {
				err = hErr
			}
		}
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
			if result.More || histResult.More {
				delay = time.Second
			}
			if r.Log != nil && (result.Payloads > 0 || result.HistoryRows > 0 || histResult.RawAgentSamples > 0 || histResult.HourlyBuckets > 0 || histResult.AuditEntries > 0 || histResult.Jobs > 0) {
				r.Log.Info("probe_history_cleaned", "payloads", result.Payloads, "history_rows", result.HistoryRows, "raw_samples", histResult.RawAgentSamples, "hourly_buckets", histResult.HourlyBuckets, "audit_entries", histResult.AuditEntries, "jobs", histResult.Jobs, "retention_days", probeDays)
			}
		}
		timer.Reset(delay)
	}
}

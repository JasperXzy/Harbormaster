package estimation

import (
	"context"
	"github.com/JasperXzy/harbormaster/internal/store"
	"log/slog"
	"time"
)

// Runner computes local cached estimates. It has no cloud writer or credentials.
type Runner struct {
	Store    *store.Store
	Timezone string
	Log      *slog.Logger
}

func (r Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		timeout, cancel := context.WithTimeout(ctx, 8*time.Second)
		loc, err := r.Store.BusinessLocation(timeout, r.Timezone)
		if err == nil {
			err = r.Store.RefreshEstimates(timeout, loc, time.Now().UTC())
		}
		cancel()
		if err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Warn("traffic_estimation_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

package protection

import (
	"context"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
	"log/slog"
	"time"
)

type Runner struct {
	Store  *store.Store
	Policy model.ProtectionPolicy
	Log    *slog.Logger
}

func (r Runner) Run(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, 8*time.Second)
		policy := r.Policy
		if loc, err := r.Store.BusinessLocation(check, policy.Timezone); err == nil {
			policy.Timezone = loc.String()
		}
		err := r.Store.EvaluateQuotaProtection(check, policy, time.Now().UTC())
		if err == nil {
			err = r.Store.EvaluateAutomation(check, policy, time.Now().UTC())
		}
		cancel()
		if err != nil && ctx.Err() == nil && r.Log != nil {
			r.Log.Warn("quota_protection_evaluation_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

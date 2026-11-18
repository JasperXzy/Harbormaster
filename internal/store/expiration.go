package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Only newly collected instance rows can establish an expiry incident or its
// recovery. Advancing the clock over a stale cached date is not fresh evidence.
func checkExpirationSnapshots(ctx context.Context, tx *sql.Tx, account string, snapshots []model.Snapshot, now time.Time) error {
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='instance_expiring'"))
	if err != nil {
		return err
	}
	if !rule.Enabled {
		return nil
	}
	for _, snapshot := range snapshots {
		if snapshot.Kind != "ecs" && snapshot.Kind != "swas" {
			continue
		}
		o := alertObservation{rule: rule.Key, target: snapshot.ID, account: account, instance: snapshot.ID, outcome: "unknown"}
		switch snapshot.Payload["charge_type"] {
		case "pay_as_you_go", "spot":
			o.outcome = "healthy"
		case "subscription":
			value, _ := snapshot.Payload["expires_at"].(string)
			at, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				continue
			}
			o.basis = at.UTC().Format(time.RFC3339Nano)
			o.outcome = "healthy"
			if !at.After(now.Add(time.Duration(*rule.Params.BeforeDays) * 24 * time.Hour)) {
				o.outcome = "firing"
				state := "unknown"
				if renewal, ok := snapshot.Payload["renewal"].(map[string]any); ok {
					switch renewal["status"] {
					case "auto_renewal", "manual_renewal", "no_renewal":
						state = renewal["status"].(string)
					}
				}
				o.facts = alertFacts{expiresAt: &at, renewal: state}
			}
		}
		if err := applyAlert(ctx, tx, o, now); err != nil {
			return err
		}
	}
	return nil
}

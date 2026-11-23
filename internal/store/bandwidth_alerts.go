package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func checkBandwidthSnapshots(ctx context.Context, tx *sql.Tx, account string, snapshots []model.Snapshot, now time.Time) error {
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='spec_bandwidth_insufficient'"))
	if err != nil {
		return err
	}
	if !rule.Enabled {
		return nil
	}
	for _, snapshot := range snapshots {
		if snapshot.Kind != "ecs" {
			continue
		}
		capacity, ok := snapshot.Payload["network_capacity"].(map[string]any)
		if !ok || capacity["last_error"] != nil {
			continue
		}
		limited, known := capacity["public_outbound_exceeds_spec"].(bool)
		public, publicKnown := capacity["public_outbound_mbps"].(int64)
		spec, specKnown := capacity["tx_kbps"].(int64)
		if !known || !publicKnown || !specKnown || public < 0 || spec <= 0 {
			continue
		}
		o := alertObservation{rule: rule.Key, target: snapshot.ID, account: account, instance: snapshot.ID, outcome: "healthy"}
		if limited {
			o.outcome = "firing"
			o.facts = alertFacts{publicMbps: public, specKbps: spec}
		}
		if err := applyAlert(ctx, tx, o, now); err != nil {
			return err
		}
	}
	return nil
}

package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Public IPv4 changes are discrete events, not open incidents. A return to a
// former address is another change, never an invented recovery notification.
func notifyInstanceIPChange(ctx context.Context, tx *sql.Tx, event model.InstanceEvent, now time.Time) error {
	if event.Kind != "public_ipv4_changed" {
		return nil
	}
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='instance_public_ip_changed'"))
	if err != nil || !rule.Enabled {
		return err
	}
	// Wording remains a placeholder for frontend-owned templates. The complete
	// before/after address sets remain in instance event history, not raw logs.
	_, err = enqueueNotification(ctx, tx, model.Notification{
		EventKey: "instance-event/" + event.ID,
		Kind:     "alert", Severity: "info", RuleKey: &rule.Key,
		Title:     "Harbormaster alert: instance_public_ip_changed",
		Body:      "target=" + event.InstanceID + "; instance_event_id=" + event.ID,
		AccountID: &event.AccountID, InstanceID: &event.InstanceID,
	}, now)
	return err
}

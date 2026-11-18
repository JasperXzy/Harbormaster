package store

import (
	"context"
	"database/sql"
	"strings"
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
	// The complete before/after address sets remain in instance event history.
	name := event.InstanceName
	if name == "" {
		name = instanceCopyName(ctx, tx, event.InstanceID)
	}
	body := ""
	if after, ok := event.After["addresses"].([]string); ok && len(after) > 0 {
		body = "现在是 " + strings.Join(after, "、") + "。"
	} else if ok {
		body = "现在没有公网 IPv4。"
	}
	_, err = enqueueNotification(ctx, tx, model.Notification{
		EventKey: "instance-event/" + event.ID,
		Kind:     "alert", Severity: "info", RuleKey: &rule.Key,
		Title:     severitySymbol("alert", "info") + " " + name + " 的公网 IP 变了",
		Body:      body,
		AccountID: &event.AccountID, InstanceID: &event.InstanceID,
	}, now)
	return err
}

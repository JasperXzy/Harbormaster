package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type idleAlertTarget struct {
	id, cloud, instance string
	at                  time.Time
}

// Only a newly accepted, complete source list may establish a condition or
// recovery. Unknown IDs retain state; absent resources become inapplicable.
func checkIdleObservation(ctx context.Context, tx *sql.Tx, a model.Account, o model.IdleObservation, now time.Time) error {
	if o.Error != nil || o.Data == nil || now.Sub(o.QueriedAt) > 10*time.Minute {
		return nil
	}
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='idle_resource_detected'"))
	if err != nil || !rule.Enabled {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT target_id,cloud_id,coalesce(instance_id,''),last_observed_at FROM idle_alert_target WHERE account_id=? AND region=? AND kind=?", a.ID, o.Region, o.Kind)
	if err != nil {
		return err
	}
	targets := map[string]idleAlertTarget{}
	for rows.Next() {
		var target idleAlertTarget
		var at string
		if err := rows.Scan(&target.id, &target.cloud, &target.instance, &at); err != nil {
			rows.Close()
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			rows.Close()
			return err
		}
		target.at = parsed
		targets[target.cloud] = target
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	positive, unknown, healthy := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range o.Data.UnclassifiedIDs {
		unknown[id] = true
	}
	for _, id := range o.Data.NonCandidateIDs {
		healthy[id] = true
	}
	for _, c := range o.Data.Candidates {
		positive[c.CloudID] = true
		previous, exists := targets[c.CloudID]
		if exists && o.QueriedAt.Before(previous.at) {
			continue
		}
		instance := ""
		if len(c.InstanceIDs) == 1 {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=? AND account_id=?", c.InstanceIDs[0], a.ID).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return ErrIdleObservation
			}
			instance = c.InstanceIDs[0]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO idle_alert_target(target_id,account_id,region,kind,cloud_id,instance_id,last_observed_at)
 VALUES(?,?,?,?,?,nullif(?,''),?) ON CONFLICT(target_id) DO UPDATE SET instance_id=excluded.instance_id,last_observed_at=excluded.last_observed_at`, c.ID, a.ID, o.Region, o.Kind, c.CloudID, instance, timestamp(o.QueriedAt)); err != nil {
			return err
		}
		if err := applyAlert(ctx, tx, alertObservation{rule: rule.Key, target: c.ID, account: a.ID, instance: instance, resource: c.ID, outcome: "firing", detail: "kind=" + c.Kind + "; reason=" + c.Reason + "; monthly_cost=unestimated"}, now); err != nil {
			return err
		}
	}
	for cloud, target := range targets {
		if positive[cloud] || o.QueriedAt.Before(target.at) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE idle_alert_target SET last_observed_at=? WHERE target_id=?", timestamp(o.QueriedAt), target.id); err != nil {
			return err
		}
		if unknown[cloud] {
			continue
		}
		if healthy[cloud] {
			if err := applyAlert(ctx, tx, alertObservation{rule: rule.Key, target: target.id, account: a.ID, instance: target.instance, resource: target.id, outcome: "healthy"}, now); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE rule_key=? AND target_id=? AND active=1", timestamp(now), rule.Key, target.id); err != nil {
			return err
		}
		if err := cancelPendingNotifications(ctx, tx, "rule_key=? AND resource_id=?", []any{rule.Key, target.id}, "idle_resource_no_longer_observed", now); err != nil {
			return err
		}
	}
	return nil
}

// The periodic evaluator keeps monitored logical targets unknown between
// reads, including across credential rotation. It cannot recover from caches.
func idleAlertObservations(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule) ([]alertObservation, error) {
	result := []alertObservation{}
	if !rules["idle_resource_detected"].Enabled {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.target_id,t.account_id,coalesce(t.instance_id,'') FROM idle_alert_target t
 JOIN account_settings a ON a.account_id=t.account_id AND a.enabled=1
 WHERE EXISTS(SELECT 1 FROM json_each(a.config_json,'$.region_ids') r WHERE r.value=t.region)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var target, account, instance string
		if err := rows.Scan(&target, &account, &instance); err != nil {
			return nil, err
		}
		result = append(result, alertObservation{rule: "idle_resource_detected", target: target, account: account, instance: instance, resource: target, outcome: "unknown"})
	}
	return result, rows.Err()
}

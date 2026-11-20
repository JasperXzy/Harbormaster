package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type AlertValidationError struct{ Fields map[string]string }

func (*AlertValidationError) Error() string { return "validation_failed" }
func readAlertRule(row rowScanner) (model.AlertRule, error) {
	var r model.AlertRule
	var raw string
	err := row.Scan(&r.Key, &r.Enabled, &raw, &r.Urgent)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(raw), &r.Params); err != nil {
		return r, err
	}
	if len(r.Validate()) != 0 {
		return r, errors.New("invalid_stored_alert_rule")
	}
	return r, nil
}
func (s *Store) AlertRules(ctx context.Context) ([]model.AlertRule, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []model.AlertRule{}
	for rows.Next() {
		r, err := readAlertRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}
func (s *Store) UpdateAlertRule(ctx context.Context, key string, update model.AlertRuleUpdate, now time.Time) (model.AlertRule, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.AlertRule{}, err
	}
	defer tx.Rollback()
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key=?", key))
	if err != nil {
		return rule, err
	}
	previous := rule
	if update.Enabled != nil {
		rule.Enabled = *update.Enabled
	}
	if update.Params != nil {
		rule.Params = *update.Params
	}
	if fields := rule.Validate(); len(fields) != 0 {
		return rule, &AlertValidationError{Fields: fields}
	}
	if err := updateBalanceTargets(ctx, tx, previous, rule, now); err != nil {
		return rule, err
	}
	if err := updateProbeAlertTargets(ctx, tx, previous, rule, now); err != nil {
		return rule, err
	}
	raw, err := json.Marshal(rule.Params)
	if err != nil {
		return rule, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE alert_rule SET enabled=?,params_json=? WHERE key=?", rule.Enabled, string(raw), key); err != nil {
		return rule, err
	}
	if !rule.Enabled {
		if _, err = tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE rule_key=? AND active=1", timestamp(now), key); err != nil {
			return rule, err
		}
		if err = cancelPendingNotifications(ctx, tx, "rule_key=?", []any{key}, "alert_rule_disabled", now); err != nil {
			return rule, err
		}
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "alert_rule_updated", TargetType: "alert_rule", TargetID: key, Details: auditDiff(auditChange{"enabled", previous.Enabled, rule.Enabled}, auditChange{"params", previous.Params, rule.Params})}, now); err != nil {
		return rule, err
	}
	return rule, tx.Commit()
}
func (s *Store) AgentOfflineAfter(ctx context.Context) (time.Duration, error) {
	rule, err := readAlertRule(s.DB.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='agent_offline'"))
	if err != nil {
		return 0, err
	}
	return time.Duration(*rule.Params.OfflineAfterSeconds) * time.Second, nil
}

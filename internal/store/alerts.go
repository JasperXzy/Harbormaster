package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type alertObservation struct {
	rule, target, account, instance string
	outcome                         string // firing, healthy, unknown; absent targets are no longer applicable.
	detail                          string
	basis                           string
}

// EvaluateAlerts reads evidence and commits state transitions + notifications
// under one SQLite transaction. It neither calls providers nor sends messages.
func (s *Store) EvaluateAlerts(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rules := map[string]model.AlertRule{}
	rows, err := tx.QueryContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule ORDER BY key")
	if err != nil {
		return err
	}
	for rows.Next() {
		r, err := readAlertRule(rows)
		if err != nil {
			rows.Close()
			return err
		}
		rules[r.Key] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Read account metadata, not credentials. Disabled accounts have no applicable
	// observations, so their open incidents close silently below.
	rows, err = tx.QueryContext(ctx, "SELECT account_id,credential_version,config_json FROM account_settings WHERE enabled=1 ORDER BY account_id")
	if err != nil {
		return err
	}
	accounts := []model.Account{}
	for rows.Next() {
		var a model.Account
		var raw, id string
		var version int64
		if err = rows.Scan(&id, &version, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &a); err != nil {
			rows.Close()
			return err
		}
		a.ID = id
		a.CredentialVersion = version
		accounts = append(accounts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	observations := []alertObservation{}
	for _, a := range accounts {
		if r := rules["balance_low"]; r.Enabled {
			for _, threshold := range *r.Params.BalanceThresholds {
				if threshold.AccountID == a.ID {
					observations = append(observations, alertObservation{rule: r.Key, target: a.ID, account: a.ID, outcome: "unknown"})
					break
				}
			}
		}
		if r := rules["account_api_failure"]; r.Enabled {
			o := alertObservation{rule: r.Key, target: a.ID, account: a.ID, outcome: "unknown"}
			if a.SyncStatus == "ok" && a.ConsecutiveFailures == 0 {
				o.outcome = "healthy"
			}
			if a.SyncStatus == "failing" && a.ConsecutiveFailures >= *r.Params.ConsecutiveFailures {
				o.outcome = "firing"
				o.detail = "recorded_consecutive_failures=" + strconv.Itoa(a.ConsecutiveFailures)
			}
			observations = append(observations, o)
		}
		if r := rules["cdt_official_unavailable"]; r.Enabled {
			o := alertObservation{rule: r.Key, target: a.ID, account: a.ID, outcome: "unknown"}
			source, err := tx.QueryContext(ctx, "SELECT error_code FROM cdt_observation WHERE account_id=? AND credential_version=? ORDER BY id DESC LIMIT ?", a.ID, a.CredentialVersion, *r.Params.ConsecutiveFailures)
			if err != nil {
				return err
			}
			failures := 0
			first := true
			for source.Next() {
				var code sql.NullString
				if err = source.Scan(&code); err != nil {
					source.Close()
					return err
				}
				if !code.Valid {
					if first {
						o.outcome = "healthy"
					}
					break
				}
				failures++
				first = false
			}
			err = source.Err()
			source.Close()
			if err != nil {
				return err
			}
			if failures >= *r.Params.ConsecutiveFailures {
				o.outcome = "firing"
				o.detail = "recorded_consecutive_failures=" + strconv.Itoa(failures)
			}
			observations = append(observations, o)
		}
	}
	if r := rules["agent_offline"]; r.Enabled {
		rows, err = tx.QueryContext(ctx, `SELECT i.id,i.account_id,g.last_received_at FROM agent g JOIN instance i ON i.id=g.instance_id
 JOIN account_settings a ON a.account_id=i.account_id AND a.enabled=1
 WHERE g.revoked_at IS NULL AND i.status='running' AND EXISTS(SELECT 1 FROM resource_snapshot rs WHERE rs.account_id=i.account_id AND rs.id=i.id AND rs.kind=i.kind AND rs.present=1)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, account string
			var last sql.NullString
			if err = rows.Scan(&id, &account, &last); err != nil {
				rows.Close()
				return err
			}
			o := alertObservation{rule: r.Key, target: id, account: account, instance: id, outcome: "unknown"}
			if at := parseAgentTime(last); at != nil && !at.After(now) {
				o.outcome = "healthy"
				if !now.Before(at.Add(time.Duration(*r.Params.OfflineAfterSeconds) * time.Second)) {
					o.outcome = "firing"
					o.detail = "seconds_since_fresh_report=" + strconv.FormatInt(int64(now.Sub(*at)/time.Second), 10)
				}
			}
			observations = append(observations, o)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	// Snapshot-based transitions require fresh collection. Keep present targets
	// unknown here; confirmed absence still closes their incidents.
	for _, key := range []string{"instance_expiring", "spec_bandwidth_insufficient"} {
		r := rules[key]
		if !r.Enabled {
			continue
		}
		rows, err = tx.QueryContext(ctx, `SELECT i.id,i.account_id FROM instance i JOIN account_settings a ON a.account_id=i.account_id AND a.enabled=1
 WHERE (?='instance_expiring' OR i.kind='ecs') AND EXISTS(SELECT 1 FROM resource_snapshot rs WHERE rs.account_id=i.account_id AND rs.id=i.id AND rs.kind=i.kind AND rs.present=1)`, key)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, account string
			if err = rows.Scan(&id, &account); err != nil {
				rows.Close()
				return err
			}
			observations = append(observations, alertObservation{rule: r.Key, target: id, account: account, instance: id, outcome: "unknown"})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, o := range observations {
		seen[o.rule+"/"+o.target] = true
		if err = applyAlert(ctx, tx, o, now); err != nil {
			return err
		}
	}
	// Disabling a rule/account, revoking an agent, releasing/stopping an instance
	// does not mean its connection recovered. Resolve silently without a recovery.
	rows, err = tx.QueryContext(ctx, "SELECT rule_key,target_id FROM alert_state WHERE active=1")
	if err != nil {
		return err
	}
	unseen := [][2]string{}
	for rows.Next() {
		var key, id string
		if err = rows.Scan(&key, &id); err != nil {
			rows.Close()
			return err
		}
		if !seen[key+"/"+id] {
			unseen = append(unseen, [2]string{key, id})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range unseen {
		if _, err = tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE rule_key=? AND target_id=?", timestamp(now), key[0], key[1]); err != nil {
			return err
		}
		if err = cancelPendingNotifications(ctx, tx, "rule_key=? AND (account_id=? OR instance_id=?)", []any{key[0], key[1], key[1]}, "alert_no_longer_applicable", now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func applyAlert(ctx context.Context, tx *sql.Tx, o alertObservation, now time.Time) error {
	if o.outcome == "unknown" {
		return nil
	}
	var active bool
	var episode int64
	var basis string
	err := tx.QueryRowContext(ctx, "SELECT active,episode,basis FROM alert_state WHERE rule_key=? AND target_id=?", o.rule, o.target).Scan(&active, &episode, &basis)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	firing := o.outcome == "firing"
	if active && firing && o.basis != "" && basis != "" && basis != o.basis {
		// A changed deadline still inside the warning window is a new basis,
		// not recovery. Replace an unsent old reminder without claiming health.
		if err = cancelPendingNotifications(ctx, tx, "rule_key=? AND instance_id=? AND kind='alert'", []any{o.rule, o.instance}, "expiration_changed", now); err != nil {
			return err
		}
		active = false
	}
	if active == firing {
		return nil
	}
	if o.rule == "instance_expiring" && !firing {
		if err = cancelPendingNotifications(ctx, tx, "rule_key=? AND instance_id=? AND kind='alert'", []any{o.rule, o.instance}, "expiration_changed", now); err != nil {
			return err
		}
	}
	kind := "recovery"
	severity := "info"
	detail := "source_observed_healthy"
	if firing {
		episode++
		kind = "alert"
		detail = o.detail
		switch o.rule {
		case "agent_offline":
			severity = "critical"
		case "account_api_failure":
			severity = "serious"
		default:
			severity = "warning"
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_state(rule_key,target_id,account_id,active,episode,changed_at,basis) VALUES(?,?,nullif(?,''),?,?,?,?)
 ON CONFLICT(rule_key,target_id) DO UPDATE SET active=excluded.active,episode=excluded.episode,changed_at=excluded.changed_at,basis=excluded.basis`, o.rule, o.target, o.account, firing, episode, timestamp(now), o.basis); err != nil {
		return err
	}
	n := model.Notification{EventKey: "alert/" + o.rule + "/" + o.target + "/" + strconv.FormatInt(episode, 10) + "/" + kind, Kind: kind, Severity: severity, RuleKey: &o.rule, Title: "Harbormaster " + kind + ": " + o.rule, Body: "target=" + o.target + "; " + detail}
	if o.account != "" {
		n.AccountID = &o.account
	}
	if o.instance != "" {
		n.InstanceID = &o.instance
	}
	_, err = enqueueNotification(ctx, tx, n, now)
	return err
}

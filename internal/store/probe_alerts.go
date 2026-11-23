package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var networkAlertKeys = []string{"network_latency", "network_loss", "network_unreachable"}

type probeAlertPolicy struct {
	enabled                      bool
	threshold                    float64
	duration, failures, recovery int
}

func policyForProbe(rule model.AlertRule, id string) probeAlertPolicy {
	p := probeAlertPolicy{enabled: rule.Enabled && rule.Key != "network_latency", threshold: 5, failures: 1}
	if rule.Key == "network_unreachable" {
		p.threshold = 0
	}
	if rule.Params.RecoverySamples != nil {
		p.recovery = *rule.Params.RecoverySamples
	}
	if rule.Params.DurationSeconds != nil {
		p.duration = *rule.Params.DurationSeconds
	}
	if rule.Params.ConsecutiveFailures != nil {
		p.failures = *rule.Params.ConsecutiveFailures
	}
	if rule.Params.ProbeThresholds == nil {
		return p
	}
	for _, entry := range *rule.Params.ProbeThresholds {
		if entry.PlanID != id {
			continue
		}
		p.enabled = rule.Enabled && (entry.Enabled == nil || *entry.Enabled)
		if rule.Key == "network_latency" && entry.LatencyMS != nil {
			p.threshold = *entry.LatencyMS
		}
		if rule.Key == "network_loss" && entry.LossPercent != nil {
			p.threshold = *entry.LossPercent
		}
		break
	}
	return p
}

type probeAlertProgress struct {
	seq, planRevision, targetRevision int64
	bad, good                         int
	since, created, sampled           *time.Time
	address                           string
}

func readProbeAlertProgress(ctx context.Context, tx *sql.Tx, key, id string) (probeAlertProgress, error) {
	var p probeAlertProgress
	var since, created, sampled sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT last_seq,plan_revision,target_revision,bad_count,good_count,bad_since,last_created_at,last_sample_at,last_address FROM probe_alert_progress WHERE rule_key=? AND plan_id=?", key, id).Scan(&p.seq, &p.planRevision, &p.targetRevision, &p.bad, &p.good, &since, &created, &sampled, &p.address)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	p.since, p.created, p.sampled = parseAgentTime(since), parseAgentTime(created), parseAgentTime(sampled)
	return p, err
}
func (p *probeAlertProgress) clear() { p.bad = 0; p.good = 0; p.since = nil }
func storeProbeAlertProgress(ctx context.Context, tx *sql.Tx, key, id string, p probeAlertProgress, now time.Time) error {
	value := func(at *time.Time) any {
		if at == nil {
			return nil
		}
		return timestamp(*at)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO probe_alert_progress(rule_key,plan_id,last_seq,plan_revision,target_revision,bad_count,good_count,bad_since,last_created_at,last_sample_at,last_address,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(rule_key,plan_id) DO UPDATE SET last_seq=excluded.last_seq,plan_revision=excluded.plan_revision,target_revision=excluded.target_revision,bad_count=excluded.bad_count,good_count=excluded.good_count,bad_since=excluded.bad_since,last_created_at=excluded.last_created_at,last_sample_at=excluded.last_sample_at,last_address=excluded.last_address,updated_at=excluded.updated_at`, key, id, p.seq, p.planRevision, p.targetRevision, p.bad, p.good, value(p.since), value(p.created), value(p.sampled), p.address, timestamp(now))
	return err
}
func updateProbeAlertTargets(ctx context.Context, tx *sql.Tx, old, current model.AlertRule, now time.Time) error {
	if !model.NetworkAlertRule(current.Key) {
		return nil
	}
	for i, entry := range *current.Params.ProbeThresholds {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM probe_plan WHERE id=?", entry.PlanID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &AlertValidationError{Fields: map[string]string{"params.probe_thresholds." + strconv.Itoa(i) + ".plan_id": "not_found"}}
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM probe_plan")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		previous, policy := policyForProbe(old, id), policyForProbe(current, id)
		if previous == policy {
			continue
		}
		var seq int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM probe_plan_run WHERE plan_id=?", id).Scan(&seq); err != nil {
			return err
		}
		if err = storeProbeAlertProgress(ctx, tx, current.Key, id, probeAlertProgress{seq: seq}, now); err != nil {
			return err
		}
		if !policy.enabled {
			if _, err = tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE rule_key=? AND target_id=? AND active=1", timestamp(now), current.Key, id); err != nil {
				return err
			}
			if err = cancelPendingNotifications(ctx, tx, "rule_key=? AND probe_plan_id=?", []any{current.Key, id}, "alert_rule_changed", now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Explicitly stopped monitoring is inapplicable; temporary missing evidence is
// still applicable and cannot close an existing incident as recovered.
func probeAlertContext(ctx context.Context, tx *sql.Tx, plan model.ProbePlan, target model.NetworkTarget) (alertObservation, bool, error) {
	o := alertObservation{target: plan.ID, plan: plan.ID, outcome: "unknown"}
	if !plan.Enabled || !target.Enabled {
		return o, false, nil
	}
	ids := []string{}
	if plan.InstanceID != nil {
		ids = append(ids, *plan.InstanceID)
		o.instance = *plan.InstanceID
	}
	if target.InstanceID != nil {
		ids = append(ids, *target.InstanceID)
		if o.instance == "" {
			o.instance = *target.InstanceID
		}
	}
	for _, id := range ids {
		var account string
		var enabled, present bool
		err := tx.QueryRowContext(ctx, `SELECT i.account_id,a.enabled,EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1) FROM instance i JOIN account_settings a ON a.account_id=i.account_id WHERE i.id=?`, id).Scan(&account, &enabled, &present)
		if errors.Is(err, sql.ErrNoRows) {
			return o, false, nil
		}
		if err != nil {
			return o, false, err
		}
		if !enabled || !present {
			return o, false, nil
		}
		if id == o.instance {
			o.account = account
		}
	}
	if plan.PointKind == "agent" {
		var active int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agent WHERE instance_id=? AND revoked_at IS NULL", *plan.InstanceID).Scan(&active); err != nil {
			return o, false, err
		}
		if active == 0 {
			return o, false, nil
		}
	}
	return o, true, nil
}
func breakProbeAlertStreaks(ctx context.Context, tx *sql.Tx, planID string, seq int64, now time.Time) error {
	for _, key := range networkAlertKeys {
		p, err := readProbeAlertProgress(ctx, tx, key, planID)
		if err != nil {
			return err
		}
		if seq <= p.seq {
			continue
		}
		p.clear()
		p.seq = seq
		p.created = &now
		p.sampled = nil
		p.address = ""
		if err = storeProbeAlertProgress(ctx, tx, key, planID, p, now); err != nil {
			return err
		}
	}
	return nil
}

// Called in the same transaction that commits a new terminal measurement. A
// duplicate upload returns before this hook and cannot count the sample twice.
func checkProbeResultAlerts(ctx context.Context, tx *sql.Tx, jobID string, now time.Time) error {
	run, err := readProbeRun(tx.QueryRowContext(ctx, "SELECT "+probeRunColumns+probeRunJoins+"WHERE r.job_id=?", jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	seq, err := strconv.ParseInt(run.ID, 10, 64)
	if err != nil {
		return err
	}
	plan, err := readProbePlan(ctx, tx, run.PlanID)
	if errors.Is(err, ErrNotFound) {
		return breakProbeAlertStreaks(ctx, tx, run.PlanID, seq, now)
	}
	if err != nil {
		return err
	}
	target, err := readTarget(ctx, tx, plan.TargetID)
	if errors.Is(err, ErrNotFound) {
		return breakProbeAlertStreaks(ctx, tx, run.PlanID, seq, now)
	}
	if err != nil {
		return err
	}
	base, applicable, err := probeAlertContext(ctx, tx, plan, target)
	if err != nil {
		return err
	}
	if !applicable || run.PlanRevision != plan.Revision || run.TargetRevision != target.Revision || run.Status != "succeeded" || run.Result == nil || run.Result.Summary == nil {
		return breakProbeAlertStreaks(ctx, tx, run.PlanID, seq, now)
	}
	// Old delayed results are evidence in history, not fresh alert transitions.
	if now.Sub(run.Result.FinishedAt) > time.Duration(plan.FrequencySeconds+75)*time.Second {
		return breakProbeAlertStreaks(ctx, tx, run.PlanID, seq, now)
	}
	for _, key := range networkAlertKeys {
		rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key=?", key))
		if err != nil {
			return err
		}
		policy := policyForProbe(rule, plan.ID)
		if !policy.enabled {
			continue
		}
		p, err := readProbeAlertProgress(ctx, tx, key, plan.ID)
		if err != nil {
			return err
		}
		if seq <= p.seq {
			continue
		}
		sample := run.Result.StartedAt
		gap := time.Duration(plan.FrequencySeconds+10) * time.Second
		if p.planRevision != plan.Revision || p.targetRevision != target.Revision || p.address != run.Result.Address || p.created != nil && (run.CreatedAt.Before(*p.created) || run.CreatedAt.Sub(*p.created) > gap) || p.sampled != nil && (!sample.After(*p.sampled) || sample.Sub(*p.sampled) > gap) {
			p.clear()
		}
		p.seq = seq
		p.planRevision = plan.Revision
		p.targetRevision = target.Revision
		p.created = &run.CreatedAt
		p.sampled = &sample
		p.address = run.Result.Address
		stats := run.Result.Summary
		known, bad, value := true, false, 0.0
		switch key {
		case "network_latency":
			if stats.AverageMS == nil {
				known = false
			} else {
				value = *stats.AverageMS
				bad = value > policy.threshold
			}
		case "network_loss":
			value = stats.LossPercent
			bad = value > policy.threshold
		case "network_unreachable":
			bad = stats.Attempts > 0 && stats.Succeeded == 0
			value = float64(stats.Succeeded)
		}
		outcome := "unknown"
		if !known {
			p.clear()
		} else if bad {
			if p.bad == 0 || p.since == nil {
				p.since = &sample
			}
			p.bad = min(p.bad+1, 1000)
			p.good = 0
			if key == "network_latency" {
				if p.bad >= 2 && sample.Sub(*p.since) >= time.Duration(policy.duration)*time.Second {
					outcome = "firing"
				}
			} else if p.bad >= policy.failures {
				outcome = "firing"
			}
		} else {
			p.bad = 0
			p.since = nil
			var active bool
			err := tx.QueryRowContext(ctx, "SELECT active FROM alert_state WHERE rule_key=? AND target_id=?", key, plan.ID).Scan(&active)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if active {
				p.good = min(p.good+1, 1000)
				if p.good >= policy.recovery {
					outcome = "healthy"
				}
			} else {
				p.good = 0
			}
		}
		if err = storeProbeAlertProgress(ctx, tx, key, plan.ID, p, now); err != nil {
			return err
		}
		o := base
		o.rule = key
		o.outcome = outcome
		o.facts = alertFacts{value: value, threshold: policy.threshold}
		if err = applyAlert(ctx, tx, o, now); err != nil {
			return err
		}
	}
	return nil
}

// Periodic evaluation preserves applicable open network incidents and clears
// interrupted streaks. It never re-counts or re-interprets cached results.
func networkAlertObservations(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule, now time.Time) ([]alertObservation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT plan_id FROM probe_alert_progress WHERE bad_count>0 OR good_count>0 UNION SELECT target_id FROM alert_state WHERE active=1 AND rule_key IN ('network_latency','network_loss','network_unreachable')`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []alertObservation{}
	for _, id := range ids {
		plan, err := readProbePlan(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			if _, err = tx.ExecContext(ctx, "UPDATE probe_alert_progress SET bad_count=0,good_count=0,bad_since=NULL WHERE plan_id=?", id); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		target, err := readTarget(ctx, tx, plan.TargetID)
		if errors.Is(err, ErrNotFound) {
			if _, err = tx.ExecContext(ctx, "UPDATE probe_alert_progress SET bad_count=0,good_count=0,bad_since=NULL WHERE plan_id=?", id); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		base, applicable, err := probeAlertContext(ctx, tx, plan, target)
		if err != nil {
			return nil, err
		}
		for _, key := range networkAlertKeys {
			p, err := readProbeAlertProgress(ctx, tx, key, id)
			if err != nil {
				return nil, err
			}
			if !applicable || !policyForProbe(rules[key], id).enabled || p.planRevision != plan.Revision || p.targetRevision != target.Revision || p.created != nil && now.Sub(*p.created) > time.Duration(plan.FrequencySeconds+75)*time.Second {
				if p.bad != 0 || p.good != 0 {
					p.clear()
					if err = storeProbeAlertProgress(ctx, tx, key, id, p, now); err != nil {
						return nil, err
					}
				}
			}
			if applicable && policyForProbe(rules[key], id).enabled {
				o := base
				o.rule = key
				out = append(out, o)
			}
		}
	}
	return out, nil
}

func closeProbePlanAlerts(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE target_id=? AND rule_key IN ('network_latency','network_loss','network_unreachable') AND active=1", timestamp(now), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE probe_alert_progress SET bad_count=0,good_count=0,bad_since=NULL WHERE plan_id=?", id); err != nil {
		return err
	}
	return cancelPendingNotifications(ctx, tx, "probe_plan_id=?", []any{id}, "probe_configuration_changed", now)
}
func sameProbeString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func sameProbePort(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func updateProbePlanAlerts(ctx context.Context, tx *sql.Tx, old, current model.ProbePlan, now time.Time) error {
	if !current.Enabled || old.PointKind != current.PointKind || !sameProbeString(old.InstanceID, current.InstanceID) || old.TargetID != current.TargetID || old.Family != current.Family || old.Method != current.Method {
		return closeProbePlanAlerts(ctx, tx, old.ID, now)
	}
	return nil
}
func updateProbeTargetAlerts(ctx context.Context, tx *sql.Tx, old, current model.NetworkTarget, now time.Time) error {
	all := !current.Enabled || old.Kind != current.Kind || !sameProbeString(old.Host, current.Host) || !sameProbeString(old.InstanceID, current.InstanceID)
	portChanged := !sameProbePort(old.TCPPort, current.TCPPort)
	if !all && !portChanged {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM probe_plan WHERE target_id=? AND (? OR json_extract(config_json,'$.method')='tcp')", old.ID, all)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = closeProbePlanAlerts(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

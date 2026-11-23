package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
	"slices"
	"sort"
	"strconv"
	"time"
)

type quotaEvaluation struct {
	decision model.QuotaDecision
	estimate model.TrafficEstimation
	official *int64
	key      string
}

func quotaSourceExecutable(r model.ProtectionRule, e quotaEvaluation) bool {
	if e.estimate.Qualified {
		return true
	}
	if !r.AllowUnverifiedExecution {
		return false
	}
	if e.estimate.Status == "ready" && e.estimate.Estimated != nil {
		return true
	}
	return e.official != nil && e.decision.Threshold != nil && *e.official >= *e.decision.Threshold
}

func evaluateQuota(ctx context.Context, tx *sql.Tx, r model.ProtectionRule, policy model.ProtectionPolicy, now time.Time) (quotaEvaluation, error) {
	d := model.QuotaDecision{RuleID: r.ID, RuleRevision: r.Revision, AccountID: r.AccountID, Scope: r.Scope, EvaluatedAt: now, State: "unknown", Reasons: []string{}, Targets: []model.ProtectionTarget{}}
	e := quotaEvaluation{decision: d}
	zone := policy.Timezone
	var configured string
	locationErr := tx.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='business_timezone'").Scan(&configured)
	if locationErr == nil {
		zone = configured
	} else if !errors.Is(locationErr, sql.ErrNoRows) {
		return e, locationErr
	}
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return e, err
	}
	start, _ := ReferenceCycle(now, loc)
	e.key = strconv.FormatInt(r.Revision, 10) + ":" + timestamp(start)
	a, err := loadAccount(ctx, tx, r.AccountID)
	if err != nil {
		return e, err
	}
	if !r.Enabled || !a.Enabled {
		e.decision.State = "inactive"
		e.decision.Reasons = append(e.decision.Reasons, "rule_or_account_disabled")
		return e, nil
	}
	settings, err := readEstimationSettings(ctx, tx)
	if err != nil {
		return e, err
	}
	estimateResult, official, quota, err := protectionSource(ctx, tx, r, a, settings, loc, now)
	if err != nil {
		return e, err
	}
	e.estimate = estimateResult
	e.decision.Reasons = append(e.decision.Reasons, e.estimate.Reasons...)
	threshold := r.Threshold
	if r.ThresholdKind == "percent" {
		if quota <= 0 {
			e.decision.Reasons = append(e.decision.Reasons, "quota_unknown")
			return e, nil
		}
		threshold, err = metering.CeilProduct(quota, r.Threshold, 100)
		if err != nil {
			return e, err
		}
	}
	e.decision.Threshold = &threshold
	sim, simErr := readQuotaSimulation(ctx, tx, r.AccountID, r.Scope, r.SourceInstanceID)
	if simErr != nil && !errors.Is(simErr, ErrNotFound) {
		return e, simErr
	}
	var estimate *int64 = e.estimate.Estimated
	if simErr == nil && sim.ExpiresAt.After(now) {
		estimate = &sim.Usage
		official = nil
		e.decision.Simulation = true
		e.decision.Reasons = append(e.decision.Reasons, "simulation")
		e.key += ":simulation:" + strconv.FormatInt(sim.Revision, 10)
	}
	if estimate == nil && official == nil {
		return e, nil
	}
	e.official = official
	value := int64(0)
	if official != nil {
		value = *official
	}
	if estimate != nil {
		value = max(value, *estimate)
	}
	rate := int64(0)
	if e.estimate.CurrentRate != nil {
		rate = *e.estimate.CurrentRate
	} else if r.ReserveSeconds > 0 && !e.decision.Simulation && value < threshold {
		e.decision.Reasons = append(e.decision.Reasons, "current_rate_unknown")
		return e, nil
	}
	if e.decision.Simulation && e.estimate.CurrentRate == nil {
		e.decision.Reasons = append(e.decision.Reasons, "simulation_rate_zero")
	}
	lead := int64(r.ReserveSeconds)
	if !e.decision.Simulation && e.estimate.Estimated != nil && now.After(e.estimate.To) {
		age := now.Sub(e.estimate.To)
		lead += int64((age + time.Second - 1) / time.Second)
		e.decision.Reasons = append(e.decision.Reasons, "sample_age_allowance")
	}
	triggered, evaluated, err := metering.Threshold(value, value, rate, lead, threshold)
	if err != nil {
		return e, err
	}
	e.decision.Value = &evaluated
	if triggered {
		e.decision.State = "triggered"
	} else if estimate != nil || e.decision.Simulation {
		e.decision.State = "below"
	}
	return e, nil
}
func protectionTargetOutcome(ctx context.Context, tx *sql.Tx, r model.ProtectionRule, id string, policy model.ProtectionPolicy, now time.Time) (string, error) {
	if !policy.SelfKnown {
		return "self_identity_unknown", nil
	}
	var quarantine string
	qerr := tx.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='protection_restore_until'").Scan(&quarantine)
	if qerr == nil {
		until, e := time.Parse(time.RFC3339Nano, quarantine)
		if e != nil {
			return "", e
		}
		if until.After(now) {
			return "restore_quarantine", nil
		}
	} else if !errors.Is(qerr, sql.ErrNoRows) {
		return "", qerr
	}

	if policy.SelfInstanceID == id && r.ConfirmedSelfID != id {
		return "self_excluded", nil
	}
	var exemption string
	err := tx.QueryRowContext(ctx, "SELECT expires_at FROM quota_exemption WHERE instance_id=?", id).Scan(&exemption)
	if err == nil {
		at, e := time.Parse(time.RFC3339Nano, exemption)
		if e != nil {
			return "", e
		}
		if at.After(now) {
			return "exempt", nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var status, region, kind, raw string
	err = tx.QueryRowContext(ctx, `SELECT i.status,i.region,i.kind,r.payload_json FROM instance i JOIN resource_snapshot r ON r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1 WHERE i.id=? AND i.account_id=?`, id, r.AccountID).Scan(&status, &region, &kind, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "target_unavailable", nil
	}
	if err != nil {
		return "", err
	}
	a, err := loadAccount(ctx, tx, r.AccountID)
	if err != nil {
		return "", err
	}
	if !a.Enabled || !slices.Contains(a.RegionIDs, region) {
		return "target_unavailable", nil
	}
	if status == "stopped" {
		return "already_stopped", nil
	}
	if status != "running" {
		return "target_unavailable", nil
	}
	if !model.ValidPowerAction(kind, "stop", r.StopMode) {
		return "target_unavailable", nil
	}
	if kind == "ecs" && r.StopMode == "stop_charging" {
		var facts struct {
			Charge string `json:"charge_type"`
		}
		if json.Unmarshal([]byte(raw), &facts) != nil || facts.Charge != "pay_as_you_go" && facts.Charge != "spot" {
			return "target_unavailable", nil
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM automatic_power_action WHERE instance_id=? AND julianday(dispatch_started_at)>julianday(?)", id, timestamp(now.Add(-10*time.Minute))).Scan(&count); err != nil {
		return "", err
	}
	if count >= 3 {
		return "operation_limited", nil
	}
	return "", nil
}

func quotaTargetDecision(ctx context.Context, tx *sql.Tx, r model.ProtectionRule, id string, evaluation quotaEvaluation, policy model.ProtectionPolicy, generation int64, now time.Time) (model.ProtectionTarget, error) {
	outcome, err := protectionTargetOutcome(ctx, tx, r, id, policy, now)
	target := model.ProtectionTarget{InstanceID: id, Outcome: outcome}
	if err != nil || outcome != "" {
		return target, err
	}
	switch {
	case evaluation.decision.Simulation || r.DryRun:
		target.Outcome = "would_stop"
	case !policy.PowerEnabled:
		target.Outcome = "power_disabled"
	case !quotaSourceExecutable(r, evaluation):
		target.Outcome = "source_unqualified"
	default:
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", r.ID, evaluation.key, generation, id)))
		j, e := enqueuePowerJobTx(model.WithJobTrigger(ctx, "schedule"), tx, id, "stop", r.StopMode, hex.EncodeToString(sum[:]), "quota:"+r.ID, now)
		if errors.Is(e, ErrPowerBusy) {
			target.Outcome = "busy"
		} else if e != nil {
			return target, e
		} else {
			if _, e = tx.ExecContext(ctx, "INSERT INTO quota_power_job(job_id,rule_id,rule_revision,evaluation_key,generation) VALUES(?,?,?,?,?)", j.ID, r.ID, r.Revision, evaluation.key, generation); e != nil {
				return target, e
			}
			target.Outcome = "queued"
			target.JobID = &j.ID
		}
	}
	return target, nil
}

func (s *Store) EvaluateQuotaProtection(ctx context.Context, policy model.ProtectionPolicy, now time.Time) error {
	rules, err := s.ProtectionRules(ctx)
	if err != nil {
		return err
	}
	var firstError error
	for _, rule := range rules {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = func() error {
			defer tx.Rollback()
			r, e := readProtectionRule(ctx, tx, rule.ID)
			if e != nil {
				return e
			}
			evaluation, e := evaluateQuota(ctx, tx, r, policy, now)
			if e != nil {
				return e
			}
			d := evaluation.decision
			var key, signature, oldRaw string
			var active bool
			var generation int64
			e = tx.QueryRowContext(ctx, "SELECT evaluation_key,active,generation,signature,last_result_json FROM quota_rule_state WHERE rule_id=?", r.ID).Scan(&key, &active, &generation, &signature, &oldRaw)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			same := key == evaluation.key
			if !same {
				active = false
				generation = 0
				signature = ""
			}
			if d.State == "triggered" && !active {
				generation++
				active = true
				if r.Action == "stop_notify" {
					for _, id := range r.Targets {
						target, e := quotaTargetDecision(ctx, tx, r, id, evaluation, policy, generation, now)
						if e != nil {
							return e
						}
						d.Targets = append(d.Targets, target)
					}
				}
				// One incident notification. No network I/O.
				kind := "alert"
				key := "quota_protection"
				account := r.AccountID
				rehearsal := d.Simulation || r.DryRun || !quotaSourceExecutable(r, evaluation) || !policy.PowerEnabled
				title, body := quotaProtectionCopy(ctx, tx, r, d, rehearsal)
				if _, e = enqueueNotification(ctx, tx, model.Notification{EventKey: fmt.Sprintf("quota:%s:%s:%d:alert", r.ID, evaluation.key, generation), Kind: kind, Severity: "critical", RuleKey: &key, Title: title, Body: body, AccountID: &account, Urgent: true}, now); e != nil {
					return e
				}
			} else if d.State == "triggered" && active && same {
				// Preserve the terminal decision for this incident. A later flag change
				// cannot replay a rehearsal or turn a disabled action into a cloud job.
				var old model.QuotaDecision
				if json.Unmarshal([]byte(oldRaw), &old) == nil {
					for _, target := range old.Targets {
						switch target.Outcome {
						case "exempt", "busy", "operation_limited", "target_unavailable", "self_identity_unknown", "restore_quarantine":
							target, e = quotaTargetDecision(ctx, tx, r, target.InstanceID, evaluation, policy, generation, now)
							if e != nil {
								return e
							}
						}
						d.Targets = append(d.Targets, target)
					}
				}
			} else if d.State == "below" && active {
				active = false
				key := "quota_protection"
				account := r.AccountID
				if _, e = enqueueNotification(ctx, tx, model.Notification{EventKey: fmt.Sprintf("quota:%s:%s:%d:recovery", r.ID, evaluation.key, generation), Kind: "recovery", Severity: "info", RuleKey: &key, Title: severitySymbol("recovery", "info") + " " + accountCopyName(ctx, tx, account) + " 的" + copyScope(r.Scope) + "流量回到保护线以下：" + r.Name, Body: "", AccountID: &account, Urgent: true}, now); e != nil {
					return e
				}
			}
			// Unknown data retains the active incident and never invents recovery.
			if d.State == "unknown" && active && same {
				var old model.QuotaDecision
				if json.Unmarshal([]byte(oldRaw), &old) == nil {
					d.Targets = old.Targets
				}
			}
			raw, e := json.Marshal(d)
			if e != nil {
				return e
			}
			codes := []string{}
			for _, t := range d.Targets {
				codes = append(codes, t.InstanceID+":"+t.Outcome)
			}
			sort.Strings(codes)
			sigRaw, _ := json.Marshal([]any{evaluation.key, d.State, d.Reasons, codes})
			newSignature := string(sigRaw)
			if newSignature != signature {
				res, e := tx.ExecContext(ctx, "INSERT INTO quota_decision(rule_id,evaluated_at,payload_json) VALUES(?,?,?)", r.ID, timestamp(now), string(raw))
				if e != nil {
					return e
				}
				d.ID, _ = res.LastInsertId()
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO quota_rule_state VALUES(?,?,?,?,?,?) ON CONFLICT(rule_id) DO UPDATE SET evaluation_key=excluded.evaluation_key,active=excluded.active,generation=excluded.generation,signature=excluded.signature,last_result_json=excluded.last_result_json`, r.ID, evaluation.key, active, generation, newSignature, string(raw)); e != nil {
				return e
			}
			return tx.Commit()
		}()
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	if err = s.finishQuotaNotifications(ctx, now); err != nil {
		return err
	}
	return firstError
}
func (s *Store) QuotaDecisions(ctx context.Context, rule string, before int64, limit int) ([]model.QuotaDecision, bool, error) {
	if limit < 1 || limit > 200 || before < 0 {
		return nil, false, ErrProtectionRule
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id,payload_json FROM quota_decision WHERE (?='' OR rule_id=?) AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?", rule, rule, before, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []model.QuotaDecision{}
	for rows.Next() {
		var id int64
		var raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, false, err
		}
		var d model.QuotaDecision
		if err = json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, false, err
		}
		d.ID = id
		out = append(out, d)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, rows.Err()
}
func (s *Store) finishQuotaNotifications(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT j.id,j.status,i.account_id,i.id FROM quota_power_job q JOIN job j ON j.id=q.job_id JOIN instance i ON i.id=j.instance_id WHERE q.final_notified=0 AND j.status IN ('succeeded','failed')")
	if err != nil {
		return err
	}
	type item struct{ job, status, account, instance string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.job, &v.status, &v.account, &v.instance); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		key := "quota_protection"
		if _, err = enqueueNotification(ctx, tx, model.Notification{EventKey: "quota-job:" + v.job + ":finished", JobID: v.job, Kind: "alert", Severity: quotaPowerSeverity(v.status), RuleKey: &key, Title: quotaPowerCopy(ctx, tx, v.instance, v.status), Body: "", AccountID: &v.account, InstanceID: &v.instance, Urgent: true}, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE quota_power_job SET final_notified=1 WHERE job_id=?", v.job); err != nil {
			return err
		}
	}
	return tx.Commit()
}

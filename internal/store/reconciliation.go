package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
	"strconv"
	"time"
)

type reconciliationProgress struct {
	credential, revision, lastID, episode int64
	cycle                                 string
	cutoff                                *time.Time
	bad, healthy                          int
	active                                bool
}

func reconcileObservation(ctx context.Context, tx *sql.Tx, a model.Account, scope string, o model.CDTObservation, settings model.EstimationSettings, loc *time.Location, now time.Time, maximumCutoff *time.Time) (model.CDTReconciliation, error) {
	v := model.CDTReconciliation{AccountID: a.ID, Scope: scope, ObservationID: o.ID, CoveredUntil: o.CoveredUntil, CheckedAt: now, Status: "not_comparable", Reason: "official_basis_unverified"}
	v.Official = o.Mainland
	if scope == "overseas" {
		v.Official = o.Overseas
	}
	start, end := ReferenceCycle(now, loc)
	if o.Error != nil {
		v.Reason = "official_query_failed"
		return v, nil
	}
	if v.Official == nil || o.Quality != "verified" || o.CoveredUntil == nil || o.PeriodStart == nil || o.PeriodEnd == nil || !o.PeriodStart.Equal(start) || !o.PeriodEnd.Equal(end) || o.CoveredUntil.After(now) || o.CoveredUntil.After(o.FetchedAt) {
		return v, nil
	}
	if maximumCutoff != nil && !o.CoveredUntil.After(*maximumCutoff) {
		if o.CoveredUntil.Equal(*maximumCutoff) {
			v.Status = "duplicate_cutoff"
			v.Reason = "cutoff_not_advanced"
		} else {
			v.Reason = "cutoff_regressed"
		}
		return v, nil
	}
	column := "mainland_bytes"
	if scope == "overseas" {
		column = "overseas_bytes"
	}
	previous, err := readObservation(tx.QueryRowContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND id<? AND error_code IS NULL AND source_quality='verified' AND "+column+" IS NOT NULL AND period_start=? AND period_end=? AND covered_until<? ORDER BY covered_until DESC,id DESC LIMIT 1", a.ID, a.CredentialVersion, o.ID, timestamp(start), timestamp(end), timestamp(*o.CoveredUntil)))
	if errors.Is(err, sql.ErrNoRows) {
		v.Reason = "previous_trusted_basis_missing"
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.PreviousID = &previous.ID
	predicted, err := calculateEstimateAt(ctx, tx, a, scope, settings, loc, now, &previous, *o.CoveredUntil, true)
	if err != nil {
		return v, err
	}
	if predicted.Status != "ready" || !predicted.Qualified || predicted.Estimated == nil || !predicted.To.Equal(*o.CoveredUntil) {
		v.Reason = "metering_coverage_or_membership_unverified"
		return v, nil
	}
	if len(predicted.Reasons) != 0 || predicted.WorstCase == nil || *predicted.WorstCase != 0 {
		v.Reason = "metering_interval_not_exact"
		return v, nil
	}
	ratio, bad, err := metering.Deviation(*predicted.Estimated, *v.Official)
	if err != nil {
		return v, err
	}
	v.Status = "comparable"
	v.Reason = ""
	v.Predicted = predicted.Estimated
	v.RelativeError = ratio
	v.Exceeds = &bad
	return v, nil
}

// Called within the estimator's coherent account transaction. Only actual
// fully covered intervals at the NEW official cutoff can be compared.
func reconcileCDT(ctx context.Context, tx *sql.Tx, a model.Account, scope string, settings model.EstimationSettings, loc *time.Location, now time.Time) error {
	cycle, cycleEnd := ReferenceCycle(now, loc)
	progress := reconciliationProgress{credential: a.CredentialVersion, revision: a.Revision, cycle: timestamp(cycle)}
	var old reconciliationProgress
	var cutoff sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT credential_version,account_revision,cycle_start,last_observation_id,max_cutoff,bad_streak,healthy_streak,active,episode FROM cdt_reconciliation_state WHERE account_id=? AND scope=?", a.ID, scope).Scan(&old.credential, &old.revision, &old.cycle, &old.lastID, &cutoff, &old.bad, &old.healthy, &old.active, &old.episode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		progress.lastID = old.lastID
	}
	if err == nil && old.credential == a.CredentialVersion && old.revision == a.Revision && old.cycle == progress.cycle {
		old.cutoff = parseAgentTime(cutoff)
		progress = old
	}
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='cdt_estimation_deviation'"))
	if err != nil {
		return err
	}
	recoverySamples := 0
	if rule.Params.RecoverySamples != nil {
		recoverySamples = *rule.Params.RecoverySamples
	}
	if !rule.Enabled {
		progress.bad = 0
		progress.healthy = 0
		progress.active = false
	}
	wasActive := progress.active
	rows, err := tx.QueryContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND id>? AND fetched_at>=? AND fetched_at<=? ORDER BY id LIMIT 32", a.ID, a.CredentialVersion, progress.lastID, timestamp(cycle), timestamp(now))
	if err != nil {
		return err
	}
	observations := []model.CDTObservation{}
	for rows.Next() {
		o, e := readObservation(rows)
		if e != nil {
			rows.Close()
			return e
		}
		observations = append(observations, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range observations {
		comparison, e := reconcileObservation(ctx, tx, a, scope, o, settings, loc, now, progress.cutoff)
		if e != nil {
			return e
		}
		if !rule.Enabled {
			comparison.Status = "not_comparable"
			comparison.Reason = "rule_disabled"
			comparison.Predicted = nil
			comparison.Exceeds = nil
			comparison.RelativeError = nil
		}
		if comparison.Status == "comparable" && comparison.Exceeds != nil {
			if *comparison.Exceeds {
				progress.bad++
				progress.healthy = 0
				if progress.bad >= *rule.Params.ConsecutiveFailures && !progress.active {
					progress.active = true
					progress.episode++
				}
			} else {
				progress.bad = 0
				progress.healthy++
				if progress.healthy >= recoverySamples {
					progress.active = false
				}
			}
		} else if comparison.Status != "duplicate_cutoff" {
			progress.bad = 0
			progress.healthy = 0
		}
		// Only a trusted current-cycle watermark advances the maximum, including
		// the first basis. Inferred records can neither move it nor become zero.
		if o.Quality == "verified" && o.CoveredUntil != nil && o.PeriodStart != nil && o.PeriodEnd != nil && o.PeriodStart.Equal(cycle) && o.PeriodEnd.Equal(cycleEnd) && !o.CoveredUntil.After(now) && !o.CoveredUntil.After(o.FetchedAt) && (progress.cutoff == nil || o.CoveredUntil.After(*progress.cutoff)) {
			progress.cutoff = o.CoveredUntil
		}
		progress.lastID, _ = strconv.ParseInt(o.ID, 10, 64)
		comparison.BadStreak = progress.bad
		comparison.HealthyStreak = progress.healthy
		comparison.AlertActive = progress.active
		raw, e := json.Marshal(comparison)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO cdt_reconciliation(account_id,scope,credential_version,account_revision,cycle_start,observation_id,payload_json) VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,scope,observation_id) DO NOTHING", a.ID, scope, a.CredentialVersion, a.Revision, progress.cycle, o.ID, string(raw)); e != nil {
			return e
		}
	}
	if rule.Enabled && wasActive != progress.active {
		kind := "alert"
		severity := "warning"
		title := severitySymbol(kind, severity) + " " + accountCopyName(ctx, tx, a.ID) + " 的" + copyScope(scope) + "实时估算和官方数值相差较大"
		body := "连续几次对账偏差超过 10%，估算可能不准，请留意官方数值。"
		if !progress.active {
			kind = "recovery"
			severity = "info"
			title = severitySymbol(kind, severity) + " " + accountCopyName(ctx, tx, a.ID) + " 的" + copyScope(scope) + "实时估算恢复准确"
			body = ""
		}
		key := "cdt_estimation_deviation"
		if _, err = enqueueNotification(ctx, tx, model.Notification{EventKey: fmt.Sprintf("reconcile:%s:%s:%d:%d:%s:%d:%s", a.ID, scope, a.CredentialVersion, a.Revision, progress.cycle, progress.episode, kind), Kind: kind, Severity: severity, RuleKey: &key, AccountID: &a.ID, Title: title, Body: body, Urgent: true}, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cdt_reconciliation_state(account_id,scope,credential_version,account_revision,cycle_start,last_observation_id,max_cutoff,bad_streak,healthy_streak,active,episode) VALUES(?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(account_id,scope) DO UPDATE SET credential_version=excluded.credential_version,account_revision=excluded.account_revision,cycle_start=excluded.cycle_start,last_observation_id=excluded.last_observation_id,max_cutoff=excluded.max_cutoff,bad_streak=excluded.bad_streak,healthy_streak=excluded.healthy_streak,active=excluded.active,episode=excluded.episode`, a.ID, scope, progress.credential, progress.revision, progress.cycle, progress.lastID, nullableMeteringTime(progress.cutoff), progress.bad, progress.healthy, progress.active, progress.episode)
	return err
}
func (s *Store) Reconciliations(ctx context.Context, account, scope string, before int64, limit int) ([]model.CDTReconciliation, bool, error) {
	if limit < 1 || limit > 200 || before < 0 || scope != "" && scope != "mainland" && scope != "overseas" {
		return nil, false, ErrProtectionRule
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id,payload_json FROM cdt_reconciliation WHERE (?='' OR account_id=?) AND (?='' OR scope=?) AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?", account, account, scope, scope, before, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []model.CDTReconciliation{}
	for rows.Next() {
		var raw string
		var id int64
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, false, err
		}
		var v model.CDTReconciliation
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, false, err
		}
		v.ID = id
		out = append(out, v)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, rows.Err()
}
func (s *Store) latestReconciliation(ctx context.Context, a model.Account, scope string, cycle time.Time) (*model.CDTReconciliation, error) {
	if !a.Enabled {
		return nil, nil
	}
	var id int64
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT id,payload_json FROM cdt_reconciliation WHERE account_id=? AND scope=? AND credential_version=? AND account_revision=? AND cycle_start=? ORDER BY id DESC LIMIT 1", a.ID, scope, a.CredentialVersion, a.Revision, timestamp(cycle)).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v model.CDTReconciliation
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	v.ID = id
	err = s.DB.QueryRowContext(ctx, "SELECT bad_streak,healthy_streak,active FROM cdt_reconciliation_state WHERE account_id=? AND scope=? AND credential_version=? AND account_revision=? AND cycle_start=?", a.ID, scope, a.CredentialVersion, a.Revision, timestamp(cycle)).Scan(&v.BadStreak, &v.HealthyStreak, &v.AlertActive)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

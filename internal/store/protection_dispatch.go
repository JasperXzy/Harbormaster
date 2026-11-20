package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/JasperXzy/harbormaster/internal/model"
	"time"
)

// Runs inside the same transaction that writes the send marker. Never trust a
// cached estimate or a decision made while a job waited in the queue.
func authorizeQuotaDispatch(ctx context.Context, tx *sql.Tx, p PowerOperation, policy model.ProtectionPolicy, now time.Time) error {
	var rule, key string
	var revision, generation int64
	err := tx.QueryRowContext(ctx, "SELECT rule_id,rule_revision,evaluation_key,generation FROM quota_power_job WHERE job_id=?", p.Job.ID).Scan(&rule, &revision, &key, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !policy.PowerEnabled || !policy.SelfKnown || p.Action != "stop" {
		return ErrProtectionGuard
	}
	var quarantine string
	err = tx.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='protection_restore_until'").Scan(&quarantine)
	if err == nil {
		until, e := time.Parse(time.RFC3339Nano, quarantine)
		if e != nil {
			return e
		}
		if until.After(now) {
			return ErrProtectionGuard
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	r, err := readProtectionRule(ctx, tx, rule)
	if errors.Is(err, ErrNotFound) {
		return ErrProtectionGuard
	}
	if err != nil {
		return err
	}
	if r.Revision != revision || !r.Enabled || r.DryRun || r.Action != "stop_notify" {
		return ErrProtectionGuard
	}
	evaluation, err := evaluateQuota(ctx, tx, r, policy, now)
	if err != nil {
		return err
	}
	if evaluation.key != key || evaluation.decision.Simulation || evaluation.decision.State != "triggered" || !quotaSourceExecutable(r, evaluation) {
		return ErrProtectionGuard
	}
	var active bool
	var stateKey string
	var stateGeneration int64
	err = tx.QueryRowContext(ctx, "SELECT active,evaluation_key,generation FROM quota_rule_state WHERE rule_id=?", rule).Scan(&active, &stateKey, &stateGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProtectionGuard
	}
	if err != nil {
		return err
	}
	if !active || key != stateKey || generation != stateGeneration {
		return ErrProtectionGuard
	}
	outcome, err := protectionTargetOutcome(ctx, tx, r, p.InstanceID, policy, now)
	if err != nil {
		return err
	}
	if outcome != "" {
		return ErrProtectionGuard
	}
	var newerIntent int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance_intent WHERE instance_id=? AND julianday(updated_at)>julianday(?)", p.InstanceID, timestamp(p.Job.CreatedAt)).Scan(&newerIntent); err != nil {
		return err
	}
	if newerIntent != 0 {
		return ErrProtectionGuard
	}
	// Reserve the real-operation budget at the once-only dispatch boundary. Even
	// rejected or uncertain requests consume a slot; rehearsal/queueing does not.
	if _, err = tx.ExecContext(ctx, "INSERT INTO automatic_power_action(job_id,instance_id,dispatch_started_at) VALUES(?,?,?)", p.Job.ID, p.InstanceID, timestamp(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance SET desired_state='quota_stopped' WHERE id=?", p.InstanceID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO instance_intent(instance_id,job_id,updated_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET job_id=excluded.job_id,updated_at=excluded.updated_at", p.InstanceID, p.Job.ID, timestamp(now))
	return err
}

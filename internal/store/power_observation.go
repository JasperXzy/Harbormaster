package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// PowerCredentials loads the account and encrypted credential under one
// transaction, fenced by the queued revision and the current job lease.
func (s *Store) PowerCredentials(ctx context.Context, j model.Job, now time.Time) (model.Account, []byte, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Account{}, nil, err
	}
	defer tx.Rollback()
	p, err := powerLease(ctx, tx, j, now)
	if err != nil {
		return model.Account{}, nil, err
	}
	a, err := loadAccount(ctx, tx, p.AccountID)
	if err != nil {
		return a, nil, err
	}
	if !a.Enabled {
		return a, nil, ErrAccountDisabled
	}
	if a.Revision != p.AccountRevision {
		return a, nil, ErrAccountChanged
	}
	var cipher []byte
	err = tx.QueryRowContext(ctx, "SELECT ciphertext FROM credential WHERE owner_field=?", model.CredentialOwner(a.ID)).Scan(&cipher)
	if err != nil {
		return a, nil, err
	}
	return a, cipher, tx.Commit()
}

func (s *Store) SavePowerBaseline(ctx context.Context, j model.Job, o model.PowerObservation, now time.Time) error {
	if !o.Valid() || o.ObservedAt.After(now) {
		return ErrPowerParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerLease(ctx, tx, j, now)
	if err != nil {
		return err
	}
	if p.DispatchStartedAt != nil {
		return ErrPowerDispatched
	}
	if j.StartedAt == nil || o.ObservedAt.Before(*j.StartedAt) {
		return ErrPowerParameters
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance_power_job SET baseline_json=? WHERE job_id=?", string(raw), j.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+powerResultSQL+" WHERE id=?", j.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailPowerJob is restricted to local preflight failures or interrupted/expired
// confirmation. Dispatch uncertainty itself goes through RecordPowerDispatch.
func (s *Store) FailPowerJob(ctx context.Context, j model.Job, code string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerLease(ctx, tx, j, now)
	if err != nil {
		return err
	}
	allowed := []string{"power_preflight_failed", "credential_decryption_failed", "account_changed", "account_disabled", "instance_state_conflict", "instance_not_found", "cancelled"}
	if p.DispatchOutcome == "accepted" {
		allowed = []string{"power_confirmation_timeout", "power_confirmation_interrupted"}
	} else if p.DispatchStartedAt != nil {
		return ErrPowerDispatched
	}
	if !slices.Contains(allowed, code) {
		return ErrPowerParameters
	}
	_, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code=?,finished_at=?,result_json="+powerResultSQL+",lease_owner=NULL,lease_until=0 WHERE id=?", code, timestamp(now), j.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ObservePower derives completion from persisted preflight and post-dispatch
// evidence. A worker cannot mark success merely by receiving API acceptance.
func (s *Store) ObservePower(ctx context.Context, j model.Job, o model.PowerObservation, now time.Time) (bool, error) {
	if !o.Valid() || o.ObservedAt.After(now) {
		return false, ErrPowerParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	p, err := powerLease(ctx, tx, j, now)
	if err != nil {
		return false, err
	}
	if p.DispatchOutcome != "accepted" || p.DispatchStartedAt == nil {
		return false, ErrPowerDispatched
	}
	if o.ObservedAt.Before(*p.DispatchStartedAt) {
		return false, ErrPowerParameters
	}
	var baselineRaw, previousRaw sql.NullString
	var transition bool
	err = tx.QueryRowContext(ctx, "SELECT baseline_json,observation_json,saw_transition FROM instance_power_job WHERE job_id=?", j.ID).Scan(&baselineRaw, &previousRaw, &transition)
	if err != nil {
		return false, err
	}
	var baseline, previous model.PowerObservation
	if !baselineRaw.Valid || json.Unmarshal([]byte(baselineRaw.String), &baseline) != nil || !baseline.Valid() {
		return false, ErrPowerParameters
	}
	if previousRaw.Valid {
		if json.Unmarshal([]byte(previousRaw.String), &previous) != nil {
			return false, errors.New("invalid_power_observation")
		}
		if !o.ObservedAt.After(previous.ObservedAt) {
			return false, ErrPowerParameters
		}
	}
	if p.Action == "reboot" && (o.Status == "starting" || o.Status == "stopping" || o.Status == "stopped") {
		transition = true
	}
	basis, code := "", ""
	switch p.Action {
	case "start":
		if o.Status == "running" {
			basis = "target_state"
		}
	case "stop":
		if o.Status == "stopped" {
			if p.Kind == "swas" {
				basis = "target_state"
			} else if o.StoppedMode != nil {
				if *o.StoppedMode == p.StopMode || p.StopMode == "keep_charging" && *o.StoppedMode == "not_applicable" {
					basis = "target_state"
				} else {
					code = "power_stop_mode_mismatch"
				}
			}
		}
	case "reboot":
		if o.Status == "running" {
			if transition {
				basis = "state_transition"
			} else if baseline.StartedAt != nil && o.StartedAt != nil && o.StartedAt.After(*baseline.StartedAt) && !o.StartedAt.Before(p.DispatchStartedAt.Truncate(time.Minute)) {
				basis = "start_time_changed"
			}
		}
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return false, err
	}
	var basisValue any
	if basis != "" {
		basisValue = basis
	}
	_, err = tx.ExecContext(ctx, "UPDATE instance_power_job SET observation_json=?,saw_transition=?,confirmation_basis=? WHERE job_id=?", string(raw), transition, basisValue, j.ID)
	if err != nil {
		return false, err
	}
	done := basis != "" || code != ""
	if done {
		status := "succeeded"
		var errorValue any
		if code != "" {
			status = "failed"
			errorValue = code
		}
		_, err = tx.ExecContext(ctx, "UPDATE job SET status=?,error_code=?,finished_at=?,result_json="+powerResultSQL+",lease_owner=NULL,lease_until=0 WHERE id=?", status, errorValue, timestamp(now), j.ID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+powerResultSQL+" WHERE id=?", j.ID)
	}
	if err != nil {
		return false, err
	}
	return done, tx.Commit()
}

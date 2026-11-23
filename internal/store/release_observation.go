package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func releaseAccount(ctx context.Context, tx *sql.Tx, p ReleaseOperation) (model.Account, error) {
	a, err := loadAccount(ctx, tx, p.AccountID)
	if err != nil {
		return a, err
	}
	if !a.Enabled {
		return a, ErrAccountDisabled
	}
	if a.Revision != p.AccountRevision || a.CredentialVersion != p.CredentialVersion || !slices.Contains(a.RegionIDs, p.Region) {
		return a, ErrAccountChanged
	}
	return a, nil
}

func (s *Store) ReleaseCredentials(ctx context.Context, j model.Job, now time.Time) (model.Account, []byte, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Account{}, nil, err
	}
	defer tx.Rollback()
	p, err := releaseLease(ctx, tx, j, now)
	if err != nil {
		return model.Account{}, nil, err
	}
	a, err := releaseAccount(ctx, tx, p)
	if err != nil {
		return a, nil, err
	}
	var cipher []byte
	if err := tx.QueryRowContext(ctx, "SELECT ciphertext FROM credential WHERE owner_field=?", model.CredentialOwner(a.ID)).Scan(&cipher); err != nil {
		return a, nil, err
	}
	return a, cipher, tx.Commit()
}

func (s *Store) FailReleaseJob(ctx context.Context, j model.Job, code string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := releaseLease(ctx, tx, j, now)
	if err != nil {
		return err
	}
	allowed := []string{"release_preflight_failed", "credential_decryption_failed", "account_changed", "account_disabled", "instance_not_found", "cancelled"}
	if p.DispatchOutcome == "accepted" {
		allowed = []string{"release_confirmation_timeout", "release_confirmation_interrupted"}
	} else if p.DispatchStartedAt != nil {
		return ErrReleaseDispatched
	}
	if !slices.Contains(allowed, code) {
		return ErrReleaseParameters
	}
	if _, err := tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code=?,finished_at=?,result_json="+releaseResultSQL+",lease_owner=NULL,lease_until=0 WHERE id=?", code, timestamp(now), j.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// ObserveRelease only accepts evidence after accepted dispatch. A successful
// job means the targeted query no longer returned the instance, not that the
// provider proved every associated resource was physically removed.
func (s *Store) ObserveRelease(ctx context.Context, j model.Job, o model.ReleaseObservation, now time.Time) (bool, error) {
	if o.ObservedAt.IsZero() || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > 30*time.Second {
		return false, ErrReleaseParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	p, err := releaseLease(ctx, tx, j, now)
	if err != nil {
		return false, err
	}
	if p.DispatchOutcome != "accepted" || p.DispatchStartedAt == nil {
		return false, ErrReleaseDispatched
	}
	if o.ObservedAt.Before(*p.DispatchStartedAt) {
		return false, ErrReleaseParameters
	}
	if _, err := releaseAccount(ctx, tx, p); err != nil {
		return false, err
	}
	var previousRaw sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT observation_json FROM instance_release_job WHERE job_id=?", j.ID).Scan(&previousRaw); err != nil {
		return false, err
	}
	if previousRaw.Valid {
		var previous model.ReleaseObservation
		if json.Unmarshal([]byte(previousRaw.String), &previous) != nil || !o.ObservedAt.After(previous.ObservedAt) {
			return false, ErrReleaseParameters
		}
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return false, err
	}
	var basis any
	if !o.InstancePresent {
		basis = "not_found_after_accept"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE instance_release_job SET observation_json=?,confirmation_basis=? WHERE job_id=?", string(raw), basis, j.ID); err != nil {
		return false, err
	}
	if o.InstancePresent {
		_, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+releaseResultSQL+" WHERE id=?", j.ID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE job SET status='succeeded',error_code=NULL,finished_at=?,result_json="+releaseResultSQL+",lease_owner=NULL,lease_until=0 WHERE id=?", timestamp(now), j.ID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE resource_snapshot SET present=0 WHERE id=? AND account_id=? AND kind='ecs' AND region=?`, p.InstanceID, p.AccountID, p.Region)
		}
	}
	if err != nil {
		return false, err
	}
	return !o.InstancePresent, tx.Commit()
}

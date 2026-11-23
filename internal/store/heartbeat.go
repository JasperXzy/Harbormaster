package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrHeartbeatChanged = errors.New("heartbeat_configuration_changed")
var ErrHeartbeatNotConfigured = errors.New("heartbeat_not_configured")
var ErrHeartbeatInvalid = errors.New("heartbeat_configuration_invalid")

type heartbeatReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readHeartbeat(ctx context.Context, q heartbeatReader) ([]byte, model.HeartbeatSettings, error) {
	var state model.HeartbeatSettings
	var cipher []byte
	var updated, attempt, success, code sql.NullString
	err := q.QueryRowContext(ctx, `SELECT h.enabled,h.interval_seconds,h.revision,h.updated_at,h.last_attempt_at,h.last_success_at,h.last_error_code,c.ciphertext
 FROM heartbeat_settings h LEFT JOIN credential c ON c.owner_field=? WHERE h.id=1`, model.HeartbeatCredentialOwner).Scan(&state.Enabled, &state.IntervalSeconds, &state.Revision, &updated, &attempt, &success, &code, &cipher)
	if err != nil {
		return nil, state, err
	}
	state.URLConfigured = len(cipher) > 0
	state.UpdatedAt = parseAgentTime(updated)
	state.LastAttemptAt = parseAgentTime(attempt)
	state.LastSuccessAt = parseAgentTime(success)
	if code.Valid {
		state.LastError = model.Error(code.String)
	}
	return cipher, state, nil
}
func (s *Store) HeartbeatSettings(ctx context.Context) ([]byte, model.HeartbeatSettings, error) {
	return readHeartbeat(ctx, s.DB)
}

// A nil replacement preserves the existing encrypted URL; a pointer to an empty
// byte slice removes it. Only ciphertext enters the store.
func (s *Store) SaveHeartbeat(ctx context.Context, expected int64, enabled bool, interval int, replacement *[]byte, now time.Time) (model.HeartbeatSettings, error) {
	var result model.HeartbeatSettings
	if expected < 0 || interval < 15 || interval > 86400 {
		return result, ErrHeartbeatInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	cipher, current, err := readHeartbeat(ctx, tx)
	if err != nil {
		return result, err
	}
	if current.Revision != expected {
		return result, ErrHeartbeatChanged
	}
	if replacement != nil {
		cipher = *replacement
	}
	if enabled && len(cipher) == 0 {
		return result, ErrHeartbeatNotConfigured
	}
	if replacement != nil {
		if len(cipher) == 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM credential WHERE owner_field=?", model.HeartbeatCredentialOwner)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO credential(owner_field,ciphertext,updated_at) VALUES(?,?,?) ON CONFLICT(owner_field) DO UPDATE SET ciphertext=excluded.ciphertext,updated_at=excluded.updated_at`, model.HeartbeatCredentialOwner, cipher, timestamp(now))
		}
		if err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='heartbeat_configuration_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE type='heartbeat_test' AND status IN ('queued','running')`, timestamp(now)); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE heartbeat_settings SET enabled=?,interval_seconds=?,revision=revision+1,updated_at=?,last_attempt_at=NULL,last_success_at=NULL,last_error_code=NULL,next_due_at=0,lease_owner=NULL,lease_until=0 WHERE id=1`, enabled, interval, timestamp(now)); err != nil {
		return result, err
	}
	at := now.UTC()
	result = model.HeartbeatSettings{Enabled: enabled, URLConfigured: len(cipher) > 0, IntervalSeconds: interval, Revision: current.Revision + 1, UpdatedAt: &at}
	return result, tx.Commit()
}
func (s *Store) EnqueueHeartbeatTest(ctx context.Context, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	_, state, err := readHeartbeat(ctx, tx)
	if err != nil {
		return model.Job{}, err
	}
	if !state.URLConfigured {
		return model.Job{}, ErrHeartbeatNotConfigured
	}
	j, err := enqueueJob(ctx, tx, "heartbeat_test", "heartbeat", nil, now)
	if err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO heartbeat_test(job_id,revision) VALUES(?,?) ON CONFLICT(job_id) DO NOTHING", j.ID, state.Revision); err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) ClaimHeartbeat(ctx context.Context, now time.Time) (model.HeartbeatClaim, error) {
	var claim model.HeartbeatClaim
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return claim, err
	}
	defer tx.Rollback()
	// An interrupted manual test is never replayed as a new successful ping.
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='heartbeat_test_interrupted',finished_at=?,lease_owner=NULL,lease_until=0 WHERE type='heartbeat_test' AND status='running' AND lease_until<=?`, timestamp(now), now.Unix()); err != nil {
		return claim, err
	}
	cipher, state, err := readHeartbeat(ctx, tx)
	if err != nil {
		return claim, err
	}
	var due, lease int64
	if err = tx.QueryRowContext(ctx, "SELECT next_due_at,lease_until FROM heartbeat_settings WHERE id=1").Scan(&due, &lease); err != nil {
		return claim, err
	}
	notFound := func() (model.HeartbeatClaim, error) {
		if err := tx.Commit(); err != nil {
			return claim, err
		}
		return claim, ErrNotFound
	}
	if !state.URLConfigured || lease > now.Unix() {
		return notFound()
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE type='heartbeat_test' AND status='queued' AND id IN (SELECT job_id FROM heartbeat_test WHERE revision=?) ORDER BY created_at,id LIMIT 1", state.Revision))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return claim, err
	}
	if errors.Is(err, ErrNotFound) && (!state.Enabled || due > now.Unix()) {
		return notFound()
	}
	owner := model.NewID("heartbeat_")
	expires := now.Add(30 * time.Second)
	claim = model.HeartbeatClaim{Settings: state, Ciphertext: cipher, Owner: owner, ExpiresAt: expires}
	if err == nil {
		if _, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=?,lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?", timestamp(now), owner, expires.Unix(), j.ID); err != nil {
			return claim, err
		}
		j.Owner = owner
		j.Status = "running"
		j.Attempts++
		started := now.UTC()
		j.StartedAt = &started
		claim.Job = &j
	}
	if _, err = tx.ExecContext(ctx, "UPDATE heartbeat_settings SET lease_owner=?,lease_until=?,next_due_at=?,last_attempt_at=? WHERE id=1", owner, expires.Unix(), now.Add(time.Duration(state.IntervalSeconds)*time.Second).Unix(), timestamp(now)); err != nil {
		return claim, err
	}
	return claim, tx.Commit()
}
func (s *Store) AuthorizeHeartbeat(ctx context.Context, c model.HeartbeatClaim, now time.Time) error {
	var count int
	err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM heartbeat_settings WHERE id=1 AND revision=? AND lease_owner=? AND lease_until>?", c.Settings.Revision, c.Owner, now.Unix()).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) FinishHeartbeat(ctx context.Context, c model.HeartbeatClaim, code string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var failure any
	if code != "" {
		failure = code
	}
	res, err := tx.ExecContext(ctx, `UPDATE heartbeat_settings SET last_success_at=CASE WHEN ? IS NULL THEN ? ELSE last_success_at END,last_error_code=?,lease_owner=NULL,lease_until=0 WHERE id=1 AND revision=? AND lease_owner=? AND lease_until>?`, failure, timestamp(now), failure, c.Settings.Revision, c.Owner, now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrLeaseLost
	}
	if c.Job != nil {
		status := "succeeded"
		if code != "" {
			status = "failed"
		}
		res, err = tx.ExecContext(ctx, `UPDATE job SET status=?,error_code=?,finished_at=?,lease_owner=NULL,lease_until=0 WHERE id=? AND type='heartbeat_test' AND status='running' AND lease_owner=? AND lease_until>?`, status, failure, timestamp(now), c.Job.ID, c.Owner, now.Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrLeaseLost
		}
	}
	return tx.Commit()
}

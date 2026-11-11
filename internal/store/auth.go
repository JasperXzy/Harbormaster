package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

var ErrInitialized = errors.New("already_initialized")
var ErrSetupToken = errors.New("invalid_setup_token")

func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM administrator WHERE id=1").Scan(&n)
	return n == 1, err
}

// PrepareSetup persists only a hash. Restarting with the same token does not
// extend its validity. Providing a new token deliberately rotates it.
func (s *Store) PrepareSetup(ctx context.Context, token []byte, now time.Time) error {
	h := sha256.Sum256(token)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO setup_credential(id,token_hash,expires_at)
 SELECT 1,?,? WHERE NOT EXISTS(SELECT 1 FROM administrator)
 ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,expires_at=excluded.expires_at
 WHERE setup_credential.token_hash<>excluded.token_hash`, h[:], timestamp(now.Add(24*time.Hour)))
	return err
}

func checkSetup(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, token []byte, now time.Time) error {
	var count int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrInitialized
	}
	var want []byte
	var expiry string
	if err := q.QueryRowContext(ctx, "SELECT token_hash,expires_at FROM setup_credential WHERE id=1").Scan(&want, &expiry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupToken
		}
		return err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiry)
	h := sha256.Sum256(token)
	if err != nil || !now.Before(expires) || subtle.ConstantTimeCompare(want, h[:]) != 1 {
		return ErrSetupToken
	}
	return nil
}

func (s *Store) CheckSetup(ctx context.Context, token []byte, now time.Time) error {
	return checkSetup(ctx, s.DB, token, now)
}

// CompleteSetup consumes the token, creates the administrator/settings and
// session in one transaction. Concurrent setup requests cannot both succeed.
func (s *Store) CompleteSetup(ctx context.Context, token []byte, passwordHash, timezone string, sessionHash []byte, expiry, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkSetup(ctx, tx, token, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO administrator VALUES(1,?,?)", passwordHash, timestamp(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO app_meta(key,value) VALUES('business_timezone',?)", timezone); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM setup_credential"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO session(token_hash,expires_at) VALUES(?,?)", sessionHash, timestamp(expiry)); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateSession rechecks the password hash under the write lock: a login that
// verified the old hash cannot race a password reset and restore a session.
func (s *Store) CreateSession(ctx context.Context, verifiedHash string, sessionHash []byte, expiry, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM session WHERE expires_at<=?", timestamp(now)); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO session(token_hash,expires_at)
 SELECT ?,? WHERE EXISTS(SELECT 1 FROM administrator WHERE id=1 AND password_hash=?)`, sessionHash, timestamp(expiry), verifiedHash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnauthorized
	}
	return tx.Commit()
}

// TakeAuthAttempt is durable across restarts, atomic across processes, and
// counts attempts before password hashing. Expired buckets are removed.
func (s *Store) TakeAuthAttempt(ctx context.Context, source string, now time.Time) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_attempt WHERE expires_at<=?", now.Unix()); err != nil {
		return 0, err
	}
	for _, b := range []struct {
		key   string
		limit int
	}{{"global", 30}, {"ip:" + source, 5}} {
		var attempts int
		var expiry int64
		err = tx.QueryRowContext(ctx, "SELECT attempts,expires_at FROM auth_attempt WHERE bucket=?", b.key).Scan(&attempts, &expiry)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if attempts >= b.limit {
			return max(1, int(expiry-now.Unix())), nil
		}
	}
	for _, key := range []string{"global", "ip:" + source} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO auth_attempt(bucket,attempts,expires_at) VALUES(?,1,?)
 ON CONFLICT(bucket) DO UPDATE SET attempts=auth_attempt.attempts+1`, key, now.Add(5*time.Minute).Unix()); err != nil {
			return 0, err
		}
	}
	return 0, tx.Commit()
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrPasskeyRequired = errors.New("passkey_required")
var ErrLastPasskey = errors.New("last_passkey")

// Eligible credentials have been checked against this server's RP by the
// WebAuthn layer. Recheck the exact persisted record under the write lock so
// concurrent deletion or credential updates cannot defeat the last-key guard.
func hasEligiblePasskey(ctx context.Context, tx *sql.Tx, eligible map[string]string, exclude string) (bool, error) {
	for id, verified := range eligible {
		if id == exclude {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM passkey WHERE id=? AND credential_json=?", id, verified).Scan(&count); err != nil {
			return false, err
		}
		if count == 1 {
			return true, nil
		}
	}
	return false, nil
}
func (s *Store) SetPasswordLogin(ctx context.Context, enabled bool, binding []byte, eligible map[string]string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validPasskeySession(ctx, tx, binding, now); err != nil {
		return err
	}
	if !enabled {
		ok, err := hasEligiblePasskey(ctx, tx, eligible, "")
		if err != nil {
			return err
		}
		if !ok {
			return ErrPasskeyRequired
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE passkey_state SET password_enabled=? WHERE id=1", enabled); err != nil {
		return err
	}
	return tx.Commit()
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrPasskeyLimit = errors.New("passkey_limit")

// Passkey contains only public verification data. CredentialJSON is never
// exposed by panel responses, which use the explicit metadata fields below.
type Passkey struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	CreatedAt      string  `json:"created_at"`
	LastUsedAt     *string `json:"last_used_at"`
	CredentialID   []byte  `json:"-"`
	CredentialJSON string  `json:"-"`
}
type PasskeySnapshot struct {
	UserHandle []byte
	Epoch      int64
	Keys       []Passkey
}
type PasskeyCeremony struct {
	ID, Kind, Name, SessionJSON string
	BindingHash                 []byte
	Epoch                       int64
	ExpiresAt                   time.Time
}

func (s *Store) Passkeys(ctx context.Context) (PasskeySnapshot, error) {
	result := PasskeySnapshot{Keys: []Passkey{}}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT user_handle,epoch FROM passkey_state WHERE id=1").Scan(&result.UserHandle, &result.Epoch); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,created_at,last_used_at,credential_id,credential_json FROM passkey ORDER BY created_at,id")
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var k Passkey
		if err = rows.Scan(&k.ID, &k.Name, &k.CreatedAt, &k.LastUsedAt, &k.CredentialID, &k.CredentialJSON); err != nil {
			rows.Close()
			return result, err
		}
		result.Keys = append(result.Keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) SavePasskeyCeremony(ctx context.Context, c PasskeyCeremony, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM passkey_ceremony WHERE expires_at<=?", timestamp(now)); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM passkey_ceremony").Scan(&n); err != nil {
		return err
	}
	if n >= 100 {
		return ErrPasskeyLimit
	}
	if c.Kind == "registration" {
		if err = validPasskeySession(ctx, tx, c.BindingHash, now); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO passkey_ceremony(id,kind,binding_hash,epoch,name,session_json,expires_at)
 SELECT ?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM administrator WHERE id=1) AND EXISTS(SELECT 1 FROM passkey_state WHERE epoch=?)`, c.ID, c.Kind, c.BindingHash, c.Epoch, c.Name, c.SessionJSON, timestamp(c.ExpiresAt), c.Epoch)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrUnauthorized
	}
	return tx.Commit()
}

// Consume before verification: malformed assertions cannot retry a challenge.
// A binding mismatch cannot consume another browser's ceremony.
func (s *Store) TakePasskeyCeremony(ctx context.Context, id, kind string, binding []byte, now time.Time) (PasskeyCeremony, error) {
	var c PasskeyCeremony
	var expiry string
	err := s.DB.QueryRowContext(ctx, `DELETE FROM passkey_ceremony WHERE id=? AND kind=? AND binding_hash=?
 RETURNING id,kind,name,session_json,binding_hash,epoch,expires_at`, id, kind, binding).Scan(&c.ID, &c.Kind, &c.Name, &c.SessionJSON, &c.BindingHash, &c.Epoch, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrUnauthorized
	}
	if err != nil {
		return c, err
	}
	c.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !now.Before(c.ExpiresAt) {
		return c, ErrUnauthorized
	}
	return c, nil
}

func validPasskeySession(ctx context.Context, tx *sql.Tx, binding []byte, now time.Time) error {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM session WHERE token_hash=? AND expires_at>?", binding, timestamp(now)).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnauthorized
	}
	return nil
}
func checkPasskeyEpoch(ctx context.Context, tx *sql.Tx, epoch int64) error {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM passkey_state WHERE epoch=?", epoch).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrUnauthorized
	}
	return nil
}
func (s *Store) RegisterPasskey(ctx context.Context, c PasskeyCeremony, k Passkey, now time.Time) error {
	if c.Kind != "registration" || !now.Before(c.ExpiresAt) {
		return ErrUnauthorized
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validPasskeySession(ctx, tx, c.BindingHash, now); err != nil {
		return err
	}
	if err = checkPasskeyEpoch(ctx, tx, c.Epoch); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM passkey").Scan(&n); err != nil {
		return err
	}
	if n >= 20 {
		return ErrPasskeyLimit
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO passkey(id,credential_id,name,credential_json,created_at) VALUES(?,?,?,?,?) ON CONFLICT(credential_id) DO NOTHING`, k.ID, k.CredentialID, c.Name, k.CredentialJSON, timestamp(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// Persist the signature counter and new session together. Compare-and-swap
// protects against concurrent assertions, key deletion and reset-password.
func (s *Store) CompletePasskeyLogin(ctx context.Context, c PasskeyCeremony, old Passkey, updated string, sessionHash []byte, expiry, now time.Time) error {
	if c.Kind != "login" || !now.Before(c.ExpiresAt) {
		return ErrUnauthorized
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkPasskeyEpoch(ctx, tx, c.Epoch); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "UPDATE passkey SET credential_json=?,last_used_at=? WHERE id=? AND credential_json=?", updated, timestamp(now), old.ID, old.CredentialJSON)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrUnauthorized
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM session WHERE expires_at<=?", timestamp(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO session(token_hash,expires_at) VALUES(?,?)", sessionHash, timestamp(expiry)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) DeletePasskey(ctx context.Context, id string, binding []byte, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validPasskeySession(ctx, tx, binding, now); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM passkey WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

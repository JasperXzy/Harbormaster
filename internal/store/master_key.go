package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

const masterVerifierOwner = "system/master-key-verifier/v1"
const masterVerifierValue = "harbormaster-master-key-verifier-v1"

var ErrMasterKey = errors.New("master_key_verification_failed")

// BindMasterKey runs before production workers or credential mutations start.
// Existing databases without a verifier are adopted only after every existing
// encrypted credential can be authenticated with the supplied key and its AAD.
// This is key verification, not key rotation or credential recovery.
func (s *Store) BindMasterKey(ctx context.Context, box *security.Box) error {
	if box == nil {
		return ErrMasterKey
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sealed []byte
	err = tx.QueryRowContext(ctx, "SELECT ciphertext FROM credential WHERE owner_field=?", masterVerifierOwner).Scan(&sealed)
	if err == nil {
		raw, err := box.Decrypt(sealed, masterVerifierOwner)
		if err != nil {
			return ErrMasterKey
		}
		defer clear(raw)
		if !bytes.Equal(raw, []byte(masterVerifierValue)) {
			return ErrMasterKey
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT owner_field,ciphertext FROM credential ORDER BY owner_field")
	if err != nil {
		return err
	}
	for rows.Next() {
		var owner string
		var ciphertext []byte
		if err = rows.Scan(&owner, &ciphertext); err != nil {
			rows.Close()
			return err
		}
		raw, err := box.Decrypt(ciphertext, owner)
		if err != nil {
			rows.Close()
			return ErrMasterKey
		}
		clear(raw)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, "SELECT ciphertext FROM telegram_channel WHERE id=1").Scan(&sealed)
	if err == nil {
		raw, err := box.Decrypt(sealed, model.TelegramCredentialOwner)
		if err != nil {
			return ErrMasterKey
		}
		clear(raw)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	sealed, err = box.Encrypt([]byte(masterVerifierValue), masterVerifierOwner)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO credential(owner_field,ciphertext,updated_at) VALUES(?,?,?)", masterVerifierOwner, sealed, timestamp(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

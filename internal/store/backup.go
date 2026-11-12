package store

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

// SnapshotTo creates a standalone, consistent SQLite file, including committed
// WAL data. The caller supplies a new path inside a private temporary directory.
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, "VACUUM main INTO ?", path)
	return err
}

// ReencryptCopy is only for isolated backup/restore copies, never a running
// database. It rewraps every current reversible credential with its original
// owner AAD, including the master-key verifier, and removes login sessions.
func (s *Store) ReencryptCopy(ctx context.Context, from, to *security.Box) error {
	if from == nil || to == nil {
		return ErrMasterKey
	}
	if err := s.Ready(ctx); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT owner_field,ciphertext FROM credential ORDER BY owner_field")
	if err != nil {
		return err
	}
	type replacement struct {
		owner  string
		sealed []byte
	}
	items := []replacement{}
	for rows.Next() {
		var owner string
		var sealed []byte
		if err = rows.Scan(&owner, &sealed); err != nil {
			break
		}
		plain, openErr := from.Decrypt(sealed, owner)
		if openErr != nil {
			err = ErrMasterKey
			break
		}
		rewrapped, sealErr := to.Encrypt(plain, owner)
		clear(plain)
		if sealErr != nil {
			err = sealErr
			break
		}
		items = append(items, replacement{owner, rewrapped})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, "UPDATE credential SET ciphertext=? WHERE owner_field=?", item.sealed, item.owner); err != nil {
			return err
		}
	}
	var sealed []byte
	err = tx.QueryRowContext(ctx, "SELECT ciphertext FROM telegram_channel WHERE id=1").Scan(&sealed)
	if err == nil {
		plain, openErr := from.Decrypt(sealed, model.TelegramCredentialOwner)
		if openErr != nil {
			return ErrMasterKey
		}
		rewrapped, sealErr := to.Encrypt(plain, model.TelegramCredentialOwner)
		clear(plain)
		if sealErr != nil {
			return sealErr
		}
		if _, err = tx.ExecContext(ctx, "UPDATE telegram_channel SET ciphertext=? WHERE id=1", rewrapped); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM session"); err != nil {
		return err
	}
	return tx.Commit()
}

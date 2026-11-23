package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

// Export streams an encrypted snapshot without changing source credentials,
// sessions, tasks or notifications. Only private temporary copies are rekeyed.
func Export(ctx context.Context, source *store.Store, master *security.Box, password []byte, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if master == nil {
		return store.ErrMasterKey
	}
	if err := source.Ready(ctx); err != nil {
		return err
	}
	e, err := newEnvelope(password)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "harbormaster-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := source.SnapshotTo(ctx, filepath.Join(dir, "harbormaster.db")); err != nil {
		return err
	}
	copyDB, err := store.Open(ctx, dir, false)
	if err != nil {
		return err
	}
	defer copyDB.Close()
	if err := copyDB.BindMasterKey(ctx, master); err != nil {
		return err
	}
	if err := copyDB.ReencryptCopy(ctx, master, e.credentials); err != nil {
		return err
	}
	// A second VACUUM produces a standalone compact file and removes obsolete
	// source-key ciphertext/session pages before the outer encryption pass.
	payload := filepath.Join(dir, "backup-payload.db")
	if err := copyDB.SnapshotTo(ctx, payload); err != nil {
		return err
	}
	f, err := os.Open(payload)
	if err != nil {
		return err
	}
	defer f.Close()
	return e.seal(ctx, out, f)
}

var ErrOutputExists = errors.New("backup_output_exists")

// ExportFile publishes a complete fsynced archive with an atomic, no-replace
// hard link. Existing files and symlinks are never overwritten.
func ExportFile(ctx context.Context, source *store.Store, master *security.Box, password []byte, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return ErrOutputExists
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hm-backup-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := Export(ctx, source, master, password, f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if os.IsExist(err) {
			return ErrOutputExists
		}
		return err
	}
	return nil
}

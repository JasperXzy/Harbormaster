package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

var ErrRestoreTargetExists = errors.New("restore_target_exists")

// Restore authenticates, validates and transforms in a private staging
// directory. Publication exclusively creates a new directory and links in a
// complete standalone database. It never opens or replaces an existing target.
func Restore(ctx context.Context, in io.Reader, password []byte, master *security.Box, target string, now time.Time) (store.RestoreSummary, error) {
	var summary store.RestoreSummary
	if master == nil {
		return summary, store.ErrMasterKey
	}
	if _, err := os.Lstat(target); err == nil {
		return summary, ErrRestoreTargetExists
	} else if !os.IsNotExist(err) {
		return summary, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".hm-restore-")
	if err != nil {
		return summary, err
	}
	defer os.RemoveAll(stage)
	f, err := os.OpenFile(filepath.Join(stage, "harbormaster.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return summary, err
	}
	backupKey, decodeErr := Unseal(ctx, f, in, password)
	closeErr := f.Close()
	if decodeErr != nil {
		return summary, decodeErr
	}
	if closeErr != nil {
		return summary, closeErr
	}
	if err := store.CheckBackupFile(ctx, f.Name()); err != nil {
		return summary, err
	}
	s, err := store.Open(ctx, stage, false)
	if err != nil {
		return summary, err
	}
	defer s.Close()
	if err := s.CheckBackupIntegrity(ctx); err != nil {
		return summary, err
	}
	if err := s.BindMasterKey(ctx, backupKey); err != nil {
		return summary, err
	}
	if err := s.ReencryptCopy(ctx, backupKey, master); err != nil {
		return summary, err
	}
	if summary, err = s.PrepareRestoredCopy(ctx, now); err != nil {
		return summary, err
	}
	if err := s.CheckBackupIntegrity(ctx); err != nil {
		return summary, err
	}
	payload := filepath.Join(stage, "restored.db")
	if err := s.SnapshotTo(ctx, payload); err != nil {
		return summary, err
	}
	f, err = os.OpenFile(payload, os.O_RDWR, 0600)
	if err != nil {
		return summary, err
	}
	syncErr := f.Sync()
	closeErr = f.Close()
	if syncErr != nil {
		return summary, syncErr
	}
	if closeErr != nil {
		return summary, closeErr
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		if os.IsExist(err) {
			return summary, ErrRestoreTargetExists
		}
		return summary, err
	}
	if err := os.Link(payload, filepath.Join(target, "harbormaster.db")); err != nil {
		// Remove only our empty reservation; never delete concurrent files.
		_ = os.Remove(target)
		return summary, err
	}
	return summary, nil
}

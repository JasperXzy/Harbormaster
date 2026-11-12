//go:build windows

package ipinfo

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

var errBusy = errors.New("ipdb_update_busy")

func lockCache(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "update.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrUpdate
	}
	var lock windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock); err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errBusy
		}
		return nil, ErrUpdate
	}
	return func() { windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &lock); f.Close() }, nil
}

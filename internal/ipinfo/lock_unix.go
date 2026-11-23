//go:build linux || darwin

package ipinfo

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errBusy = errors.New("ipdb_update_busy")

func lockCache(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "update.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, ErrUpdate
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errBusy
		}
		return nil, ErrUpdate
	}
	return func() { f.Close() }, nil
}

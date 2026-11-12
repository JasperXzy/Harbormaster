//go:build !linux && !darwin && !windows

package ipinfo

import "errors"

var errBusy = errors.New("ipdb_update_busy")

func lockCache(string) (func(), error) { return nil, ErrUpdate }

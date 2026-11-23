//go:build linux || darwin

package ipinfo

import (
	"runtime"
	"syscall"
)

func peakRSS() *uint64 {
	var value syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &value) != nil || value.Maxrss < 0 {
		return nil
	}
	n := uint64(value.Maxrss)
	if runtime.GOOS == "linux" {
		n *= 1024
	}
	return &n
}

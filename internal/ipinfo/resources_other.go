//go:build !linux && !darwin

package ipinfo

func peakRSS() *uint64 { return nil }

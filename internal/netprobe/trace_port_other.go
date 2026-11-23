//go:build !linux

package netprobe

import "net/netip"

func reserveTracePort(netip.Addr) (uint16, func(), error) { return 0, nil, ErrUnavailable }

//go:build linux

package netprobe

import (
	"golang.org/x/sys/unix"
	"net/netip"
)

// Reserve an ephemeral TCP port without listen/connect and therefore without
// opening an inbound service or creating kernel-generated SYN retransmissions.
func reserveTracePort(source netip.Addr) (uint16, func(), error) {
	family := unix.AF_INET
	var address unix.Sockaddr
	if source.Is4() {
		address = &unix.SockaddrInet4{Addr: source.As4()}
	} else {
		family = unix.AF_INET6
		address = &unix.SockaddrInet6{Addr: source.As16()}
	}
	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		return 0, nil, err
	}
	closePort := func() { _ = unix.Close(fd) }
	if err = unix.Bind(fd, address); err != nil {
		closePort()
		return 0, nil, err
	}
	bound, err := unix.Getsockname(fd)
	if err != nil {
		closePort()
		return 0, nil, err
	}
	switch b := bound.(type) {
	case *unix.SockaddrInet4:
		return uint16(b.Port), closePort, nil
	case *unix.SockaddrInet6:
		return uint16(b.Port), closePort, nil
	}
	closePort()
	return 0, nil, ErrUnavailable
}

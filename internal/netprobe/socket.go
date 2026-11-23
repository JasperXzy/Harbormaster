package netprobe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func openSession(ctx context.Context, q Request, ip netip.Addr) (session, error) {
	if !PublicAddress(ip) {
		return nil, ErrTarget
	}
	if q.Method == "tcp" {
		return &tcpSession{address: net.JoinHostPort(ip.String(), strconv.Itoa(q.Port)), dial: (&net.Dialer{KeepAlive: -1}).DialContext}, nil
	}
	network, local, proto := "ip4:icmp", "0.0.0.0", 1
	var requestType, replyType icmp.Type = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	if ip.Is6() {
		network, local, proto = "ip6:ipv6-icmp", "::", 58
		requestType, replyType = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return nil, ErrUnavailable
	}
	conn, err := icmp.ListenPacket(network, local)
	if err != nil {
		return nil, ErrUnavailable
	}
	s := &echoSession{conn: conn, ip: ip, proto: proto, requestType: requestType, replyType: replyType, id: int(binary.BigEndian.Uint16(data)), data: data}
	s.stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
	return s, nil
}

type tcpSession struct {
	address string
	dial    func(context.Context, string, string) (net.Conn, error)
}

func (s *tcpSession) Close() error { return nil }
func (s *tcpSession) Probe(ctx context.Context, _ int, timeout time.Duration) (time.Duration, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	conn, err := s.dial(callCtx, "tcp", s.address)
	elapsed := time.Since(start)
	if err != nil {
		return 0, networkError(err)
	}
	_ = conn.Close() // TCP handshake only: no HTTP/TLS/application payload.
	return elapsed, nil
}
func networkError(err error) error {
	var timeout net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout
	case errors.As(err, &timeout) && timeout.Timeout():
		return ErrTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		return ErrRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return ErrUnreachable
	default:
		return ErrIO
	}
}

type echoSession struct {
	conn                   net.PacketConn
	ip                     netip.Addr
	proto                  int
	requestType, replyType icmp.Type
	id                     int
	data                   []byte
	stop                   func() bool
}

func (s *echoSession) Close() error {
	if s.stop != nil {
		s.stop()
	}
	return s.conn.Close()
}
func (s *echoSession) Probe(ctx context.Context, sequence int, timeout time.Duration) (time.Duration, error) {
	if ctx.Err() != nil {
		return 0, ErrCancelled
	}
	msg := icmp.Message{Type: s.requestType, Code: 0, Body: &icmp.Echo{ID: s.id, Seq: sequence, Data: s.data}}
	raw, err := msg.Marshal(nil)
	if err != nil {
		return 0, ErrIO
	}
	deadline := time.Now().Add(timeout)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	if err = s.conn.SetDeadline(deadline); err != nil {
		return 0, ErrIO
	}
	start := time.Now()
	n, err := s.conn.WriteTo(raw, &net.IPAddr{IP: net.IP(s.ip.AsSlice())})
	if err != nil {
		return 0, networkError(err)
	}
	if n != len(raw) {
		return 0, ErrIO
	}
	buffer := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return 0, ErrCancelled
		}
		n, peer, err := s.conn.ReadFrom(buffer)
		if err != nil {
			return 0, networkError(err)
		}
		if !s.matches(buffer[:n], peer, sequence) {
			continue
		}
		return time.Since(start), nil
	}
}
func (s *echoSession) matches(raw []byte, peer net.Addr, sequence int) bool {
	addr, ok := peer.(*net.IPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(addr.IP)
	if !ok || addr.Zone != "" || ip.Unmap() != s.ip {
		return false
	}
	if s.proto == 1 && !validChecksum(raw) {
		return false
	}
	msg, err := icmp.ParseMessage(s.proto, raw)
	if err != nil || msg.Type != s.replyType || msg.Code != 0 {
		return false
	}
	echo, ok := msg.Body.(*icmp.Echo)
	return ok && echo.ID == s.id && echo.Seq == sequence && bytes.Equal(echo.Data, s.data)
}
func validChecksum(raw []byte) bool {
	var sum uint32
	for len(raw) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(raw))
		raw = raw[2:]
	}
	if len(raw) > 0 {
		sum += uint32(raw[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 65535) + (sum >> 16)
	}
	return sum == 65535
}

package netprobe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestTCPNumericEndpointHandshakeOnly(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			received <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var buf [1]byte
		n, err := conn.Read(buf[:])
		if n != 0 || !errors.Is(err, io.EOF) {
			received <- errors.New("application data sent or socket retained")
			return
		}
		received <- nil
	}()
	s := &tcpSession{address: "1.1.1.1:443", dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "1.1.1.1:443" {
			t.Fatal("not pinned", network, address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", listener.Addr().String())
	}}
	elapsed, err := s.Probe(context.Background(), 1, time.Second)
	if err != nil || elapsed < 0 {
		t.Fatal(elapsed, err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
}
func TestTCPTimeoutCancellationAndSafeErrors(t *testing.T) {
	s := &tcpSession{address: "1.1.1.1:443", dial: func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }}
	if _, err := s.Probe(context.Background(), 1, 10*time.Millisecond); err != ErrTimeout {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, want error }{{syscall.ECONNREFUSED, ErrRefused}, {syscall.EHOSTUNREACH, ErrUnreachable}, {syscall.ENETUNREACH, ErrUnreachable}, {errors.New("sensitive resolver detail"), ErrIO}} {
		if got := networkError(tc.input); got != tc.want {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, _ = s.Probe(ctx, 1, time.Second)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("cancel did not interrupt connect")
	}
}

type packet struct {
	raw  []byte
	peer net.Addr
}
type packetSocket struct {
	mu       sync.Mutex
	packets  []packet
	wrote    []byte
	deadline time.Time
	closed   bool
	failure  error
}

func (c *packetSocket) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.packets) == 0 {
		return 0, nil, context.DeadlineExceeded
	}
	p := c.packets[0]
	c.packets = c.packets[1:]
	return copy(b, p.raw), p.peer, nil
}
func (c *packetSocket) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return 0, c.failure
	}
	c.wrote = append([]byte(nil), b...)
	return len(b), nil
}
func (c *packetSocket) Close() error                       { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true; return nil }
func (c *packetSocket) LocalAddr() net.Addr                { return &net.IPAddr{IP: net.IPv4zero} }
func (c *packetSocket) SetDeadline(t time.Time) error      { c.deadline = t; return nil }
func (c *packetSocket) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *packetSocket) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }
func reply(t *testing.T, kind icmp.Type, code, id, seq int, data []byte) []byte {
	t.Helper()
	b, err := (&icmp.Message{Type: kind, Code: code, Body: &icmp.Echo{ID: id, Seq: seq, Data: data}}).Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestEchoIPv4AndIPv6RejectUnrelatedReplies(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		ip := netip.MustParseAddr("1.1.1.1")
		proto := 1
		var req, resp icmp.Type = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
		if v6 {
			ip = netip.MustParseAddr("2606:4700:4700::1111")
			proto = 58
			req, resp = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
		}
		data := bytes.Repeat([]byte{17}, 32)
		peer := &net.IPAddr{IP: net.IP(ip.AsSlice())}
		conn := &packetSocket{}
		s := &echoSession{conn: conn, ip: ip, proto: proto, requestType: req, replyType: resp, id: 42, data: data}
		good := reply(t, resp, 0, 42, 1, data)
		for _, p := range []packet{
			{reply(t, req, 0, 42, 1, data), peer},
			{reply(t, resp, 0, 43, 1, data), peer},
			{reply(t, resp, 0, 42, 2, data), peer},
			{reply(t, resp, 0, 42, 1, bytes.Repeat([]byte{18}, 32)), peer},
			{good, &net.IPAddr{IP: net.ParseIP("8.8.8.8")}},
			{good, &net.UDPAddr{IP: net.IP(ip.AsSlice())}},
			{[]byte{0, 0}, peer},
		} {
			if s.matches(p.raw, p.peer, 1) {
				t.Fatal("unrelated echo accepted", v6)
			}
			conn.packets = append(conn.packets, p)
		}
		if !v6 {
			bad := append([]byte(nil), good...)
			bad[2] ^= 1
			if s.matches(bad, peer, 1) {
				t.Fatal("invalid checksum accepted")
			}
			conn.packets = append(conn.packets, packet{bad, peer})
		}
		conn.packets = append(conn.packets, packet{good, peer})
		elapsed, err := s.Probe(context.Background(), 1, time.Second)
		if err != nil || elapsed < 0 || len(conn.packets) != 0 || conn.deadline.IsZero() {
			t.Fatal(elapsed, err)
		}
		msg, err := icmp.ParseMessage(proto, conn.wrote)
		if err != nil || msg.Type != req || msg.Code != 0 {
			t.Fatal(msg, err)
		}
		echo := msg.Body.(*icmp.Echo)
		if echo.Seq != 1 || echo.ID != 42 || !bytes.Equal(echo.Data, data) {
			t.Fatal(echo)
		}
	}
}
func TestEchoDeadlineAndCancellation(t *testing.T) {
	conn := &packetSocket{}
	s := &echoSession{conn: conn, ip: netip.MustParseAddr("1.1.1.1"), proto: 1, requestType: ipv4.ICMPTypeEcho, replyType: ipv4.ICMPTypeEchoReply, id: 1, data: []byte("fixed")}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	conn.wrote = nil
	if _, err := s.Probe(ctx, 1, time.Second); err != ErrTimeout {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if !conn.deadline.Equal(deadline) {
		t.Fatal("parent deadline ignored")
	}
	cancel()
	conn.wrote = nil
	if _, err := s.Probe(ctx, 1, time.Second); err != ErrCancelled {
		t.Fatal(err)
	}
	if len(conn.wrote) != 0 {
		t.Fatal("cancelled probe sent a packet")
	}
	conn.failure = syscall.EPERM
	if _, err := s.Probe(context.Background(), 1, time.Second); err != ErrIO {
		t.Fatal("permission failure counted as loss", err)
	}
}

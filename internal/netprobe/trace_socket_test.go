package netprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// A blocking packet connection exercises deadline changes and reader joining;
// no test below opens a raw socket or sends a packet to the public network.
type traceTestConn struct {
	mu       sync.Mutex
	packets  chan packet
	changed  chan struct{}
	deadline time.Time
	closed   bool
	writes   [][]byte
	hook     func([]byte, net.Addr)
	failure  error
	readers  atomic.Int32
}

func newTraceConn() *traceTestConn {
	return &traceTestConn{packets: make(chan packet, 16), changed: make(chan struct{})}
}
func (c *traceTestConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	c.readers.Add(1)
	defer c.readers.Add(-1)
	for {
		c.mu.Lock()
		deadline, wake, closed := c.deadline, c.changed, c.closed
		c.mu.Unlock()
		if closed {
			return 0, nil, net.ErrClosed
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case p := <-c.packets:
			timer.Stop()
			return copy(buf, p.raw), p.peer, nil
		case <-timer.C:
			return 0, nil, context.DeadlineExceeded
		case <-wake:
			timer.Stop()
		}
	}
}
func (c *traceTestConn) WriteTo(b []byte, peer net.Addr) (int, error) {
	c.mu.Lock()
	failure := c.failure
	if failure == nil {
		c.writes = append(c.writes, bytes.Clone(b))
	}
	c.mu.Unlock()
	if failure != nil {
		return 0, failure
	}
	if c.hook != nil {
		c.hook(b, peer)
	}
	return len(b), nil
}
func (c *traceTestConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.changed)
		c.changed = make(chan struct{})
	}
	return nil
}
func (c *traceTestConn) LocalAddr() net.Addr { return &net.IPAddr{IP: net.IPv4zero} }
func (c *traceTestConn) SetDeadline(d time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = d
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}
func (c *traceTestConn) SetReadDeadline(d time.Time) error  { return c.SetDeadline(d) }
func (c *traceTestConn) SetWriteDeadline(d time.Time) error { return c.SetDeadline(d) }

func TestTraceSocketMatchesHopAndTargetAndResetsSYNACK(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		for _, tcp := range []bool{false, true} {
			s := traceTestSocket(v6, tcp)
			control := newTraceConn()
			s.icmpConn = control
			writer := control
			if tcp {
				writer = newTraceConn()
				s.tcpConn = writer
			}
			ttl := 0
			s.setHop = func(n int) error { ttl = n; return nil }
			router := netip.MustParseAddr("10.0.0.1")
			var exceeded, echo icmp.Type = ipv4.ICMPTypeTimeExceeded, ipv4.ICMPTypeEchoReply
			if v6 {
				router = netip.MustParseAddr("fe80::1")
				exceeded, echo = ipv6.ICMPTypeTimeExceeded, ipv6.ICMPTypeEchoReply
			}
			writer.hook = func(raw []byte, peer net.Addr) {
				if got, ok := packetPeer(peer, !v6); !ok || got != s.target {
					t.Error("un-pinned destination", peer)
				}
				if tcp && raw[13] == 0x04 {
					return
				}
				p := tracePacket{source: s.source, target: s.target, raw: bytes.Clone(raw), protocol: 1}
				if v6 {
					p.protocol = 58
				}
				if tcp {
					p.protocol = 6
				}
				if ttl == 1 {
					control.packets <- packet{traceICMP(t, p, router, exceeded, 0, &icmp.TimeExceeded{Data: quoteTrace(p, 8)}), ipPeer(router)}
				} else if tcp {
					bad := traceTCPReply(p, 0x12)
					bad[11]++
					repairTCPChecksum(p, bad)
					writer.packets <- packet{bad, ipPeer(s.target)}
					writer.packets <- packet{traceTCPReply(p, 0x12), ipPeer(s.target)}
				} else {
					control.packets <- packet{traceICMP(t, p, s.target, echo, 0, &icmp.Echo{ID: int(binary.BigEndian.Uint16(raw[4:6])), Seq: int(binary.BigEndian.Uint16(raw[6:8])), Data: raw[8:]}), ipPeer(s.target)}
				}
			}
			r, err := s.Probe(context.Background(), 1, 1, time.Second)
			if err != nil || r.address != router || r.kind != "time_exceeded" || r.reached {
				t.Fatal(r, err)
			}
			if control.readers.Load() != 0 || writer.readers.Load() != 0 {
				t.Fatal("reader left behind")
			}
			r, err = s.Probe(context.Background(), 2, 2, time.Second)
			if err != nil || !r.reached || r.address != s.target {
				t.Fatal(r, err)
			}
			if control.readers.Load() != 0 || writer.readers.Load() != 0 {
				t.Fatal("reader left behind")
			}
			if tcp {
				writer.mu.Lock()
				writes := writer.writes
				writer.mu.Unlock()
				if len(writes) != 3 || writes[2][13] != 0x04 || len(writes[2]) != 20 || binary.BigEndian.Uint32(writes[2][4:8]) != binary.BigEndian.Uint32(writes[1][4:8])+1 || !validChecksum(transportPacket(s.source, s.target, 6, writes[2])) {
					t.Fatal("invalid cleanup RST", writes)
				}
			} else if len(writer.writes) != 2 {
				t.Fatal("extra ICMP packets")
			}
			released := 0
			s.releasePort = func() { released++ }
			_ = s.Close()
			_ = s.Close()
			if released != 1 || !control.closed || !writer.closed {
				t.Fatal("close not idempotent")
			}
		}
	}
}

func TestTraceSocketTimeoutCancellationAndLocalFailures(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "ttl_failure", "write_failure"} {
		t.Run(mode, func(t *testing.T) {
			s := traceTestSocket(false, true)
			control, writer := newTraceConn(), newTraceConn()
			s.icmpConn, s.tcpConn = control, writer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.setHop = func(int) error {
				if mode == "ttl_failure" {
					return errors.New("local details")
				}
				return nil
			}
			if mode == "write_failure" {
				writer.failure = errors.New("local details")
			}
			if mode == "cancel" {
				writer.hook = func([]byte, net.Addr) { cancel() }
			}
			start := time.Now()
			_, err := s.Probe(ctx, 1, 1, 20*time.Millisecond)
			want := ErrIO
			if mode == "timeout" {
				want = ErrTimeout
			}
			if mode == "cancel" {
				want = ErrCancelled
			}
			if err != want || time.Since(start) > time.Second {
				t.Fatal(err, time.Since(start))
			}
			if control.readers.Load() != 0 || writer.readers.Load() != 0 {
				t.Fatal("leaked readers")
			}
			_ = s.Close()
		})
	}
}

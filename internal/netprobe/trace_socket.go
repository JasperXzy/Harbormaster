package netprobe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type traceSocket struct {
	source, target         netip.Addr
	icmpConn, tcpConn      net.PacketConn
	setHop                 func(int) error
	sourcePort, targetPort uint16
	nonce                  []byte
	releasePort            func()
	closeOnce              sync.Once
}

func openTraceSession(ctx context.Context, q TraceRequest, target netip.Addr) (traceSession, error) {
	if !PublicAddress(target) {
		return nil, ErrTarget
	}
	// A connected UDP socket selects the route/source without transmitting a
	// datagram. It is closed immediately; it never listens or sends a payload.
	network, icmpNetwork, tcpNetwork := "udp4", "ip4:icmp", "ip4:tcp"
	if target.Is6() {
		network, icmpNetwork, tcpNetwork = "udp6", "ip6:ipv6-icmp", "ip6:tcp"
	}
	port := q.Port
	if port == 0 {
		port = 33434
	}
	route, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(target.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, ErrUnavailable
	}
	local, ok := route.LocalAddr().(*net.UDPAddr)
	_ = route.Close()
	if !ok {
		return nil, ErrUnavailable
	}
	source, ok := netip.AddrFromSlice(local.IP)
	source = source.Unmap()
	if !ok || source.IsUnspecified() || source.Is4() != target.Is4() {
		return nil, ErrUnavailable
	}
	s := &traceSocket{source: source, target: target, nonce: make([]byte, 32), targetPort: uint16(q.Port)}
	complete := false
	defer func() {
		if !complete {
			_ = s.Close()
		}
	}()
	if _, err = rand.Read(s.nonce); err != nil {
		return nil, ErrUnavailable
	}
	// Unconnected ICMP is essential: connected raw sockets can filter out
	// Time Exceeded messages whose sender is an intermediate router.
	c, err := icmp.ListenPacket(icmpNetwork, source.String())
	if err != nil {
		return nil, ErrUnavailable
	}
	s.icmpConn = c
	if target.Is4() {
		s.setHop = c.IPv4PacketConn().SetTTL
	} else {
		s.setHop = c.IPv6PacketConn().SetHopLimit
	}
	if q.Method == "tcp" {
		s.sourcePort, s.releasePort, err = reserveTracePort(source)
		if err != nil {
			return nil, ErrUnavailable
		}
		t, err := net.ListenIP(tcpNetwork, &net.IPAddr{IP: net.IP(source.AsSlice())})
		if err != nil {
			return nil, ErrUnavailable
		}
		s.tcpConn = t
		if target.Is4() {
			s.setHop = ipv4.NewPacketConn(t).SetTTL
		} else {
			s.setHop = ipv6.NewPacketConn(t).SetHopLimit
		}
	}
	// The trace runner always cancels this context on return, releasing the
	// callback even when the socket has already been closed normally.
	context.AfterFunc(ctx, func() { _ = s.Close() })
	complete = true
	return s, nil
}

func (s *traceSocket) Close() error {
	// Cancellation and the runner's defer may both close the session.
	s.closeOnce.Do(func() {
		if s.icmpConn != nil {
			_ = s.icmpConn.Close()
		}
		if s.tcpConn != nil {
			_ = s.tcpConn.Close()
		}
		if s.releasePort != nil {
			s.releasePort()
		}
	})
	return nil
}

func (s *traceSocket) request(sequence int) (tracePacket, error) {
	p := tracePacket{source: s.source, target: s.target}
	if s.tcpConn != nil {
		p.protocol = 6
		// A random starting ISN plus the sequence avoids collisions within a run.
		p.raw = tcpTracePacket(s.source, s.target, s.sourcePort, s.targetPort, binary.BigEndian.Uint32(s.nonce[:4])+uint32(sequence), 0x02)
		return p, nil
	}
	var kind icmp.Type = ipv4.ICMPTypeEcho
	var pseudo []byte
	p.protocol = 1
	if s.target.Is6() {
		kind = ipv6.ICMPTypeEchoRequest
		p.protocol = 58
		pseudo = icmp.IPv6PseudoHeader(net.IP(s.source.AsSlice()), net.IP(s.target.AsSlice()))
	}
	b, err := (&icmp.Message{Type: kind, Body: &icmp.Echo{ID: int(binary.BigEndian.Uint16(s.nonce)), Seq: sequence, Data: s.nonce}}).Marshal(pseudo)
	p.raw = b
	return p, err
}

func (s *traceSocket) Probe(ctx context.Context, ttl, sequence int, timeout time.Duration) (traceReply, error) {
	if ctx.Err() != nil {
		return traceReply{}, ErrCancelled
	}
	p, err := s.request(sequence)
	if err != nil {
		return traceReply{}, ErrIO
	}
	if err = s.setHop(ttl); err != nil {
		return traceReply{}, ErrIO
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	connections := []net.PacketConn{s.icmpConn}
	writer := s.icmpConn
	if s.tcpConn != nil {
		connections = append(connections, s.tcpConn)
		writer = s.tcpConn
	}
	for _, conn := range connections {
		if err = conn.SetDeadline(deadline); err != nil {
			return traceReply{}, ErrIO
		}
	}
	start := time.Now()
	n, err := writer.WriteTo(p.raw, &net.IPAddr{IP: net.IP(s.target.AsSlice())})
	if err != nil {
		return traceReply{}, networkError(err)
	}
	if n != len(p.raw) {
		return traceReply{}, ErrIO
	}
	type answer struct {
		reply traceReply
		err   error
	}
	answers := make(chan answer, len(connections))
	var readers sync.WaitGroup
	defer func() {
		for _, conn := range connections {
			if conn.SetReadDeadline(time.Now()) != nil {
				_ = conn.Close()
			}
		}
		readers.Wait() // No reader survives into the next sequence/deadline.
	}()
	for i, conn := range connections {
		readers.Add(1)
		go func(tcp bool, conn net.PacketConn) {
			defer readers.Done()
			buffer := make([]byte, 4096)
			for {
				n, peer, err := conn.ReadFrom(buffer)
				if err != nil {
					answers <- answer{err: networkError(err)}
					return
				}
				var reply traceReply
				var matched bool
				if tcp {
					reply, matched = p.matchTCP(buffer[:n], peer)
				} else {
					reply, matched = p.matchICMP(buffer[:n], peer)
				}
				if matched {
					reply.elapsed = time.Since(start)
					answers <- answer{reply: reply}
					return
				}
				if ctx.Err() != nil {
					answers <- answer{err: ErrCancelled}
					return
				}
			}
		}(i == 1, conn)
	}
	for remaining := len(connections); remaining > 0; remaining-- {
		select {
		case <-ctx.Done():
			return traceReply{}, ErrCancelled
		case a := <-answers:
			if a.err == nil {
				if a.reply.kind == "tcp_syn_ack" {
					// RFC 9293: responding to an ACK with RST uses its ACK as SEQ.
					// No handshake ACK/application data is ever sent. The local TCP
					// stack may also reset an unsolicited SYN-ACK on this bound port.
					rst := tcpTracePacket(s.source, s.target, s.sourcePort, s.targetPort, binary.BigEndian.Uint32(p.raw[4:8])+1, 0x04)
					_, _ = writer.WriteTo(rst, &net.IPAddr{IP: net.IP(s.target.AsSlice())})
				}
				return a.reply, nil
			}
			if a.err != ErrTimeout {
				return traceReply{}, a.err
			}
		}
	}
	return traceReply{}, ErrTimeout
}

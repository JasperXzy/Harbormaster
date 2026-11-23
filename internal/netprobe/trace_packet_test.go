package netprobe

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func traceTestSocket(v6, tcp bool) *traceSocket {
	s := &traceSocket{source: netip.MustParseAddr("10.0.0.2"), target: netip.MustParseAddr("1.1.1.1"), nonce: bytes.Repeat([]byte{17}, 32), sourcePort: 40000, targetPort: 443}
	if v6 {
		s.source = netip.MustParseAddr("2001:db8::2")
		s.target = netip.MustParseAddr("2606:4700:4700::1111")
	}
	if tcp {
		s.tcpConn = &packetSocket{}
	}
	return s
}
func quoteTrace(p tracePacket, n int) []byte {
	header := 20
	if p.target.Is6() {
		header = 40
	}
	b := make([]byte, header+n)
	if p.target.Is4() {
		b[0], b[8], b[9] = 0x45, 1, p.protocol
		binary.BigEndian.PutUint16(b[2:4], uint16(header+len(p.raw)))
		copy(b[12:16], p.source.AsSlice())
		copy(b[16:20], p.target.AsSlice())
		binary.BigEndian.PutUint16(b[10:12], internetChecksum(b[:20]))
	} else {
		b[0], b[6], b[7] = 0x60, p.protocol, 1
		binary.BigEndian.PutUint16(b[4:6], uint16(len(p.raw)))
		copy(b[8:24], p.source.AsSlice())
		copy(b[24:40], p.target.AsSlice())
	}
	copy(b[header:], p.raw[:n])
	return b
}
func traceICMP(t *testing.T, p tracePacket, router netip.Addr, kind icmp.Type, code int, body icmp.MessageBody) []byte {
	t.Helper()
	var pseudo []byte
	if p.target.Is6() {
		pseudo = icmp.IPv6PseudoHeader(net.IP(router.AsSlice()), net.IP(p.source.AsSlice()))
	}
	b, err := (&icmp.Message{Type: kind, Code: code, Body: body}).Marshal(pseudo)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func ipPeer(ip netip.Addr) net.Addr { return &net.IPAddr{IP: net.IP(ip.AsSlice())} }

func TestTraceICMPMatchesQuotedPacketAndEcho(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		for _, tcp := range []bool{false, true} {
			s := traceTestSocket(v6, tcp)
			p, err := s.request(7)
			if err != nil {
				t.Fatal(err)
			}
			router := netip.MustParseAddr("10.0.0.1")
			var exceeded, unreachable, echo icmp.Type = ipv4.ICMPTypeTimeExceeded, ipv4.ICMPTypeDestinationUnreachable, ipv4.ICMPTypeEchoReply
			if v6 {
				router = netip.MustParseAddr("fe80::1")
				exceeded, unreachable, echo = ipv6.ICMPTypeTimeExceeded, ipv6.ICMPTypeDestinationUnreachable, ipv6.ICMPTypeEchoReply
			}
			for _, n := range []int{8, len(p.raw)} {
				q := quoteTrace(p, n)
				raw := traceICMP(t, p, router, exceeded, 0, &icmp.TimeExceeded{Data: q})
				r, ok := p.matchICMP(raw, ipPeer(router))
				if !ok || r.reached || r.terminal || r.kind != "time_exceeded" || r.address != router {
					t.Fatal(v6, tcp, r, ok)
				}
				raw[2] ^= 1
				if _, ok := p.matchICMP(raw, ipPeer(router)); ok {
					t.Fatal("bad checksum accepted")
				}
				raw = traceICMP(t, p, router, exceeded, 1, &icmp.TimeExceeded{Data: q})
				if _, ok := p.matchICMP(raw, ipPeer(router)); ok {
					t.Fatal("fragment timeout treated as hop")
				}
				raw = traceICMP(t, p, router, unreachable, 1, &icmp.DstUnreach{Data: q})
				r, ok = p.matchICMP(raw, ipPeer(router))
				if !ok || r.reached || !r.terminal || r.kind != "destination_unreachable" {
					t.Fatal(r, ok)
				}
			}
			if !tcp {
				raw := traceICMP(t, p, p.target, echo, 0, &icmp.Echo{ID: int(binary.BigEndian.Uint16(s.nonce)), Seq: 7, Data: s.nonce})
				r, ok := p.matchICMP(raw, ipPeer(p.target))
				if !ok || !r.reached || !r.terminal {
					t.Fatal(r, ok)
				}
				if _, ok := p.matchICMP(raw, ipPeer(router)); ok {
					t.Fatal("wrong echo peer")
				}
				old, _ := s.request(8)
				if _, ok := old.matchICMP(raw, ipPeer(p.target)); ok {
					t.Fatal("wrong echo sequence")
				}
				mutated := bytes.Clone(s.nonce)
				mutated[31]++
				raw = traceICMP(t, p, p.target, echo, 0, &icmp.Echo{ID: int(binary.BigEndian.Uint16(s.nonce)), Seq: 7, Data: mutated})
				if _, ok := p.matchICMP(raw, ipPeer(p.target)); ok {
					t.Fatal("wrong nonce")
				}
			}
		}
	}
}

func TestTraceRejectsUnrelatedQuotesAndMalformedHeaders(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		for _, tcp := range []bool{false, true} {
			s := traceTestSocket(v6, tcp)
			p, _ := s.request(7)
			q := quoteTrace(p, 8)
			header := 20
			if v6 {
				header = 40
			}
			for n := 0; n < len(q); n++ {
				if p.matchesQuote(q[:n]) {
					t.Fatal("truncated quote accepted", v6, tcp, n)
				}
			}
			offsets := []int{0, 9, 12, 16, header, header + 2, header + 4, header + 7}
			if v6 {
				offsets = []int{0, 6, 8, 24, header, header + 2, header + 4, header + 7}
			}
			for _, offset := range offsets {
				b := bytes.Clone(q)
				b[offset] ^= 0x10
				if p.matchesQuote(b) {
					t.Fatal("unrelated quote", v6, tcp, offset)
				}
			}
			b := bytes.Clone(q)
			if v6 {
				binary.BigEndian.PutUint16(b[4:6], 7)
			} else {
				binary.BigEndian.PutUint16(b[2:4], 27)
			}
			if p.matchesQuote(b) {
				t.Fatal("short IP payload accepted")
			}
			if !v6 {
				b = bytes.Clone(q)
				b[6] = 0x20
				if p.matchesQuote(b) {
					t.Fatal("fragment accepted")
				}
				// Quoted IPv4 options move the transport header; never assume 20 bytes.
				b = append(bytes.Clone(q[:20]), append(make([]byte, 4), q[20:]...)...)
				b[0] = 0x46
				binary.BigEndian.PutUint16(b[2:4], uint16(24+len(p.raw)))
				if !p.matchesQuote(b) {
					t.Fatal("IPv4 options rejected")
				}
			} else {
				for _, kind := range []byte{0, 43, 60, 44, 51} {
					size := 8
					if kind == 51 {
						size = 12
					}
					b = append(bytes.Clone(q[:40]), append(make([]byte, size), q[40:]...)...)
					b[6], b[40] = kind, p.protocol
					if kind == 51 {
						b[41] = 1
					}
					binary.BigEndian.PutUint16(b[4:6], uint16(size+len(p.raw)))
					if !p.matchesQuote(b) {
						t.Fatal("IPv6 extension rejected", kind)
					}
					if kind == 44 {
						b[43] = 1
					} else {
						b[41] = 255
					}
					if p.matchesQuote(b) {
						t.Fatal("malformed/fragment extension accepted", kind)
					}
				}
			}
		}
	}
}

func traceTCPReply(p tracePacket, flags byte) []byte {
	b := tcpTracePacket(p.target, p.source, binary.BigEndian.Uint16(p.raw[2:4]), binary.BigEndian.Uint16(p.raw[:2]), 123, flags)
	binary.BigEndian.PutUint32(b[8:12], binary.BigEndian.Uint32(p.raw[4:8])+1)
	repairTCPChecksum(p, b)
	return b
}
func repairTCPChecksum(p tracePacket, b []byte) {
	b[16], b[17] = 0, 0
	binary.BigEndian.PutUint16(b[16:18], internetChecksum(transportPacket(p.target, p.source, 6, b)))
}
func TestTraceTCPRequiresEndpointsACKFlagsAndChecksum(t *testing.T) {
	if internetChecksum([]byte("123456789")) != 0xf62a {
		t.Fatal("checksum vector")
	}
	for _, v6 := range []bool{false, true} {
		s := traceTestSocket(v6, true)
		p, _ := s.request(1)
		next, _ := s.request(2)
		if !validChecksum(transportPacket(p.source, p.target, 6, p.raw)) || bytes.Equal(p.raw[:8], next.raw[:8]) {
			t.Fatal("invalid SYN")
		}
		for _, flags := range []byte{0x12, 0x14} {
			b := traceTCPReply(p, flags)
			r, ok := p.matchTCP(b, ipPeer(p.target))
			if !ok || !r.reached || !r.terminal {
				t.Fatal(r, ok)
			}
			if flags == 0x14 && r.kind != "tcp_reset" {
				t.Fatal(r)
			}
			if _, ok := p.matchTCP(b, ipPeer(p.source)); ok {
				t.Fatal("wrong peer")
			}
			if _, ok := next.matchTCP(b, ipPeer(p.target)); ok {
				t.Fatal("old ACK")
			}
			for _, offset := range []int{0, 2, 8, 11, 12, 13} {
				bad := bytes.Clone(b)
				bad[offset] ^= 1
				if offset == 12 {
					bad[12] = 0xf0
				}
				repairTCPChecksum(p, bad)
				if _, ok := p.matchTCP(bad, ipPeer(p.target)); ok {
					t.Fatal("bad TCP field", offset)
				}
			}
			b[16] ^= 1
			if _, ok := p.matchTCP(b, ipPeer(p.target)); ok {
				t.Fatal("bad TCP checksum")
			}
		}
	}
}

func FuzzTracePacketParsers(f *testing.F) {
	f.Add([]byte{0x45, 0, 0, 28})
	f.Add(make([]byte, 128))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, v6 := range []bool{false, true} {
			s := traceTestSocket(v6, true)
			p, _ := s.request(1)
			_, _ = p.matchICMP(b, ipPeer(p.target))
			_, _ = p.matchTCP(b, ipPeer(p.target))
			_ = p.matchesQuote(b)
		}
	})
}

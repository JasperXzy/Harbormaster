package netprobe

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type tracePacket struct {
	source, target netip.Addr
	protocol       byte
	raw            []byte
}

func packetPeer(peer net.Addr, v4 bool) (netip.Addr, bool) {
	p, ok := peer.(*net.IPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	ip, ok := netip.AddrFromSlice(p.IP)
	ip = ip.Unmap()
	// Routers may legitimately reply from private/link-local addresses. They
	// are observations only: never resolve or send a follow-up probe to them.
	return ip, ok && ip.Is4() == v4 && !ip.IsUnspecified() && !ip.IsMulticast()
}

func (p tracePacket) matchICMP(raw []byte, peer net.Addr) (traceReply, bool) {
	ip, ok := packetPeer(peer, p.target.Is4())
	if !ok || len(raw) < 8 {
		return traceReply{}, false
	}
	proto := 58
	if p.target.Is4() {
		proto = 1
		if !validChecksum(raw) {
			return traceReply{}, false
		}
	} else if !validChecksum(transportPacket(ip, p.source, 58, raw)) {
		return traceReply{}, false
	}
	m, err := icmp.ParseMessage(proto, raw)
	if err != nil {
		return traceReply{}, false
	}
	typ, code := int(raw[0]), m.Code
	r := traceReply{address: ip, icmpType: &typ, icmpCode: &code}
	if (m.Type == ipv4.ICMPTypeEchoReply || m.Type == ipv6.ICMPTypeEchoReply) && p.protocol != 6 {
		if ip != p.target || code != 0 || len(raw) != len(p.raw) || !bytes.Equal(raw[4:], p.raw[4:]) {
			return traceReply{}, false
		}
		r.kind, r.reached, r.terminal = "echo_reply", true, true
		return r, true
	}
	var quote []byte
	switch {
	case m.Type == ipv4.ICMPTypeTimeExceeded || m.Type == ipv6.ICMPTypeTimeExceeded:
		if code != 0 {
			return traceReply{}, false
		}
		b, ok := m.Body.(*icmp.TimeExceeded)
		if !ok {
			return traceReply{}, false
		}
		quote = b.Data
		r.kind = "time_exceeded"
	case m.Type == ipv4.ICMPTypeDestinationUnreachable || m.Type == ipv6.ICMPTypeDestinationUnreachable:
		if p.target.Is4() && code > 15 || p.target.Is6() && code > 7 {
			return traceReply{}, false
		}
		b, ok := m.Body.(*icmp.DstUnreach)
		if !ok {
			return traceReply{}, false
		}
		quote = b.Data
		r.kind, r.terminal = "destination_unreachable", true
	case m.Type == ipv6.ICMPTypePacketTooBig:
		if code != 0 {
			return traceReply{}, false
		}
		b, ok := m.Body.(*icmp.PacketTooBig)
		if !ok {
			return traceReply{}, false
		}
		quote = b.Data
		r.kind, r.terminal = "packet_too_big", true
	case m.Type == ipv4.ICMPTypeParameterProblem || m.Type == ipv6.ICMPTypeParameterProblem:
		if code > 2 {
			return traceReply{}, false
		}
		b, ok := m.Body.(*icmp.ParamProb)
		if !ok {
			return traceReply{}, false
		}
		quote = b.Data
		r.kind, r.terminal = "parameter_problem", true
	default:
		return traceReply{}, false
	}
	if !p.matchesQuote(quote) {
		return traceReply{}, false
	}
	return r, true
}

func (p tracePacket) matchesQuote(quote []byte) bool {
	data, ok := p.quotedPayload(quote)
	if !ok || len(data) < 8 {
		return false
	}
	if p.protocol == 6 {
		// RFC 792's minimum quote includes both ports and the 32-bit SYN ISN.
		return bytes.Equal(data[:8], p.raw[:8])
	}
	// Echo includes the random payload's checksum, ID and unique sequence even
	// in the minimum quote. Compare the nonce prefix whenever more is returned.
	n := min(len(data), len(p.raw))
	return bytes.Equal(data[:n], p.raw[:n])
}

func (p tracePacket) quotedPayload(b []byte) ([]byte, bool) {
	if len(b) < 1 {
		return nil, false
	}
	if p.target.Is4() {
		if len(b) < 20 || b[0]>>4 != 4 {
			return nil, false
		}
		h := int(b[0]&15) * 4
		total := int(binary.BigEndian.Uint16(b[2:4]))
		if h < 20 || len(b) < h+8 || total < h+8 || b[9] != p.protocol || binary.BigEndian.Uint16(b[6:8])&0x3fff != 0 {
			return nil, false
		}
		src, dst := netip.AddrFrom4([4]byte(b[12:16])), netip.AddrFrom4([4]byte(b[16:20]))
		if src != p.source || dst != p.target {
			return nil, false
		}
		return b[h:min(len(b), total)], true
	}
	if len(b) < 48 || b[0]>>4 != 6 {
		return nil, false
	}
	src, dst := netip.AddrFrom16([16]byte(b[8:24])), netip.AddrFrom16([16]byte(b[24:40]))
	if src != p.source || dst != p.target {
		return nil, false
	}
	total := 40 + int(binary.BigEndian.Uint16(b[4:6]))
	if total < 48 {
		return nil, false
	} // No jumbo packets are emitted.
	b = b[:min(len(b), total)]
	next, offset := b[6], 40
	for headers := 0; headers <= 8; headers++ {
		if next == p.protocol {
			return b[offset:], len(b)-offset >= 8
		}
		if len(b)-offset < 8 {
			return nil, false
		}
		length := 0
		switch next {
		case 0, 43, 60:
			length = (int(b[offset+1]) + 1) * 8
		case 44:
			if binary.BigEndian.Uint16(b[offset+2:offset+4]) != 0 {
				return nil, false
			}
			length = 8 // Only atomic fragments; never reassemble arbitrary packets.
		case 51:
			length = (int(b[offset+1]) + 2) * 4
			if length < 12 {
				return nil, false
			}
		default:
			return nil, false
		}
		if length < 8 || length > len(b)-offset {
			return nil, false
		}
		next, offset = b[offset], offset+length
	}
	return nil, false
}

func (p tracePacket) matchTCP(raw []byte, peer net.Addr) (traceReply, bool) {
	ip, ok := packetPeer(peer, p.target.Is4())
	if !ok || ip != p.target || len(raw) < 20 || int(raw[12]>>4)*4 < 20 || int(raw[12]>>4)*4 > len(raw) {
		return traceReply{}, false
	}
	if !bytes.Equal(raw[:2], p.raw[2:4]) || !bytes.Equal(raw[2:4], p.raw[:2]) || binary.BigEndian.Uint32(raw[8:12]) != binary.BigEndian.Uint32(p.raw[4:8])+1 {
		return traceReply{}, false
	}
	if !validChecksum(transportPacket(p.target, p.source, 6, raw)) {
		return traceReply{}, false
	}
	kind := ""
	switch raw[13] & 0x3f {
	case 0x12:
		kind = "tcp_syn_ack"
	case 0x14:
		kind = "tcp_reset"
	default:
		return traceReply{}, false
	}
	return traceReply{address: ip, kind: kind, reached: true, terminal: true}, true
}

// transportPacket prefixes the standard IPv4/IPv6 checksum pseudo-header.
func transportPacket(src, dst netip.Addr, protocol byte, raw []byte) []byte {
	var b []byte
	if src.Is4() {
		b = make([]byte, 12, len(raw)+12)
		copy(b[:4], src.AsSlice())
		copy(b[4:8], dst.AsSlice())
		b[9] = protocol
		binary.BigEndian.PutUint16(b[10:12], uint16(len(raw)))
	} else {
		b = make([]byte, 40, len(raw)+40)
		copy(b[:16], src.AsSlice())
		copy(b[16:32], dst.AsSlice())
		binary.BigEndian.PutUint32(b[32:36], uint32(len(raw)))
		b[39] = protocol
	}
	return append(b, raw...)
}
func internetChecksum(b []byte) uint16 {
	var sum uint32
	for len(b) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) > 0 {
		sum += uint32(b[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
func tcpTracePacket(src, dst netip.Addr, sourcePort, targetPort uint16, seq uint32, flags byte) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b[:2], sourcePort)
	binary.BigEndian.PutUint16(b[2:4], targetPort)
	binary.BigEndian.PutUint32(b[4:8], seq)
	b[12], b[13] = 0x50, flags
	binary.BigEndian.PutUint16(b[14:16], 1024)
	binary.BigEndian.PutUint16(b[16:18], internetChecksum(transportPacket(src, dst, 6, b)))
	return b
}

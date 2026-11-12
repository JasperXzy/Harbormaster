package netprobe

import (
	"math"
	"net/netip"
	"time"
)

// ValidateTraceResult validates structure, not remote measurement truth. The
// server derives reached/stop_reason from accepted samples, never agent claims.
func ValidateTraceResult(q TraceRequest, r *TraceResult, code *string, now time.Time) error {
	if q.Validate() != nil || r == nil || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || r.FinishedAt.After(now.Add(2*time.Minute)) || r.FinishedAt.Sub(r.StartedAt) > 127*time.Second || len(r.Hops) > q.MaxHops || len(r.ResolvedAddresses) > 64 {
		return ErrInvalid
	}
	if code != nil {
		switch *code {
		case "invalid_probe", "probe_target_not_public", "probe_dns_failed", "probe_unavailable", "probe_io_failed", "probe_cancelled", "probe_interrupted":
		default:
			return ErrInvalid
		}
	}
	if r.Address == "" {
		if code == nil || len(r.Hops) != 0 || len(r.ResolvedAddresses) != 0 {
			return ErrInvalid
		}
	} else {
		ip, err := netip.ParseAddr(r.Address)
		if err != nil || !PublicAddress(ip) || ip.Is4() != (q.Family == "ipv4") || len(r.ResolvedAddresses) == 0 || r.ResolvedAddresses[0] != r.Address {
			return ErrInvalid
		}
		if literal, err := netip.ParseAddr(q.Host); err == nil && literal != ip {
			return ErrInvalid
		}
		seen := map[netip.Addr]bool{}
		for _, address := range r.ResolvedAddresses {
			a, err := netip.ParseAddr(address)
			if err != nil || !PublicAddress(a) || a.Is4() != (q.Family == "ipv4") || seen[a] {
				return ErrInvalid
			}
			seen[a] = true
		}
	}
	reached, terminal := false, false
	previous, seq := r.StartedAt, 0
	for i, hop := range r.Hops {
		if hop.TTL != i+1 || len(hop.Samples) > q.Count || (code == nil || i < len(r.Hops)-1) && len(hop.Samples) != q.Count || terminal {
			return ErrInvalid
		}
		for _, sample := range hop.Samples {
			seq++
			if sample.Sequence != seq || sample.StartedAt.Before(previous) || sample.StartedAt.After(r.FinishedAt) {
				return ErrInvalid
			}
			previous = sample.StartedAt
			if sample.ErrorCode != nil {
				if *sample.ErrorCode != "probe_timeout" || sample.Address != nil || sample.RTTMS != nil || sample.Reply != nil || sample.ICMPType != nil || sample.ICMPCode != nil {
					return ErrInvalid
				}
				continue
			}
			if sample.Address == nil || sample.RTTMS == nil || sample.Reply == nil || math.IsNaN(*sample.RTTMS) || math.IsInf(*sample.RTTMS, 0) || *sample.RTTMS < 0 || *sample.RTTMS > float64(q.TimeoutMS)+1000 {
				return ErrInvalid
			}
			address, err := netip.ParseAddr(*sample.Address)
			if err != nil || address.Is4() != (q.Family == "ipv4") || address.Is4In6() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
				return ErrInvalid
			}
			hit, end, valid := validTraceReply(q, sample)
			if !valid || hit && address.String() != r.Address {
				return ErrInvalid
			}
			reached = reached || hit
			terminal = terminal || end
		}
	}
	if code == nil && (len(r.Hops) == 0 || !terminal && len(r.Hops) != q.MaxHops) {
		return ErrInvalid
	}
	r.Reached = reached
	switch {
	case code != nil:
		r.StopReason = "incomplete"
	case reached:
		r.StopReason = "reached"
	case terminal:
		r.StopReason = "unreachable"
	default:
		r.StopReason = "max_hops"
	}
	return nil
}

func validTraceReply(q TraceRequest, s TraceSample) (reached, terminal, valid bool) {
	if *s.Reply == "tcp_syn_ack" || *s.Reply == "tcp_reset" {
		return true, true, q.Method == "tcp" && s.ICMPType == nil && s.ICMPCode == nil
	}
	if s.ICMPType == nil || s.ICMPCode == nil || *s.ICMPCode < 0 {
		return false, false, false
	}
	typ, code := *s.ICMPType, *s.ICMPCode
	echo, exceeded, unreachable, parameter, maxCode := 0, 11, 3, 12, 15
	if q.Family == "ipv6" {
		echo, exceeded, unreachable, parameter, maxCode = 129, 3, 1, 4, 7
	}
	switch *s.Reply {
	case "echo_reply":
		return true, true, q.Method == "icmp" && typ == echo && code == 0
	case "time_exceeded":
		return false, false, typ == exceeded && code == 0
	case "destination_unreachable":
		return false, true, typ == unreachable && code <= maxCode
	case "parameter_problem":
		return false, true, typ == parameter && code <= 2
	case "packet_too_big":
		return false, true, q.Family == "ipv6" && typ == 2 && code == 0
	default:
		return false, false, false
	}
}

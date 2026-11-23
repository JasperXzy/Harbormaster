package netprobe

import (
	"math"
	"net/netip"
	"time"
)

// ValidateResult checks an authenticated agent's structure, not the truth of
// measurements on a remote host. Summary is recomputed from raw attempts.
func ValidateResult(q Request, r *Result, code *string, now time.Time) error {
	if q.Validate() != nil || r == nil || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || r.FinishedAt.After(now.Add(2*time.Minute)) || r.FinishedAt.Sub(r.StartedAt) > 67*time.Second || len(r.Samples) > q.Count || len(r.ResolvedAddresses) > 64 {
		return ErrInvalid
	}
	if code != nil {
		switch *code {
		case "invalid_probe", "probe_target_not_public", "probe_dns_failed", "probe_unavailable", "probe_io_failed", "probe_cancelled", "probe_interrupted":
		default:
			return ErrInvalid
		}
	} else if len(r.Samples) != q.Count {
		return ErrInvalid
	}
	if r.Address == "" {
		if code == nil || len(r.Samples) != 0 || len(r.ResolvedAddresses) != 0 {
			return ErrInvalid
		}
	} else {
		ip, err := netip.ParseAddr(r.Address)
		if err != nil || !PublicAddress(ip) || ip.Is4() != (q.Family == "ipv4") {
			return ErrInvalid
		}
		if literal, err := netip.ParseAddr(q.Host); err == nil && literal != ip {
			return ErrInvalid
		}
		found := false
		for _, raw := range r.ResolvedAddresses {
			a, err := netip.ParseAddr(raw)
			if err != nil || !PublicAddress(a) || a.Is4() != (q.Family == "ipv4") {
				return ErrInvalid
			}
			if a == ip {
				found = true
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	previous := r.StartedAt
	for i, s := range r.Samples {
		if s.Sequence != i+1 || s.StartedAt.Before(previous) || s.StartedAt.After(r.FinishedAt) || (s.RTTMS == nil) == (s.ErrorCode == nil) {
			return ErrInvalid
		}
		if s.RTTMS != nil {
			if math.IsNaN(*s.RTTMS) || math.IsInf(*s.RTTMS, 0) || *s.RTTMS < 0 || *s.RTTMS > float64(q.TimeoutMS)+1000 {
				return ErrInvalid
			}
		} else {
			switch *s.ErrorCode {
			case "probe_timeout", "probe_connection_refused", "probe_network_unreachable":
			default:
				return ErrInvalid
			}
		}
		previous = s.StartedAt
	}
	r.Summary = nil
	if code == nil {
		stats := summarize(r.Samples)
		r.Summary = &stats
	}
	return nil
}

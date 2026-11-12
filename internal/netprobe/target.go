package netprobe

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

// Special-use destinations are not probe targets. The IPv6 global-unicast
// allocation is explicit, excluding translation/tunneling and special ranges.
// This is independent of the traffic counter's on-host metering exclusions.
var excluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}
var globalIPv6 = netip.MustParsePrefix("2000::/3")

func PublicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.Is6() && !globalIPv6.Contains(ip) {
		return false
	}
	for _, prefix := range excluded {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func normalizeHost(host string) (string, error) {
	if len(host) == 0 || len(host) > 253 || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/%\\@?#[]\x00\r\n\t") {
		return "", ErrInvalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicAddress(ip) {
			return "", ErrTarget
		}
		return ip.String(), nil
	}
	if strings.Contains(host, ":") {
		return "", ErrInvalid
	}
	normalized, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || len(normalized) > 253 {
		return "", ErrInvalid
	}
	for _, label := range strings.Split(normalized, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalid
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return "", ErrInvalid
			}
		}
	}
	return strings.ToLower(normalized), nil
}
func resolve(ctx context.Context, q Request, lookup func(context.Context, string, string) ([]netip.Addr, error)) ([]netip.Addr, error) {
	host, err := normalizeHost(q.Host)
	if err != nil {
		return nil, err
	}
	if ip, e := netip.ParseAddr(host); e == nil {
		if ip.Is4() != (q.Family == "ipv4") {
			return nil, ErrInvalid
		}
		return []netip.Addr{ip}, nil
	}
	dnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Request all answers so a mixed public/private hostname is rejected even
	// when the private answer belongs to a different family than this run.
	addresses, err := lookup(dnsCtx, "ip", host)
	if ctx.Err() != nil {
		return nil, ErrCancelled
	}
	if err != nil || len(addresses) == 0 || len(addresses) > 64 {
		return nil, ErrDNS
	}
	chosen := []netip.Addr{}
	seen := map[netip.Addr]bool{}
	for _, ip := range addresses {
		// Go resolvers can represent A records as IPv4-mapped netip values.
		ip = ip.Unmap()
		if !PublicAddress(ip) {
			return nil, ErrTarget
		}
		if ip.Is4() == (q.Family == "ipv4") && !seen[ip] {
			chosen = append(chosen, ip)
			seen[ip] = true
		}
	}
	if len(chosen) == 0 {
		return nil, ErrDNS
	}
	// Preserve resolver order, pin its first eligible address for the whole run.
	// Every connect uses the numeric address; no second DNS lookup or fallback.
	return chosen, nil
}

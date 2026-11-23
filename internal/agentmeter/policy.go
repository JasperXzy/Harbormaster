// Package agentmeter counts packets at an explicitly selected public boundary.
// It never accepts rules, shell commands or executable paths from the server.
package agentmeter

import (
	"errors"
	"net/netip"
	"regexp"
	"sort"
)

const RulesVersion = "nft-public-v1"
const TableName = "harbormaster"

var ErrUnsupported = errors.New("agent_requires_linux")
var ErrPolicy = errors.New("agent_counter_policy_mismatch")
var ErrRead = errors.New("agent_counter_read_failed")

// Counters belong to a kernel generation, retained across process restarts.
// Kernel recreation or reboot creates a new generation; callers must then use
// a new report epoch and never subtract across the boundary.
type Sample struct {
	Generation                     string
	UptimeMS                       int64
	IPv4RX, IPv4TX, IPv6RX, IPv6TX int64
}

var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func Interfaces(names []string) ([]string, error) {
	if len(names) == 0 || len(names) > 8 {
		return nil, errors.New("explicit_public_interfaces_required")
	}
	out := append([]string(nil), names...)
	sort.Strings(out)
	for i, name := range out {
		if !interfacePattern.MatchString(name) || name == "lo" || (i > 0 && name == out[i-1]) {
			return nil, errors.New("invalid_public_interface")
		}
	}
	return out, nil
}

// Local/link-local, CGNAT, multicast and reserved address ranges must never be
// classified as public peers. The same list builds the kernel exclusions.
func Exclusions(ipv6 bool) []netip.Prefix {
	raw := []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"}
	if ipv6 {
		raw = []string{"::/128", "::1/128", "::ffff:0:0/96", "fc00::/7", "fe80::/10", "ff00::/8"}
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}
func PublicPeer(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Is4In6() {
		return false
	}
	for _, p := range Exclusions(addr.Is6()) {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

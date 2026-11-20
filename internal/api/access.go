package api

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/config"
)

type panelAccess struct {
	origin, host     string
	trusted, allowed []netip.Prefix
}

func newPanelAccess(c config.Config) panelAccess {
	u, _ := url.Parse(c.PublicURL)
	a := panelAccess{origin: strings.TrimSuffix(c.PublicURL, "/")}
	if u != nil {
		a.host = u.Host
	}
	for _, item := range c.TrustedProxies {
		if p, err := netip.ParsePrefix(item); err == nil {
			a.trusted = append(a.trusted, p)
		}
	}
	for _, item := range c.AdminAllowCIDRs {
		if p, err := netip.ParsePrefix(item); err == nil {
			a.allowed = append(a.allowed, p)
		}
	}
	return a
}

func within(ip netip.Addr, ranges []netip.Prefix) bool {
	for _, p := range ranges {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// Forwarded headers are meaningful only from an explicitly trusted immediate
// peer. Walk XFF right-to-left; an attacker-supplied leftmost address is not proof.
func (a panelAccess) source(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	trusted := within(peer, a.trusted)
	if !trusted {
		return peer.Unmap(), false
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	ip := peer
	for i := len(chain) - 1; i >= 0 && within(ip, a.trusted); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return netip.Addr{}, true
		}
		ip = next.Unmap()
	}
	return ip.Unmap(), true
}

func (a panelAccess) checkPublicTransport(w http.ResponseWriter, r *http.Request) (string, bool) {
	ip, proxied := a.source(r)
	secure := r.TLS != nil || (proxied && r.Header.Get("X-Forwarded-Proto") == "https")
	if a.origin == "" || !ip.IsValid() || !secure || !strings.EqualFold(r.Host, a.host) {
		writeError(w, 403, "forbidden")
		return "", false
	}
	return ip.String(), true
}

func (a panelAccess) checkTransport(w http.ResponseWriter, r *http.Request) (string, bool) {
	ip, proxied := a.source(r)
	secure := r.TLS != nil || (proxied && r.Header.Get("X-Forwarded-Proto") == "https")
	if a.origin == "" || !ip.IsValid() || !secure || !strings.EqualFold(r.Host, a.host) || (len(a.allowed) > 0 && !within(ip, a.allowed)) {
		writeError(w, 403, "forbidden")
		return "", false
	}
	return ip.String(), true
}

func (a panelAccess) check(w http.ResponseWriter, r *http.Request) (string, bool) {
	source, ok := a.checkTransport(w, r)
	if !ok {
		return "", false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("Origin") != a.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, 403, "csrf_failed")
			return "", false
		}
	}
	return source, true
}

package agentmeter

import (
	"net/netip"
	"testing"
)

func TestPeerBoundaries(t *testing.T) {
	for _, raw := range []string{"0.0.0.0", "10.0.0.1", "10.255.255.255", "100.64.0.0", "100.127.255.255", "127.0.0.1", "169.254.1.1", "172.16.0.0", "172.31.255.255", "192.168.1.1", "224.0.0.1", "255.255.255.255", "::", "::1", "fe80::1", "fc00::1", "fdff::1", "ff02::1", "::ffff:8.8.8.8"} {
		if PublicPeer(netip.MustParseAddr(raw)) {
			t.Errorf("private peer counted: %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "100.63.255.255", "100.128.0.0", "172.15.255.255", "172.32.0.0", "2001:4860:4860::8888", "2408:4000::1"} {
		if !PublicPeer(netip.MustParseAddr(raw)) {
			t.Errorf("public peer excluded: %s", raw)
		}
	}
}
func TestInterfacesMustBeExplicitAndUnambiguous(t *testing.T) {
	for _, names := range [][]string{nil, {"lo"}, {"eth0", "eth0"}, {"eth0;sh"}, {"eth0\x00"}} {
		if _, err := Interfaces(names); err == nil {
			t.Fatal(names)
		}
	}
	if names, err := Interfaces([]string{"ens5", "eth0"}); err != nil || names[0] != "ens5" {
		t.Fatal(names, err)
	}
}

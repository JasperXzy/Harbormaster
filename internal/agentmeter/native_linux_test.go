//go:build linux

package agentmeter

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/nftables"
	"golang.org/x/sys/unix"
)

// Opt-in only, and never runs in PID 1's network namespace. Build with go test
// -c and invoke through `sudo unshare --net` in an approved test environment.
// All links, routes and tables disappear when the private namespace exits.
func TestKernelCounters(t *testing.T) {
	if os.Getenv("HM_AGENT_KERNEL_TEST") != "1" {
		t.Skip("requires explicit isolated Linux kernel verification")
	}
	current, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Readlink("/proc/1/ns/net")
	if err != nil || current == host {
		t.Fatal("refusing test in host network namespace")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated namespace requires root/CAP_NET_ADMIN")
	}
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip setup failed: %v: %s", err, out)
		}
	}
	ip("link", "add", "hmtest0", "type", "veth", "peer", "name", "hmpeer0")
	ip("link", "set", "hmtest0", "up")
	ip("link", "set", "hmpeer0", "up")
	ip("addr", "add", "10.231.0.2/24", "dev", "hmtest0")
	ip("-6", "addr", "add", "fd00:231::2/64", "dev", "hmtest0", "nodad")
	boundary, err := net.InterfaceByName("hmtest0")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.InterfaceByName("hmpeer0")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := &nftables.Table{Name: "hm_unrelated_test", Family: nftables.TableFamilyINet}
	conn := &nftables.Conn{}
	conn.AddTable(sentinel)
	if err = conn.Flush(); err != nil {
		t.Fatal(err)
	}
	meter, err := Open([]string{"hmtest0"})
	if err != nil {
		t.Fatal("create rules", err)
	}
	initial, err := meter.Read()
	if err != nil {
		t.Fatal("read and validate freshly created rules", err)
	}
	if initial.IPv4RX+initial.IPv4TX+initial.IPv6RX+initial.IPv6TX != 0 {
		t.Fatal("unexpected initial traffic", initial)
	}
	read := func() Sample {
		t.Helper()
		s, err := meter.Read()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	wait := func(expect func(Sample) bool) Sample {
		t.Helper()
		end := time.Now().Add(3 * time.Second)
		for {
			s := read()
			if expect(s) {
				time.Sleep(50 * time.Millisecond)
				return read()
			}
			if time.Now().After(end) {
				t.Fatal("counter update timed out", s)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	sendFrame := func(packet []byte, v6 bool) {
		t.Helper()
		fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ALL)))
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		frame := make([]byte, 14+len(packet))
		copy(frame[:6], boundary.HardwareAddr)
		copy(frame[6:12], peer.HardwareAddr)
		proto := uint16(unix.ETH_P_IP)
		if v6 {
			proto = unix.ETH_P_IPV6
		}
		binary.BigEndian.PutUint16(frame[12:14], proto)
		copy(frame[14:], packet)
		if err = unix.Sendto(fd, frame, 0, &unix.SockaddrLinklayer{Ifindex: peer.Index, Protocol: htons(proto)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, v6 := range []bool{false, true} {
		source, dest, private := "1.1.1.1", "10.231.0.2", "10.99.0.1"
		if v6 {
			source, dest, private = "2001:4860::8888", "fd00:231::2", "fd00:99::1"
		}
		publicPacket := udpPacket(source, dest, v6)
		privatePacket := udpPacket(private, dest, v6)
		sendFrame(publicPacket, v6)
		sendFrame(privatePacket, v6)
		sendFrame(publicPacket, v6)
		want := int64(2 * len(publicPacket))
		result := wait(func(s Sample) bool {
			if v6 {
				return s.IPv6RX >= want
			}
			return s.IPv4RX >= want
		})
		actual := result.IPv4RX
		if v6 {
			actual = result.IPv6RX
		}
		if actual != want {
			t.Fatalf("public ingress or private exclusion incorrect (ipv6=%v): got %d want %d", v6, actual, want)
		}
	}
	// Fixed neighbors avoid ARP/NDP delays; the peer has no public IP or service.
	for _, v6 := range []bool{false, true} {
		public, private, prefix, privatePrefix := "198.51.100.1", "10.232.0.1", "198.51.100.0/24", "10.232.0.0/24"
		family := []string{}
		network := "udp4"
		if v6 {
			public, private, prefix, privatePrefix = "2001:db8:232::1", "fd00:232::1", "2001:db8:232::/64", "fd00:232::/64"
			family = []string{"-6"}
			network = "udp6"
		}
		for _, route := range []string{prefix, privatePrefix} {
			ip(append(append([]string{}, family...), "route", "add", route, "dev", "hmtest0")...)
		}
		for _, addr := range []string{public, private} {
			ip(append(append([]string{}, family...), "neigh", "replace", addr, "lladdr", peer.HardwareAddr.String(), "dev", "hmtest0", "nud", "permanent")...)
		}
		for _, addr := range []string{public, private, public} {
			socket, err := net.DialUDP(network, nil, &net.UDPAddr{IP: net.ParseIP(addr), Port: 45001})
			if err != nil {
				t.Fatal(err)
			}
			_, err = socket.Write([]byte("hm-kernel-test"))
			socket.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		header := 28
		if v6 {
			header = 48
		}
		want := int64(2 * (header + len("hm-kernel-test")))
		result := wait(func(s Sample) bool {
			if v6 {
				return s.IPv6TX >= want
			}
			return s.IPv4TX >= want
		})
		actual := result.IPv4TX
		if v6 {
			actual = result.IPv6TX
		}
		if actual != want {
			t.Fatalf("public egress or private exclusion incorrect (ipv6=%v): got %d want %d", v6, actual, want)
		}
	}
	before := read()
	reopened, err := Open([]string{"hmtest0"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.Read()
	if err != nil {
		t.Fatal(err)
	}
	if before.Generation != after.Generation || before.IPv4RX != after.IPv4RX || before.IPv6TX != after.IPv6TX {
		t.Fatal("reopening reset kernel counters")
	}
	if err = Remove([]string{"hmtest0"}); err != nil {
		t.Fatal(err)
	}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, table := range tables {
		if table.Name == sentinel.Name {
			found = true
		}
		if table.Name == TableName {
			t.Fatal("owned table was not removed")
		}
	}
	if !found {
		t.Fatal("unrelated table was removed")
	}
	meter, err = Open([]string{"hmtest0"})
	if err != nil {
		t.Fatal(err)
	}
	reset := read()
	if reset.Generation == before.Generation || reset.IPv4RX+reset.IPv4TX+reset.IPv6RX+reset.IPv6TX != 0 {
		t.Fatal("kernel reset did not start new generation")
	}
	conn.AddChain(&nftables.Chain{Name: "foreign_change", Table: table()})
	if err = conn.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err = meter.Read(); !errors.Is(err, ErrPolicy) {
		t.Fatal("modified table was trusted", err)
	}
	if err = Remove([]string{"hmtest0"}); !errors.Is(err, ErrPolicy) {
		t.Fatal("modified table was deleted", err)
	}
	t.Logf("verified IPv4/IPv6 public ingress/egress and private exclusions; L3 counters rx4=%d tx4=%d rx6=%d tx6=%d; reopen, reset and foreign-table refusal passed", before.IPv4RX, before.IPv4TX, before.IPv6RX, before.IPv6TX)
}
func htons(v uint16) uint16 { return v<<8 | v>>8 }
func checksum(raw []byte) uint16 {
	var sum uint32
	for len(raw) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(raw))
		raw = raw[2:]
	}
	if len(raw) > 0 {
		sum += uint32(raw[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
func udpPacket(src, dst string, v6 bool) []byte {
	payload := []byte("hm-kernel-test")
	udp := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(udp, 45000)
	binary.BigEndian.PutUint16(udp[2:], 45001)
	binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
	copy(udp[8:], payload)
	if v6 {
		packet := make([]byte, 40+len(udp))
		packet[0] = 0x60
		packet[6] = 17
		packet[7] = 64
		binary.BigEndian.PutUint16(packet[4:], uint16(len(udp)))
		copy(packet[8:24], net.ParseIP(src).To16())
		copy(packet[24:40], net.ParseIP(dst).To16())
		pseudo := make([]byte, 40+len(udp))
		copy(pseudo[:32], packet[8:40])
		binary.BigEndian.PutUint32(pseudo[32:], uint32(len(udp)))
		pseudo[39] = 17
		copy(pseudo[40:], udp)
		sum := checksum(pseudo)
		if sum == 0 {
			sum = 0xffff
		}
		binary.BigEndian.PutUint16(udp[6:], sum)
		copy(packet[40:], udp)
		return packet
	}
	packet := make([]byte, 20+len(udp))
	packet[0] = 0x45
	packet[8] = 64
	packet[9] = 17
	binary.BigEndian.PutUint16(packet[2:], uint16(len(packet)))
	copy(packet[12:16], net.ParseIP(src).To4())
	copy(packet[16:20], net.ParseIP(dst).To4())
	binary.BigEndian.PutUint16(packet[10:], checksum(packet[:20]))
	copy(packet[20:], udp)
	return packet
}

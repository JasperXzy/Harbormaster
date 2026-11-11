//go:build linux

package agentmeter

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"net"
	"reflect"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

type Meter struct{ interfaces []string }
type ownership struct {
	Version    string   `json:"version"`
	Generation string   `json:"generation"`
	Interfaces []string `json:"interfaces"`
}

var counterNames = []string{"rx4", "rx6", "tx4", "tx6"}

func Open(names []string) (*Meter, error) {
	interfaces, err := Interfaces(names)
	if err != nil {
		return nil, err
	}
	for _, name := range interfaces {
		iface, e := net.InterfaceByName(name)
		if e != nil || iface.Flags&net.FlagLoopback != 0 {
			return nil, ErrPolicy
		}
	}
	m := &Meter{interfaces: interfaces}
	if _, err = m.ensure(true); err != nil {
		return nil, err
	}
	return m, nil
}
func table() *nftables.Table {
	return &nftables.Table{Name: TableName, Family: nftables.TableFamilyINet}
}
func chains(t *nftables.Table) []*nftables.Chain {
	accept := nftables.ChainPolicyAccept
	return []*nftables.Chain{
		{Name: "rx", Table: t, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityRaw, Policy: &accept},
		{Name: "tx", Table: t, Type: nftables.ChainTypeFilter, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityConntrackHelper, Policy: &accept},
	}
}
func (m *Meter) ensure(create bool) (ownership, error) {
	c := &nftables.Conn{}
	t := table()
	tables, err := c.ListTablesOfFamily(t.Family)
	if err != nil {
		return ownership{}, ErrRead
	}
	found := false
	for _, existing := range tables {
		if existing.Name == TableName {
			found = true
		}
	}
	if !found {
		if !create {
			return ownership{}, ErrPolicy
		}
		var raw [16]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return ownership{}, ErrRead
		}
		owner := ownership{Version: RulesVersion, Generation: hex.EncodeToString(raw[:]), Interfaces: m.interfaces}
		marker, _ := json.Marshal(owner)
		c.AddTable(t)
		for _, name := range counterNames {
			c.AddObj(&nftables.CounterObj{Table: t, Name: name})
		}
		for _, chain := range chains(t) {
			c.AddChain(chain)
			for _, iface := range m.interfaces {
				for _, v6 := range []bool{false, true} {
					c.AddRule(&nftables.Rule{Table: t, Chain: chain, Exprs: expressions(iface, chain.Name == "rx", v6), UserData: marker})
				}
			}
		}
		if err = c.Flush(); err != nil {
			return ownership{}, ErrRead
		}
		return owner, nil
	}
	// Never flush, replace or adopt a foreign/modified table. Compare the full
	// expression program, hooks and ownership before trusting existing counters.
	existingChains, err := c.ListChainsOfTableFamily(t.Family)
	if err != nil {
		return ownership{}, ErrRead
	}
	count := 0
	for _, chain := range existingChains {
		if chain.Table.Name == t.Name {
			count++
		}
	}
	if count != 2 {
		return ownership{}, ErrPolicy
	}
	var owner ownership
	for _, want := range chains(t) {
		got, err := c.ListChain(t, want.Name)
		if err != nil {
			return owner, ErrRead
		}
		if got.Type != want.Type || got.Hooknum == nil || got.Priority == nil || got.Policy == nil || *got.Hooknum != *want.Hooknum || *got.Priority != *want.Priority || *got.Policy != *want.Policy || got.Device != "" {
			return owner, ErrPolicy
		}
		rules, err := c.GetRules(t, got)
		if err != nil {
			return owner, ErrRead
		}
		if len(rules) != len(m.interfaces)*2 {
			return owner, ErrPolicy
		}
		for i, rule := range rules {
			var marker ownership
			if json.Unmarshal(rule.UserData, &marker) != nil || marker.Version != RulesVersion || len(marker.Generation) != 32 || !reflect.DeepEqual(marker.Interfaces, m.interfaces) {
				return owner, ErrPolicy
			}
			if _, err = hex.DecodeString(marker.Generation); err != nil {
				return owner, ErrPolicy
			}
			if owner.Generation != "" && !reflect.DeepEqual(owner, marker) {
				return owner, ErrPolicy
			}
			owner = marker
			if !sameExpressions(rule.Exprs, expressions(m.interfaces[i/2], want.Name == "rx", i%2 == 1)) {
				return owner, ErrPolicy
			}
		}
	}
	objects, err := c.GetObjects(t)
	if err != nil {
		return owner, ErrRead
	}
	if len(objects) != 4 {
		return owner, ErrPolicy
	}
	seen := map[string]bool{}
	for _, object := range objects {
		counter, ok := object.(*nftables.CounterObj)
		if !ok {
			return owner, ErrPolicy
		}
		seen[counter.Name] = true
	}
	for _, name := range counterNames {
		if !seen[name] {
			return owner, ErrPolicy
		}
	}
	return owner, nil
}
func sameExpressions(a, b []expr.Any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, err := expr.Marshal(byte(nftables.TableFamilyINet), a[i])
		if err != nil {
			return false
		}
		y, err := expr.Marshal(byte(nftables.TableFamilyINet), b[i])
		if err != nil || !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}
func expressions(iface string, rx, v6 bool) []expr.Any {
	key := expr.MetaKeyOIFNAME
	if rx {
		key = expr.MetaKeyIIFNAME
	}
	family := byte(unix.NFPROTO_IPV4)
	size := uint32(4)
	offset := uint32(16)
	name := "tx4"
	if rx {
		offset = 12
		name = "rx4"
	}
	if v6 {
		family = unix.NFPROTO_IPV6
		size = 16
		offset = 24
		name = "tx6"
		if rx {
			offset = 8
			name = "rx6"
		}
	}
	out := []expr.Any{&expr.Meta{Key: key, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: append([]byte(iface), 0)}, &expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}}}
	for _, prefix := range Exclusions(v6) {
		mask := net.CIDRMask(prefix.Bits(), int(size)*8)
		out = append(out, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: size}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: size, Mask: mask, Xor: make([]byte, size)}, &expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: prefix.Addr().AsSlice()})
	}
	return append(out, &expr.Objref{Type: int(nftables.ObjTypeCounter), Name: name})
}
func (m *Meter) Read() (Sample, error) {
	owner, err := m.ensure(true)
	if err != nil {
		return Sample{}, err
	}
	c := &nftables.Conn{}
	values := map[string]int64{}
	for _, name := range counterNames {
		object, err := c.GetObject(&nftables.CounterObj{Table: table(), Name: name})
		if err != nil {
			return Sample{}, ErrRead
		}
		counter, ok := object.(*nftables.CounterObj)
		if !ok || counter.Bytes > math.MaxInt64 {
			return Sample{}, ErrRead
		}
		values[name] = int64(counter.Bytes)
	}
	// A concurrent deletion/replacement must not mix two kernel generations.
	after, err := m.ensure(false)
	if err != nil || after.Generation != owner.Generation {
		return Sample{}, ErrPolicy
	}
	if values["rx4"] > math.MaxInt64-values["rx6"] || values["tx4"] > math.MaxInt64-values["tx6"] {
		return Sample{}, ErrRead
	}
	var boot unix.Timespec
	if err = unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot); err != nil {
		return Sample{}, ErrRead
	}
	return Sample{Generation: owner.Generation, UptimeMS: boot.Sec*1000 + boot.Nsec/1000000, IPv4RX: values["rx4"], IPv4TX: values["tx4"], IPv6RX: values["rx6"], IPv6TX: values["tx6"]}, nil
}

// Remove deletes only the exact owned table. Stop the service before rollback.
func Remove(names []string) error {
	interfaces, err := Interfaces(names)
	if err != nil {
		return err
	}
	m := &Meter{interfaces: interfaces}
	if _, err = m.ensure(false); err != nil {
		return err
	}
	c := &nftables.Conn{}
	c.DelTable(table())
	if c.Flush() != nil {
		return ErrRead
	}
	return nil
}

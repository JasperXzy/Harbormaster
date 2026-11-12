package ipinfo

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func TestBundledOfflineLookupsAndUnknowns(t *testing.T) {
	m := manager(t)
	for _, tc := range []struct {
		ip      string
		asn     uint32
		country string
	}{{"1.1.1.1", 13335, "AU"}, {"8.8.8.8", 15169, "US"}, {"2606:4700:4700::1111", 13335, "US"}} {
		r := m.Lookup(netip.MustParseAddr(tc.ip))
		if r.ASNStatus != "ok" || r.ASN == nil || r.ASN.Number != tc.asn || r.ASN.Organization == nil || r.LocationStatus != "ok" || r.Location == nil || r.Location.CountryCode == nil || *r.Location.CountryCode != tc.country {
			t.Fatal(r)
		}
		if r.Location.Granularity != "country" || r.Location.City != nil || r.Location.Region != nil || r.Location.Latitude != nil || r.Location.Longitude != nil {
			t.Fatal("country lookup invented precision", r)
		}
	}
	// The provider's ASN Lite snapshot has a real hole for this CN2 address.
	r := m.Lookup(netip.MustParseAddr("59.43.1.1"))
	if r.ASN != nil || r.ASNStatus != "not_found" || r.Location == nil || *r.Location.CountryCode != "CN" {
		t.Fatal("missing ASN fabricated", r)
	}
	for _, text := range []string{"10.0.0.1", "100.100.100.200", "127.0.0.1", "192.0.2.1", "fe80::1", "::ffff:1.1.1.1"} {
		r = m.Lookup(netip.MustParseAddr(text))
		if r.ASN != nil || r.Location != nil || r.ASNStatus != "not_public" || r.LocationStatus != "not_public" {
			t.Fatal(r)
		}
	}
	s := m.Status()
	if s.Mode != "country" || s.City != nil || s.CityEnabled || s.ASN.Edition != "2026-11" || s.Country.Edition != "2026-11" || s.ASN.SizeBytes != 9648279 || s.Country.SizeBytes != 8515063 || len(s.Attribution) != 1 || s.GoHeapAllocBytes == 0 || s.MappedFileBytes != 0 {
		t.Fatal(s)
	}
	peak := uint64(0)
	if s.ProcessPeakRSSBytes != nil {
		peak = *s.ProcessPeakRSSBytes
	}
	t.Logf("bundled raw bytes=%d; Go heap=%d; peak RSS bytes=%d", s.ASN.SizeBytes+s.Country.SizeBytes, s.GoHeapAllocBytes, peak)
}
func TestCacheFailureFallsBackToBundleAndCloseIsSafe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte(`{"files":{"asn":"../outside.mmdb"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Directory: dir, CityEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status().LastError == nil || m.Status().City != nil || m.Status().Mode != "country" || m.Lookup(netip.MustParseAddr("1.1.1.1")).ASN == nil {
		t.Fatal("bad cache defeated offline fallback")
	}
	m.Close()
	m.Close()
	if r := m.Lookup(netip.MustParseAddr("1.1.1.1")); r.ASNStatus != "unavailable" || r.LocationStatus != "unavailable" {
		t.Fatal(r)
	}
	for _, path := range []string{"/tmp/asn.mmdb", "../asn.mmdb", "set-test/../../asn.mmdb", "set-test/unrecognized.mmdb"} {
		if _, err := cachePath(dir, path); !errors.Is(err, ErrUpdate) {
			t.Fatal("cache traversal accepted", path, err)
		}
	}
}
func TestCityFileIntegration(t *testing.T) {
	path := os.Getenv("HM_TEST_CITY_MMDB")
	if path == "" {
		t.Skip("optional real city database integration needs HM_TEST_CITY_MMDB")
	}
	m := manager(t)
	db, err := openFile("city", path, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.options.CityEnabled = true
	m.dbs["city"] = db
	m.mu.Unlock()
	r := m.Lookup(netip.MustParseAddr("8.8.8.8"))
	s := m.Status()
	if s.Mode != "city" || s.City == nil || s.MappedFileBytes == 0 || r.Location == nil || r.Location.City == nil || r.Location.Region == nil || r.Location.Latitude == nil || r.Location.Longitude == nil || r.Location.Granularity != "city" {
		t.Fatal(r, s)
	}
	peak := uint64(0)
	if s.ProcessPeakRSSBytes != nil {
		peak = *s.ProcessPeakRSSBytes
	}
	t.Logf("optional city=%s; bytes=%d; country=%s; city=%s; peak RSS bytes=%d", s.City.Edition, s.City.SizeBytes, *r.Location.CountryCode, *r.Location.City, peak)
}

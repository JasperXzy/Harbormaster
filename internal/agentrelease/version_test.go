package agentrelease

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSemanticUpgradeComparison(t *testing.T) {
	for _, tc := range []struct {
		installed, available string
		want                 int
	}{
		{"v1.9.0", "v1.10.0", 1}, {"1.2.3", "v1.2.3", 0},
		{"v2.0.0", "v1.9.0", 0}, {"v1.2.3-rc.2", "v1.2.3-rc.10", 1},
		{"v1.2.3-rc.1", "v1.2.3", 1}, {"v1.2.3", "v1.2.3-rc.1", 0},
		{"v1.2.3+old", "v1.2.3+new", 0}, {"dev", "v1.2.3", -1},
		{"v1.2.3", "dev", -1}, {"", "v1.2.3", -1}, {"unknown", "v1.2.3", -1},
	} {
		got := UpdateAvailable(tc.installed, tc.available)
		if tc.want == -1 {
			if got != nil {
				t.Fatal(tc, *got)
			}
			continue
		}
		if got == nil || *got != (tc.want == 1) {
			t.Fatal(tc, got)
		}
	}
	for _, invalid := range []string{"", "v1", "1.2", "v01.2.3", "v1.2.3-01", " v1.2.3", "v1.2.3\n", "v1.2.3;echo", "v1.2.3 -X secret", "v1.2.3+" + strings.Repeat("a", 65)} {
		if v, ok := NormalizeVersion(invalid); ok {
			t.Fatal("invalid release accepted", v)
		}
	}
}

func writeVersion(t *testing.T, dir, version string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version), 0600); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, name := range Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(b), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestVersionMetadataMustBeValidAndChecksummed(t *testing.T) {
	dir := fixture(t)
	if m, err := Check(dir); err != nil || m.Version != "v1.2.0" {
		t.Fatal(m.Version, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("v1.3.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(dir); err == nil {
		t.Fatal("unverified version trusted")
	}
	for _, invalid := range []string{"", "v1", "unknown", "v1.2.0\n\n", strings.Repeat("a", 66)} {
		writeVersion(t, dir, invalid)
		if _, err := Check(dir); err == nil {
			t.Fatal("invalid checksummed version trusted", invalid)
		}
	}
	writeVersion(t, dir, "1.3.0-rc.1+build\n")
	if m, err := Check(dir); err != nil || m.Version != "v1.3.0-rc.1+build" {
		t.Fatal(m.Version, err)
	}
	if err := os.Remove(filepath.Join(dir, "VERSION")); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(dir); err == nil {
		t.Fatal("missing metadata accepted")
	}
}

func TestVersionCacheExpiresAndRechecksChangedDirectory(t *testing.T) {
	dir := fixture(t)
	var cache VersionCache
	now := time.Now()
	if got := cache.Get(dir, now); got == nil || *got != "v1.2.0" {
		t.Fatal(got)
	}
	writeVersion(t, dir, "v1.3.0\n")
	if got := cache.Get(dir, now.Add(29*time.Second)); got == nil || *got != "v1.2.0" {
		t.Fatal("cache did not hold immutable result", got)
	}
	if got := cache.Get(dir, now.Add(30*time.Second)); got == nil || *got != "v1.3.0" {
		t.Fatal("new version not picked up", got)
	}
	if err := os.WriteFile(filepath.Join(dir, Files[0]), []byte("corrupt executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(dir); err == nil {
		t.Fatal("install validation relied on cached hint")
	}
	if got := cache.Get(dir, now.Add(time.Minute)); got != nil {
		t.Fatal("invalid release still advertised", *got)
	}
	other := fixture(t)
	if got := cache.Get(other, now.Add(time.Minute)); got == nil || *got != "v1.2.0" {
		t.Fatal(got)
	}
	if got := cache.Get("", now.Add(time.Minute)); got != nil {
		t.Fatal("missing release advertised", *got)
	}
}

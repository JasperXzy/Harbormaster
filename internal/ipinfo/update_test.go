package ipinfo

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func makeOld(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, db := range m.dbs {
		db.info.Edition = "2026-10"
	}
}
func fakeDownload(t *testing.T, calls *atomic.Int32, badCountry bool) *http.Client {
	t.Helper()
	return &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "download.db-ip.com" || r.Method != "GET" || !strings.HasPrefix(r.UserAgent(), "Harbormaster-IPDB/") {
			t.Error("non-fixed update request", r.URL)
		}
		if badCountry && strings.Contains(r.URL.Path, "country") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not gzip"))}, nil
		}
		file, err := bundle.Open("data/" + filepath.Base(r.URL.Path))
		if err != nil {
			return nil, err
		}
		stat, _ := file.Stat()
		return &http.Response{StatusCode: 200, Body: file, ContentLength: stat.Size()}, nil
	})}
}
func TestMonthlyPublicationRestoreAndOfflineReads(t *testing.T) {
	m := manager(t)
	var calls atomic.Int32
	m.options.HTTP = fakeDownload(t, &calls, false)
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if err := m.Sync(context.Background(), now); err != nil || calls.Load() != 0 {
		t.Fatal("current bundle fetched needlessly", calls.Load(), err)
	}
	makeOld(m)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if r := m.Lookup(netip.MustParseAddr("8.8.8.8")); r.ASN == nil || r.ASN.Number != 15169 {
					t.Error(r)
				}
			}
		}()
	}
	if err := m.Sync(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	s := m.Status()
	if calls.Load() != 2 || s.ASN.Origin != "downloaded" || s.Country.Storage != "mmap" || s.MappedFileBytes != 18163342 || s.LastUpdateAt == nil || s.LastError != nil {
		t.Fatal(calls.Load(), s)
	}
	if err := m.Sync(context.Background(), now.Add(-31*24*time.Hour)); err != nil || calls.Load() != 2 {
		t.Fatal("clock rollback downgraded data", calls.Load(), err)
	}
	other, err := New(Options{Directory: m.options.Directory})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if other.Status().ASN.Origin != "downloaded" || other.Lookup(netip.MustParseAddr("1.1.1.1")).ASN.Number != 13335 {
		t.Fatal("published files not restored")
	}
	if _, err = os.Stat(filepath.Join(m.options.Directory, "active.json")); err != nil {
		t.Fatal(err)
	}
}
func TestFailedUpdateRetainsWholePriorSetAndRepairsBadCache(t *testing.T) {
	m := manager(t)
	makeOld(m)
	var calls atomic.Int32
	m.options.HTTP = fakeDownload(t, &calls, true)
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if err := m.Sync(context.Background(), now); err != ErrUpdate {
		t.Fatal(err)
	}
	if m.Status().ASN.Origin != "bundled" || m.Status().LastError == nil || m.Lookup(netip.MustParseAddr("1.1.1.1")).ASN.Number != 13335 {
		t.Fatal("half-published failed update")
	}
	if _, err := os.Stat(filepath.Join(m.options.Directory, "active.json")); !os.IsNotExist(err) {
		t.Fatal("failed edition was committed", err)
	}
	if err := os.WriteFile(filepath.Join(m.options.Directory, "active.json"), []byte("corrupt manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	m.options.HTTP = fakeDownload(t, &calls, false)
	if err := m.Sync(context.Background(), now); err != nil || m.Status().LastError != nil || m.Status().ASN.Origin != "downloaded" {
		t.Fatal("bad cache could not recover", err)
	}
}
func TestUpdateLockAndCancelledAutoUpdate(t *testing.T) {
	m := manager(t)
	unlock, err := lockCache(m.options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if release, err := lockCache(m.options.Directory); err != errBusy {
		if release != nil {
			release()
		}
		t.Fatal("two cache publishers", err)
	}
	makeOld(m)
	var calls atomic.Int32
	m.options.HTTP = fakeDownload(t, &calls, false)
	if err = m.Sync(context.Background(), time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)); err != nil || calls.Load() != 0 {
		t.Fatal("ignored another updater lock", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.options.AutoUpdate = true
	m.Run(ctx)
	if calls.Load() != 0 {
		t.Fatal("cancelled worker downloaded")
	}
}
func TestLiveMonthlyDownload(t *testing.T) {
	if os.Getenv("HM_TEST_IPDB_LIVE") != "1" {
		t.Skip("explicit opt-in for public dataset download integration")
	}
	m := manager(t)
	makeOld(m)
	if err := m.Sync(context.Background(), time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	s := m.Status()
	if s.ASN.Origin != "downloaded" || s.Country.Origin != "downloaded" {
		t.Fatal(s)
	}
	t.Logf("downloaded ASN=%s country=%s bytes=%d", s.ASN.Edition, s.Country.Edition, s.MappedFileBytes)
}

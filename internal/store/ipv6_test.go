package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func ipv6ScanFixture(now time.Time, success bool, addresses ...string) model.ScanResult {
	if addresses == nil {
		addresses = []string{}
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	view := map[string]any{"addresses": addresses, "status": "ok", "source": "ecs_network_interfaces", "observed_at": stamp, "checked_at": stamp, "last_error": nil}
	if !success {
		view["status"], view["source"], view["observed_at"], view["last_error"] = "unknown", nil, nil, model.Error("permission_denied")
	}
	return model.ScanResult{Snapshots: []model.Snapshot{{ID: "ecs", Kind: "ecs", Region: "cn-shanghai", Payload: map[string]any{"id": "ecs", "cloud_instance_id": "i-test", "name": "test", "status": "running", "ipv6": view}}}, CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}}
}

func startIPv6Scan(t *testing.T, s *Store, now time.Time) (model.Job, model.Account) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.EnqueueJob(ctx, "refresh", "a", []string{"a"}, now); err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.BeginAccountScan(ctx, "a", j.Owner, now)
	if err != nil {
		t.Fatal(err)
	}
	return j, a
}

func TestIPv6PreservesLastSuccessAndOnlyClearsAfterConfirmation(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	start := time.Now().UTC()
	for i, step := range []struct {
		success bool
		ips     []string
		status  string
		seen    int
	}{
		{false, nil, "unknown", -1},
		{true, []string{"2001:db8::1", "2001:db8::2"}, "ok", 1},
		{false, []string{"2001:db8::1", "2001:db8::2"}, "stale", 1},
		{false, []string{"2001:db8::1", "2001:db8::2"}, "stale", 1},
		{true, []string{"2001:db8::3"}, "ok", 4},
		{true, nil, "ok", 5},
		{false, nil, "stale", 5},
	} {
		now := start.Add(time.Duration(i) * time.Minute)
		j, a := startIPv6Scan(t, s, now)
		result := ipv6ScanFixture(now, step.success)
		if step.success && step.ips != nil {
			result = ipv6ScanFixture(now, true, step.ips...)
		}
		if err := s.SaveScan(ctx, j, a, result, now); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishJob(ctx, j, nil, "", now); err != nil {
			t.Fatal(err)
		}
		// Closing/reopening ensures retention is stored, not an in-memory cache.
		if i == 1 {
			var path string
			if err := s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			s, err = Open(ctx, filepath.Dir(path), false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
		}
		items, err := s.Snapshots(ctx, []string{"ecs"}, "a")
		if err != nil || len(items) != 1 {
			t.Fatal(items, err)
		}
		view := items[0]["ipv6"].(map[string]any)
		want := []any{}
		for _, ip := range step.ips {
			want = append(want, ip)
		}
		var observed any
		if step.seen >= 0 {
			observed = start.Add(time.Duration(step.seen) * time.Minute).Format(time.RFC3339Nano)
		}
		if view["status"] != step.status || view["observed_at"] != observed || view["checked_at"] != now.Format(time.RFC3339Nano) || !reflect.DeepEqual(view["addresses"], want) {
			t.Fatal("wrong retained result", i, view)
		}
		if (view["last_error"] == nil) != step.success {
			t.Fatal("attempt error not preserved", view)
		}
	}
}

func TestIPv6RejectedScanCannotReplaceStoredAddresses(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	j, a := startIPv6Scan(t, s, now)
	if err := s.SaveScan(ctx, j, a, ipv6ScanFixture(now, true, "2001:db8::1"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishJob(ctx, j, nil, "", now); err != nil {
		t.Fatal(err)
	}
	j, a = startIPv6Scan(t, s, now.Add(time.Minute))
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(ctx, j, a, ipv6ScanFixture(now, true), now.Add(time.Minute)); !errors.Is(err, ErrAccountChanged) {
		t.Fatal("stale worker published empty IPv6", err)
	}
	items, err := s.Snapshots(ctx, []string{"ecs"}, "a")
	if err != nil || len(items) != 1 || items[0]["ipv6"].(map[string]any)["addresses"].([]any)[0] != "2001:db8::1" {
		t.Fatal("stored addresses changed", items, err)
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func eventSnapshot(status string, ipv4, ipv6 []string) model.Snapshot {
	public := []map[string]any{}
	for _, ip := range ipv4 {
		public = append(public, map[string]any{"address": ip, "type": "eip"})
	}
	if ipv6 == nil {
		ipv6 = []string{}
	}
	return model.Snapshot{ID: "ecs", Kind: "ecs", Region: "cn-shanghai", IPv4Complete: true, Payload: map[string]any{"id": "ecs", "cloud_instance_id": "i-test", "name": "test", "status": status, "public_ips": public, "ipv6": map[string]any{"status": "ok", "addresses": ipv6, "observed_at": "2026-11-12T00:00:00Z"}}}
}
func saveEventScan(t *testing.T, s *Store, result model.ScanResult, now time.Time) {
	t.Helper()
	j, a := startIPv6Scan(t, s, now)
	if err := s.SaveScan(context.Background(), j, a, result, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishJob(context.Background(), j, nil, "", now); err != nil {
		t.Fatal(err)
	}
}
func instanceEvents(t *testing.T, s *Store) []model.InstanceEvent {
	t.Helper()
	events, err := s.InstanceEvents(context.Background(), "a", "ecs", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestInstanceEventLifecycleAndUnknownSourceRecovery(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	if _, err := s.DB.Exec("DELETE FROM resource_snapshot"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	scan := func(snapshot model.Snapshot) {
		t.Helper()
		now = now.Add(time.Minute)
		saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{snapshot}, CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}}, now)
	}
	scan(eventSnapshot("running", []string{"192.0.2.2", "192.0.2.1"}, []string{"2001:db8::1"}))
	history := instanceEvents(t, s)
	if len(history) != 1 || history[0].Kind != "discovered" || history[0].Before != nil || history[0].After["present"] != true {
		t.Fatal(history)
	}
	// Address ordering, duplication and IPv6 text formatting are not changes.
	scan(eventSnapshot("running", []string{"192.0.2.1", "192.0.2.2", "192.0.2.1"}, []string{"2001:0db8:0:0::1"}))
	unknown := eventSnapshot("unknown", []string{"192.0.2.99"}, nil)
	unknown.IPv4Complete = false
	unknown.Payload["ipv6"].(map[string]any)["status"] = "stale"
	scan(unknown)
	if len(instanceEvents(t, s)) != 1 {
		t.Fatal("unknown data overwrote comparison baseline")
	}
	var filename string
	if err := s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&filename); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(context.Background(), filepath.Dir(filename), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scan(eventSnapshot("stopped", []string{"192.0.2.3"}, nil))
	history = instanceEvents(t, s)
	if len(history) != 4 || history[0].Kind != "ipv6_changed" || history[1].Kind != "public_ipv4_changed" || history[2].Kind != "status_changed" {
		t.Fatal(history)
	}
	if !reflect.DeepEqual(history[0].After["addresses"], []any{}) || !reflect.DeepEqual(history[1].Before["addresses"], []any{"192.0.2.1", "192.0.2.2"}) || history[2].Before["status"] != "running" {
		t.Fatal("lost last known data across restart", history)
	}
	scan(eventSnapshot("stopped", []string{"192.0.2.3"}, nil))
	saveEventScan(t, s, model.ScanResult{Failure: "permission_denied", CompleteScopes: map[string]bool{"ecs:cn-shanghai": false}}, now)
	if len(instanceEvents(t, s)) != 4 {
		t.Fatal("failed scan recorded disappearance")
	}
	for range 2 {
		saveEventScan(t, s, model.ScanResult{CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}}, now)
	}
	history = instanceEvents(t, s)
	if len(history) != 5 || history[0].Kind != "disappeared" || history[0].After["present"] != false {
		t.Fatal(history)
	}
	scan(eventSnapshot("running", []string{"192.0.2.4"}, []string{"2001:db8::4"}))
	history = instanceEvents(t, s)
	if len(history) != 6 || history[0].Kind != "reappeared" {
		t.Fatal("reappearance fabricated intermediate field changes", history)
	}
	if _, err = s.UpdateAccount(context.Background(), "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(instanceEvents(t, s)) != 6 {
		t.Fatal("account disable erased or created history")
	}
}

func TestInstanceEventsLegacyBaselineAndScopeIsolation(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	first := eventSnapshot("running", nil, nil)
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{first}}, now)
	if len(instanceEvents(t, s)) != 0 {
		t.Fatal("feature rollout fabricated a discovery")
	}
	// A different region or product completing cannot hide this ECS instance.
	saveEventScan(t, s, model.ScanResult{CompleteScopes: map[string]bool{"ecs:cn-hongkong": true, "swas:cn-shanghai": true}}, now)
	if len(instanceEvents(t, s)) != 0 {
		t.Fatal("different scope caused disappearance")
	}
	first.Payload["status"] = "stopped"
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{first}}, now)
	if events := instanceEvents(t, s); len(events) != 1 || events[0].Kind != "status_changed" {
		t.Fatal(events)
	}
	for _, filters := range [][2]string{{"another-account", ""}, {"a", "other-instance"}} {
		events, err := s.InstanceEvents(context.Background(), filters[0], filters[1], 0, 20)
		if err != nil || len(events) != 0 {
			t.Fatal("history crossed filter", events, err)
		}
	}
}

func TestInstanceEventsAtomicityAndAccountFence(t *testing.T) {
	for _, mode := range []string{"event_insert_failure", "snapshot_insert_failure", "account_changed"} {
		t.Run(mode, func(t *testing.T) {
			s := agentStore(t)
			setAlertAccount(t, s, "ok", 0)
			ctx := context.Background()
			now := time.Now().UTC()
			saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", nil, nil)}}, now)
			j, a := startIPv6Scan(t, s, now)
			switch mode {
			case "event_insert_failure":
				_, err := s.DB.Exec("CREATE TRIGGER fail_event BEFORE INSERT ON instance_event BEGIN SELECT RAISE(ABORT,'test event failure'); END")
				if err != nil {
					t.Fatal(err)
				}
			case "snapshot_insert_failure":
				_, err := s.DB.Exec("CREATE TRIGGER fail_event BEFORE UPDATE ON resource_snapshot BEGIN SELECT RAISE(ABORT,'test snapshot failure'); END")
				if err != nil {
					t.Fatal(err)
				}
			case "account_changed":
				if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			result := model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("stopped", nil, nil)}}
			err := s.SaveScan(ctx, j, a, result, now)
			if err == nil || (mode == "account_changed" && !errors.Is(err, ErrAccountChanged)) {
				t.Fatal("invalid scan committed", err)
			}
			var raw string
			if err := s.DB.QueryRow("SELECT value_json FROM instance_event_state WHERE instance_id='ecs'").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var baseline instanceEventValues
			if json.Unmarshal([]byte(raw), &baseline) != nil || baseline.Status != "running" || len(instanceEvents(t, s)) != 0 {
				t.Fatal("event and baseline were not rolled back together", raw)
			}
			items, readErr := s.Snapshots(ctx, []string{"ecs"}, "a")
			if readErr != nil || len(items) != 1 || items[0]["status"] != "running" {
				t.Fatal("resource snapshot escaped rollback", items, readErr)
			}
			if mode != "account_changed" {
				if _, err := s.DB.Exec("DROP TRIGGER fail_event"); err != nil {
					t.Fatal(err)
				}
				if err := s.SaveScan(ctx, j, a, result, now); err != nil {
					t.Fatal(err)
				}
				if len(instanceEvents(t, s)) != 1 {
					t.Fatal("retry did not record exactly one change")
				}
			}
		})
	}
}

func TestSWASInstanceEvents(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	snapshot := eventSnapshot("running", []string{"192.0.2.1"}, nil)
	snapshot.ID, snapshot.Kind = "swas", "swas"
	snapshot.Payload["id"], snapshot.Payload["cloud_instance_id"], snapshot.Payload["ipv6"] = "swas", "swas-uuid", nil
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{snapshot}}, now)
	snapshot.Payload["status"] = "stopped"
	snapshot.Payload["public_ips"] = []map[string]any{{"address": "192.0.2.2", "type": "instance_public_ip"}}
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{snapshot}}, now)
	events, err := s.InstanceEvents(context.Background(), "a", "swas", 0, 20)
	if err != nil || len(events) != 2 || events[0].Kind != "public_ipv4_changed" || events[1].Kind != "status_changed" || events[0].InstanceKind != "swas" {
		t.Fatal(events, err)
	}
}

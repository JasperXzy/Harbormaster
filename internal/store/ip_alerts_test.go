package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestPublicIPChangeRecordsEachEventWithoutRecovery(t *testing.T) {
	for _, kind := range []string{"ecs", "swas"} {
		t.Run(kind, func(t *testing.T) {
			s := agentStore(t)
			setAlertAccount(t, s, "ok", 0)
			now := time.Now().UTC()
			if _, err := s.DB.Exec("DELETE FROM resource_snapshot"); err != nil {
				t.Fatal(err)
			}
			makeSnapshot := func(addresses ...string) model.Snapshot {
				snapshot := eventSnapshot("running", addresses, nil)
				snapshot.ID, snapshot.Kind = kind, kind
				snapshot.Payload["id"] = kind
				if kind == "swas" {
					snapshot.Payload["cloud_instance_id"] = "swas-uuid"
				}
				return snapshot
			}
			scan := func(snapshot model.Snapshot) {
				t.Helper()
				now = now.Add(time.Second)
				saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{snapshot}}, now)
			}
			scan(makeSnapshot("192.0.2.1", "192.0.2.2"))
			scan(makeSnapshot("192.0.2.2", "192.0.2.1", "192.0.2.1"))
			partial := makeSnapshot()
			partial.IPv4Complete = false
			scan(partial)
			ipv6 := makeSnapshot("192.0.2.1", "192.0.2.2")
			ipv6.Payload["ipv6"] = map[string]any{"status": "ok", "addresses": []string{"2001:db8::1"}}
			scan(ipv6)
			if len(alertHistory(t, s)) != 0 {
				t.Fatal("baseline, reorder, partial or allocated IPv6 caused public IP alert")
			}
			scan(makeSnapshot("192.0.2.3"))
			scan(makeSnapshot("192.0.2.3"))
			evaluate(t, s, now)
			history := alertHistory(t, s)
			if len(history) != 1 || history[0].RuleKey == nil || *history[0].RuleKey != "instance_public_ip_changed" || history[0].Kind != "alert" || history[0].DeliveryStatus != "not_sent" || history[0].AccountID == nil || *history[0].AccountID != "a" || history[0].InstanceID == nil || *history[0].InstanceID != kind {
				t.Fatal(history)
			}
			var eventID string
			if err := s.DB.QueryRow("SELECT id FROM instance_event WHERE json_extract(payload_json,'$.kind')='public_ipv4_changed' ORDER BY sequence DESC LIMIT 1").Scan(&eventID); err != nil {
				t.Fatal(err)
			}
			if history[0].EventKey != "instance-event/"+eventID || !strings.Contains(history[0].Body, "192.0.2.3") {
				t.Fatal("notice not tied to durable event", history[0])
			}
			// Returning to an old IP set and removing all IPs are separate changes.
			scan(makeSnapshot("192.0.2.1", "192.0.2.2"))
			scan(makeSnapshot())
			evaluate(t, s, now)
			history = alertHistory(t, s)
			if len(history) != 3 {
				t.Fatal("distinct changes deduplicated incorrectly", history)
			}
			for _, notification := range history {
				if notification.Kind != "alert" {
					t.Fatal("change misreported as recovery", history)
				}
			}
		})
	}
}

func TestPublicIPRuleDisableCancelsAndDoesNotReplay(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	channelFixture(t, s, 0, now)
	scan := func(address string) {
		t.Helper()
		saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{address}, nil)}}, now)
	}
	scan("192.0.2.1")
	scan("192.0.2.2")
	if history := alertHistory(t, s); len(history) != 1 || history[0].DeliveryStatus != "pending" {
		t.Fatal(history)
	}
	off, on := false, true
	if _, err := s.UpdateAlertRule(ctx, "instance_public_ip_changed", model.AlertRuleUpdate{Enabled: &off}, now); err != nil {
		t.Fatal(err)
	}
	scan("192.0.2.3")
	if history := alertHistory(t, s); len(history) != 1 || history[0].DeliveryStatus == "pending" {
		t.Fatal("disabled rule left pending notice or recorded another", history)
	}
	if events := instanceEvents(t, s); len(events) != 2 {
		t.Fatal("disabled alert stopped resource history", events)
	}
	if _, err := s.UpdateAlertRule(ctx, "instance_public_ip_changed", model.AlertRuleUpdate{Enabled: &on}, now); err != nil {
		t.Fatal(err)
	}
	scan("192.0.2.3")
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("reenabling replayed an old change")
	}
	scan("192.0.2.4")
	if history := alertHistory(t, s); len(history) != 2 || history[0].DeliveryStatus != "pending" {
		t.Fatal(history)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, notification := range alertHistory(t, s) {
		if notification.DeliveryStatus == "pending" {
			t.Fatal("disabled account retained pending IP change notice")
		}
	}
}

func TestPublicIPNotificationFailureRollsBackEventAndSnapshot(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"192.0.2.1"}, nil)}}, now)
	j, a := startIPv6Scan(t, s, now)
	if _, err := s.DB.Exec("CREATE TRIGGER reject_ip_notice BEFORE INSERT ON notification BEGIN SELECT RAISE(ABORT,'test notification failure'); END"); err != nil {
		t.Fatal(err)
	}
	result := model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("running", []string{"192.0.2.2"}, nil)}}
	if err := s.SaveScan(ctx, j, a, result, now); err == nil {
		t.Fatal("failed notification did not roll back")
	}
	if len(instanceEvents(t, s)) != 0 || len(alertHistory(t, s)) != 0 {
		t.Fatal("event or notice survived failed transaction")
	}
	items, err := s.Snapshots(ctx, []string{"ecs"}, "a")
	if err != nil || len(items) != 1 || items[0]["public_ips"].([]any)[0].(map[string]any)["address"] != "192.0.2.1" {
		t.Fatal("snapshot escaped failed transaction", items, err)
	}
	if _, err := s.DB.Exec("DROP TRIGGER reject_ip_notice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(ctx, j, a, result, now); err != nil {
		t.Fatal(err)
	}
	if len(instanceEvents(t, s)) != 1 || len(alertHistory(t, s)) != 1 {
		t.Fatal("retry did not atomically record one event and one notice")
	}
}

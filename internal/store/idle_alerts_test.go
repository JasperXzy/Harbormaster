package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func idleHistory(t *testing.T, s *Store) []model.Notification {
	t.Helper()
	items := []model.Notification{}
	for _, n := range alertHistory(t, s) {
		if n.RuleKey != nil && *n.RuleKey == "idle_resource_detected" {
			items = append(items, n)
		}
	}
	return items
}

func idleState(t *testing.T, s *Store, id string, wantActive bool, wantEpisode int) {
	t.Helper()
	var active bool
	var episode int
	if err := s.DB.QueryRow("SELECT active,episode FROM alert_state WHERE rule_key='idle_resource_detected' AND target_id=?", id).Scan(&active, &episode); err != nil || active != wantActive || episode != wantEpisode {
		t.Fatal(id, active, episode, err)
	}
}

func healthyIdleScan(kind, id string, at time.Time) model.ScanResult {
	result := idleScan(kind, at, 1, nil)
	result.Idle[0].Data.NonCandidateIDs = []string{id}
	return result
}

func TestIdleAlertRequiresFreshKnownEvidenceAndDeduplicatesEpisodes(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	c := idleFixture("eip", "eip-one", now)
	saveEventScan(t, s, idleScan("eip", now, 1, nil, c), now)
	first := idleHistory(t, s)
	if len(first) != 1 || first[0].ResourceID == nil || *first[0].ResourceID != c.ID || first[0].InstanceID != nil || first[0].AccountID == nil || *first[0].AccountID != "a" || first[0].Kind != "alert" || first[0].Urgent || first[0].Severity != "warning" || first[0].DeliveryStatus != "not_sent" || !strings.Contains(first[0].Body, "monthly_cost=unestimated") || strings.Contains(first[0].Body, c.Name) {
		t.Fatal(first)
	}
	for minute := 1; minute <= 2; minute++ {
		at := now.Add(time.Duration(minute) * time.Minute)
		saveEventScan(t, s, idleScan("eip", at, 1, nil, idleFixture("eip", c.CloudID, at)), at)
		evaluate(t, s, at)
	}
	unknown := now.Add(3 * time.Minute)
	saveEventScan(t, s, idleScan("eip", unknown, 1, []string{c.CloudID}), unknown)
	failed := now.Add(4 * time.Minute)
	saveEventScan(t, s, model.ScanResult{Idle: []model.IdleObservation{{Region: "cn-shanghai", Kind: "eip", QueriedAt: failed, Error: model.Error("permission_denied")}}}, failed)
	evaluate(t, s, now.Add(time.Hour))
	if len(idleHistory(t, s)) != 1 {
		t.Fatal("repeat, unknown, failure or evaluation fabricated an incident transition")
	}
	idleState(t, s, c.ID, true, 1)
	healthy := now.Add(2 * time.Hour)
	saveEventScan(t, s, healthyIdleScan("eip", c.CloudID, healthy), healthy)
	saveEventScan(t, s, healthyIdleScan("eip", c.CloudID, healthy.Add(time.Minute)), healthy.Add(time.Minute))
	history := idleHistory(t, s)
	if len(history) != 2 || history[0].Kind != "recovery" || history[0].ResourceID == nil || *history[0].ResourceID != c.ID {
		t.Fatal(history)
	}
	idleState(t, s, c.ID, false, 1)
	again := healthy.Add(2 * time.Minute)
	saveEventScan(t, s, idleScan("eip", again, 1, nil, idleFixture("eip", c.CloudID, again)), again)
	if len(idleHistory(t, s)) != 3 {
		t.Fatal("fresh recurrence did not open a new episode")
	}
	idleState(t, s, c.ID, true, 2)
}

func TestIdleAlertResourcesAreIndependentAndDisappearanceIsSilent(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	channelFixture(t, s, 0, now)
	a, b := idleFixture("eip", "one", now), idleFixture("eip", "two", now)
	saveEventScan(t, s, idleScan("eip", now, 2, nil, a, b), now)
	at := now.Add(time.Minute)
	saveEventScan(t, s, idleScan("eip", at, 1, nil, idleFixture("eip", b.CloudID, at)), at)
	evaluate(t, s, at)
	history := idleHistory(t, s)
	if len(history) != 2 {
		t.Fatal("disappearance created a recovery", history)
	}
	for _, n := range history {
		if n.ResourceID == nil || n.Kind != "alert" {
			t.Fatal(n)
		}
		if *n.ResourceID == a.ID && (n.DeliveryStatus != "not_sent" || n.LastError == nil || n.LastError.Code != "idle_resource_no_longer_observed") {
			t.Fatal("disappeared resource retained pending delivery", n)
		}
		if *n.ResourceID == b.ID && n.DeliveryStatus != "pending" {
			t.Fatal("unrelated incident was cancelled", n)
		}
	}
	idleState(t, s, a.ID, false, 1)
	idleState(t, s, b.ID, true, 1)
	again := at.Add(time.Minute)
	saveEventScan(t, s, idleScan("eip", again, 2, nil, idleFixture("eip", a.CloudID, again), idleFixture("eip", b.CloudID, again)), again)
	if len(idleHistory(t, s)) != 3 {
		t.Fatal("reappearance did not reopen only its own incident")
	}
	idleState(t, s, a.ID, true, 2)
	idleState(t, s, b.ID, true, 1)
}

func TestIdleAlertCredentialRotationPreservesLogicalIncident(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	c := idleFixture("eip", "one", now)
	saveEventScan(t, s, idleScan("eip", now, 1, nil, c), now)
	if _, err := s.UpdateAccount(ctx, "a", func(*model.Account) ([]byte, error) { return make([]byte, 32), nil }, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(time.Minute))
	idleState(t, s, c.ID, true, 1)
	if items, _ := readIdle(t, s, "eip", now.Add(time.Minute)); len(items) != 0 {
		t.Fatal("predecessor credential evidence was exposed", items)
	}
	at := now.Add(2 * time.Minute)
	saveEventScan(t, s, idleScan("eip", at, 1, nil, idleFixture("eip", c.CloudID, at)), at)
	if len(idleHistory(t, s)) != 1 {
		t.Fatal("changing credentials duplicated the same resource incident")
	}
}

func TestIdleAlertDisabledMonitoringClosesWithoutRecoveryOrCacheReplay(t *testing.T) {
	for _, scope := range []string{"account", "region", "rule"} {
		t.Run(scope, func(t *testing.T) {
			s := powerStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			channelFixture(t, s, 0, now)
			c := idleFixture("eip", "one", now)
			saveEventScan(t, s, idleScan("eip", now, 1, nil, c), now)
			toggle := func(enabled bool, at time.Time) {
				t.Helper()
				if scope == "rule" {
					if _, err := s.UpdateAlertRule(ctx, "idle_resource_detected", model.AlertRuleUpdate{Enabled: &enabled}, at); err != nil {
						t.Fatal(err)
					}
					return
				}
				if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) {
					if scope == "account" {
						a.Enabled = enabled
					} else if enabled {
						a.RegionIDs = []string{"cn-shanghai"}
					} else {
						a.RegionIDs = []string{"cn-hongkong"}
					}
					return nil, nil
				}, at, time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			toggle(false, now.Add(time.Minute))
			evaluate(t, s, now.Add(time.Minute))
			idleState(t, s, c.ID, false, 1)
			history := idleHistory(t, s)
			if len(history) != 1 || history[0].DeliveryStatus != "not_sent" {
				t.Fatal("disable invented recovery or left delivery queued", history)
			}
			toggle(true, now.Add(2*time.Minute))
			evaluate(t, s, now.Add(2*time.Minute))
			if len(idleHistory(t, s)) != 1 {
				t.Fatal("re-enable replayed cached evidence")
			}
			at := now.Add(3 * time.Minute)
			saveEventScan(t, s, idleScan("eip", at, 1, nil, idleFixture("eip", c.CloudID, at)), at)
			if len(idleHistory(t, s)) != 2 {
				t.Fatal("new enabled observation failed to open incident")
			}
			idleState(t, s, c.ID, true, 2)
		})
	}
}

func TestIdleAlertQuietAndMaintenanceRecordWithoutCatchUp(t *testing.T) {
	for _, mode := range []string{"daily", "maintenance"} {
		t.Run(mode, func(t *testing.T) {
			s := powerStore(t)
			now := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)
			if _, err := s.DB.Exec("INSERT INTO app_meta(key,value) VALUES('business_timezone','UTC')"); err != nil {
				t.Fatal(err)
			}
			channelFixture(t, s, 0, now)
			q := model.QuietHours{Enabled: mode == "daily", Start: "23:00", End: "08:00"}
			if mode == "maintenance" {
				q.Maintenance = &model.MaintenanceWindow{StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour)}
			}
			q, err := s.SaveQuietHours(context.Background(), q, now)
			if err != nil {
				t.Fatal(err)
			}
			saveEventScan(t, s, idleScan("eip", now, 1, nil, idleFixture("eip", "one", now)), now)
			history := idleHistory(t, s)
			if len(history) != 1 || history[0].Urgent || history[0].DeliveryStatus != "not_sent" || history[0].SuppressionReason == nil || *history[0].SuppressionReason != mode {
				t.Fatal(history)
			}
			q.Enabled, q.Maintenance = false, nil
			if _, err := s.SaveQuietHours(context.Background(), q, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			at := now.Add(2 * time.Minute)
			saveEventScan(t, s, idleScan("eip", at, 1, nil, idleFixture("eip", "one", at)), at)
			evaluate(t, s, at)
			if history = idleHistory(t, s); len(history) != 1 || history[0].DeliveryStatus != "not_sent" {
				t.Fatal("suppressed incident was backfilled", history)
			}
		})
	}
}

func TestIdleAlertStaleObservationsCannotFireOrRecover(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	c := idleFixture("eip", "one", now)
	saveEventScan(t, s, idleScan("eip", now, 1, nil, c), now.Add(11*time.Minute))
	if len(idleHistory(t, s)) != 0 {
		t.Fatal("stale positive opened incident")
	}
	fresh := now.Add(12 * time.Minute)
	saveEventScan(t, s, idleScan("eip", fresh, 1, nil, idleFixture("eip", c.CloudID, fresh)), fresh)
	healthy := fresh.Add(time.Minute)
	saveEventScan(t, s, healthyIdleScan("eip", c.CloudID, healthy), healthy.Add(11*time.Minute))
	evaluate(t, s, healthy.Add(11*time.Minute))
	if len(idleHistory(t, s)) != 1 {
		t.Fatal("stale negative recovered incident")
	}
	idleState(t, s, c.ID, true, 1)
	again := healthy.Add(12 * time.Minute)
	saveEventScan(t, s, healthyIdleScan("eip", c.CloudID, again), again)
	if history := idleHistory(t, s); len(history) != 2 || history[0].Kind != "recovery" {
		t.Fatal(history)
	}
}

func TestIdleAlertLinksNewInstanceFromSameDiscoveryTransaction(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	c := idleFixture("ecs", "new-instance", now)
	c.InstanceIDs = []string{c.ID}
	result := idleScan("ecs", now, 1, nil, c)
	snapshot := eventSnapshot("stopped", nil, nil)
	snapshot.ID = c.ID
	snapshot.Payload["id"], snapshot.Payload["cloud_instance_id"] = c.ID, c.CloudID
	result.Snapshots = []model.Snapshot{snapshot}
	saveEventScan(t, s, result, now)
	history := idleHistory(t, s)
	if len(history) != 1 || history[0].InstanceID == nil || *history[0].InstanceID != c.ID || history[0].ResourceID == nil || *history[0].ResourceID != c.ID {
		t.Fatal("first discovery failed to link the new instance", history)
	}
	var broken int
	if err := s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil || broken != 0 {
		t.Fatal(broken, err)
	}
}

func TestIdleAlertFailureRollsBackCandidateStateAndNotificationTogether(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	ctx := context.Background()
	if _, err := s.DB.Exec("CREATE TRIGGER reject_idle_notice BEFORE INSERT ON notification WHEN NEW.rule_key='idle_resource_detected' BEGIN SELECT RAISE(ABORT,'test notification failure'); END"); err != nil {
		t.Fatal(err)
	}
	j, a := startIPv6Scan(t, s, now)
	result := idleScan("eip", now, 1, nil, idleFixture("eip", "one", now))
	if err := s.SaveScan(ctx, j, a, result, now); err == nil {
		t.Fatal("notification failure did not abort collection")
	}
	if items, source := readIdle(t, s, "eip", now); len(items) != 0 || source.Status != "never_fetched" || len(idleHistory(t, s)) != 0 {
		t.Fatal("candidate or source escaped rollback", items, source)
	}
	var targets, states int
	if err := s.DB.QueryRow("SELECT (SELECT count(*) FROM idle_alert_target),(SELECT count(*) FROM alert_state WHERE rule_key='idle_resource_detected')").Scan(&targets, &states); err != nil || targets != 0 || states != 0 {
		t.Fatal(targets, states, err)
	}
	if _, err := s.DB.Exec("DROP TRIGGER reject_idle_notice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(ctx, j, a, result, now); err != nil || len(idleHistory(t, s)) != 1 {
		t.Fatal("same collection could not commit after rollback", err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func bandwidthScan(t *testing.T, s *Store, now time.Time, kind string, capacity any) error {
	t.Helper()
	ctx := context.Background()
	if _, err := s.EnqueueJob(ctx, "refresh", "a", []string{"a"}, now); err != nil {
		return err
	}
	j, err := s.ClaimJob(ctx, now)
	if err != nil {
		return err
	}
	a, err := s.BeginAccountScan(ctx, "a", j.Owner, now)
	if err != nil {
		return err
	}
	cloudID := "i-test"
	if kind == "swas" {
		cloudID = "swas-uuid"
	}
	r := model.ScanResult{Snapshots: []model.Snapshot{{Kind: kind, ID: kind, Region: "cn-shanghai", Payload: map[string]any{"id": kind, "cloud_instance_id": cloudID, "name": "test", "status": "stopped", "charge_type": "pay_as_you_go", "network_capacity": capacity}}}}
	if err := s.SaveScan(ctx, j, a, r, now); err != nil {
		return err
	}
	return s.FinishJob(ctx, j, nil, "", now)
}
func capacityFixture(limited any) map[string]any {
	public := int64(2000)
	if limited == false {
		public = 100
	}
	return map[string]any{"public_outbound_exceeds_spec": limited, "public_outbound_mbps": public, "tx_kbps": int64(204800), "last_error": nil}
}

func TestBandwidthIncidentDeduplicationUnknownAndRecovery(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	if err := bandwidthScan(t, s, now, "ecs", capacityFixture(true)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := bandwidthScan(t, s, now, "ecs", capacityFixture(true)); err != nil {
			t.Fatal(err)
		}
		evaluate(t, s, now)
	}
	history := alertHistory(t, s)
	if len(history) != 1 || history[0].RuleKey == nil || *history[0].RuleKey != "spec_bandwidth_insufficient" || history[0].Kind != "alert" {
		t.Fatal(history)
	}
	failed := capacityFixture(false)
	failed["last_error"] = model.Error("permission_denied")
	missing := capacityFixture(false)
	missing["tx_kbps"] = nil
	for _, unknown := range []any{nil, capacityFixture(nil), failed, missing} {
		if err := bandwidthScan(t, s, now, "ecs", unknown); err != nil {
			t.Fatal(err)
		}
		evaluate(t, s, now)
	}
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("unknown comparison recovered")
	}
	if err := bandwidthScan(t, s, now, "ecs", capacityFixture(false)); err != nil {
		t.Fatal(err)
	}
	history = alertHistory(t, s)
	if len(history) != 2 || history[0].Kind != "recovery" {
		t.Fatal(history)
	}
	if err := bandwidthScan(t, s, now, "ecs", capacityFixture(true)); err != nil {
		t.Fatal(err)
	}
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("recurring mismatch did not alert")
	}
	if err := bandwidthScan(t, s, now, "swas", capacityFixture(true)); err != nil {
		t.Fatal(err)
	}
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("unsupported SWAS specification comparison alerted")
	}
}

func TestBandwidthRuleCancellationAndTransaction(t *testing.T) {
	for _, mode := range []string{"rule_disabled", "account_disabled", "resource_absent", "notification_failure"} {
		t.Run(mode, func(t *testing.T) {
			s := agentStore(t)
			setAlertAccount(t, s, "ok", 0)
			ctx := context.Background()
			now := time.Now().UTC()
			channelFixture(t, s, 0, now)
			if mode == "notification_failure" {
				if _, err := s.DB.Exec("CREATE TRIGGER reject_bandwidth_notice BEFORE INSERT ON notification BEGIN SELECT RAISE(ABORT,'test notification failure'); END"); err != nil {
					t.Fatal(err)
				}
				if err := bandwidthScan(t, s, now, "ecs", capacityFixture(true)); err == nil {
					t.Fatal("notification failure ignored")
				}
				var n int
				if err := s.DB.QueryRow("SELECT count(*) FROM alert_state WHERE rule_key='spec_bandwidth_insufficient'").Scan(&n); err != nil || n != 0 {
					t.Fatal("incident committed alone", n, err)
				}
				var raw string
				if err := s.DB.QueryRow("SELECT payload_json FROM resource_snapshot WHERE id='ecs'").Scan(&raw); err != nil || raw != "{}" {
					t.Fatal("snapshot committed alone", err)
				}
				return
			}
			if err := bandwidthScan(t, s, now, "ecs", capacityFixture(true)); err != nil {
				t.Fatal(err)
			}
			off := false
			switch mode {
			case "rule_disabled":
				if _, err := s.UpdateAlertRule(ctx, "spec_bandwidth_insufficient", model.AlertRuleUpdate{Enabled: &off}, now); err != nil {
					t.Fatal(err)
				}
				if err := bandwidthScan(t, s, now, "ecs", capacityFixture(true)); err != nil {
					t.Fatal(err)
				}
			case "account_disabled":
				if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
					t.Fatal(err)
				}
			case "resource_absent":
				if _, err := s.DB.Exec("UPDATE resource_snapshot SET present=0 WHERE id='ecs'"); err != nil {
					t.Fatal(err)
				}
			}
			evaluate(t, s, now)
			history := alertHistory(t, s)
			if len(history) != 1 || history[0].DeliveryStatus == "pending" {
				t.Fatal("inapplicable reminder not cancelled", history)
			}
		})
	}
}

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func expiryScan(t *testing.T, s *Store, now time.Time, charge string, expiry any) error {
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
	r := model.ScanResult{Snapshots: []model.Snapshot{{Kind: "ecs", ID: "ecs", Region: "cn-shanghai", Payload: map[string]any{"id": "ecs", "cloud_instance_id": "i-test", "name": "test", "status": "stopped", "charge_type": charge, "expires_at": expiry, "renewal": map[string]any{"status": "auto_renewal"}}}}}
	if err := s.SaveScan(ctx, j, a, r, now); err != nil {
		return err
	}
	return s.FinishJob(ctx, j, nil, "", now)
}

func TestExpirationRequiresFreshSnapshotAndRecoversAfterRenewal(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	channelFixture(t, s, 0, now)
	expiry := now.Add(7*24*time.Hour + time.Second).Format(time.RFC3339Nano)
	if err := expiryScan(t, s, now, "subscription", expiry); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(24*time.Hour))
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("cached clock crossing invented a fresh expiration observation")
	}
	fresh := now.Add(24 * time.Hour)
	if err := expiryScan(t, s, fresh, "subscription", expiry); err != nil {
		t.Fatal(err)
	}
	history := alertHistory(t, s)
	if len(history) != 1 || history[0].Kind != "alert" || history[0].DeliveryStatus != "pending" || !strings.Contains(history[0].Body, "已开自动续费") {
		t.Fatal(history)
	}
	if err := expiryScan(t, s, fresh.Add(time.Second), "subscription", expiry); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, fresh.Add(2*time.Second))
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("same active expiry repeated or disappeared")
	}
	if err := expiryScan(t, s, fresh.Add(3*time.Second), "subscription", nil); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, fresh.Add(4*time.Second))
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("unknown expiry generated recovery")
	}
	if err := expiryScan(t, s, fresh.Add(5*time.Second), "subscription", now.Add(40*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	history = alertHistory(t, s)
	if len(history) != 2 || history[0].Kind != "recovery" || history[1].DeliveryStatus == "pending" {
		t.Fatal("renewal failed to recover and cancel stale reminder", history)
	}
	if err := expiryScan(t, s, fresh.Add(6*time.Second), "subscription", now.Add(5*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("new expiration incident not recorded")
	}
	if _, err := s.DB.Exec("UPDATE resource_snapshot SET present=0 WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, fresh.Add(7*time.Second))
	var pending int
	if err := s.DB.QueryRow("SELECT count(*) FROM notification WHERE rule_key='instance_expiring' AND delivery_status='pending'").Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
}

func TestChangedExpirationWithinWindowStartsNewIncident(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	for _, days := range []int{1, 2, 2} {
		if err := expiryScan(t, s, now, "subscription", now.Add(time.Duration(days)*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	history := alertHistory(t, s)
	if len(history) != 2 || history[0].Kind != "alert" || history[1].Kind != "alert" {
		t.Fatal(history)
	}
}

func TestExpirationControlsAndAtomicNotification(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	date := now.Add(time.Hour).Format(time.RFC3339Nano)
	zero := 0
	if _, err := s.UpdateAlertRule(ctx, "instance_expiring", model.AlertRuleUpdate{Params: &model.AlertParams{BeforeDays: &zero}}, now); err != nil {
		t.Fatal(err)
	}
	if err := expiryScan(t, s, now, "subscription", date); err != nil {
		t.Fatal(err)
	}
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("zero-day threshold fired early")
	}
	off := false
	if _, err := s.UpdateAlertRule(ctx, "instance_expiring", model.AlertRuleUpdate{Enabled: &off}, now); err != nil {
		t.Fatal(err)
	}
	if err := expiryScan(t, s, now.Add(2*time.Hour), "subscription", date); err != nil {
		t.Fatal(err)
	}
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("disabled rule fired")
	}
	on := true
	if _, err := s.UpdateAlertRule(ctx, "instance_expiring", model.AlertRuleUpdate{Enabled: &on}, now); err != nil {
		t.Fatal(err)
	}
	for _, charge := range []string{"unknown", "pay_as_you_go", "spot"} {
		if err := expiryScan(t, s, now.Add(2*time.Hour), charge, date); err != nil {
			t.Fatal(err)
		}
	}
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("non-subscription or unknown billing fired")
	}
	if _, err := s.DB.Exec("CREATE TRIGGER reject_expiry_notice BEFORE INSERT ON notification BEGIN SELECT RAISE(ABORT,'test notification failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := expiryScan(t, s, now.Add(2*time.Hour), "subscription", date); err == nil {
		t.Fatal("notification failure did not fail transaction")
	}
	var active int
	if err := s.DB.QueryRow("SELECT count(*) FROM alert_state WHERE rule_key='instance_expiring' AND active=1").Scan(&active); err != nil || active != 0 {
		t.Fatal("state committed without notification", active, err)
	}
	var charge string
	if err := s.DB.QueryRow("SELECT json_extract(payload_json,'$.charge_type') FROM resource_snapshot WHERE id='ecs'").Scan(&charge); err != nil || charge != "spot" {
		t.Fatal("snapshot committed without notification", charge, err)
	}
}

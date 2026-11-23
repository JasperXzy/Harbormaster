package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func quietStore(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s := notificationStore(t)
	now := time.Date(2026, 11, 12, 23, 0, 0, 0, time.UTC)
	if _, err := s.DB.Exec("INSERT INTO app_meta(key,value) VALUES('business_timezone','UTC')"); err != nil {
		t.Fatal(err)
	}
	channelFixture(t, s, 0, now)
	return s, now
}
func quietPolicy(t *testing.T, s *Store, now time.Time, enabled bool) model.QuietHours {
	t.Helper()
	q, err := s.QuietHours(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	q.Enabled = enabled
	q.Start = "23:00"
	q.End = "08:00"
	q, err = s.SaveQuietHours(context.Background(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	return q
}
func TestQuietEnqueueIsTerminalAndUrgentEventsBypass(t *testing.T) {
	s, now := quietStore(t)
	ctx := context.Background()
	quietPolicy(t, s, now, true)
	n, err := s.EnqueueNotification(ctx, notice("quiet"), now)
	if err != nil || n.DeliveryStatus != "not_sent" || n.SuppressionReason == nil || *n.SuppressionReason != "daily" || n.SuppressedAt == nil || n.Urgent {
		t.Fatal(n, err)
	}
	quietPolicy(t, s, now.Add(time.Minute), false)
	dup, err := s.EnqueueNotification(ctx, notice("quiet"), now.Add(time.Minute))
	if err != nil || dup.ID != n.ID || dup.DeliveryStatus != "not_sent" {
		t.Fatal("quiet record replayed", dup, err)
	}
	if _, err = s.ClaimNotification(ctx, now.Add(time.Minute), 3, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("suppressed event claimable", err)
	}
	quietPolicy(t, s, now.Add(2*time.Minute), true)
	for _, key := range []string{"agent_offline", "cdt_usage", "cdt_forecast_exceed", "cdt_official_unavailable"} {
		in := notice(key)
		in.RuleKey = &key
		in.Severity = "info"
		n, err = s.EnqueueNotification(ctx, in, now.Add(2*time.Minute))
		if err != nil || !n.Urgent || n.DeliveryStatus != "pending" || n.SuppressionReason != nil {
			t.Fatal("urgent rule suppressed", n, err)
		}
	}
	protection := notice("protection-action")
	protection.Urgent = true
	n, err = s.EnqueueNotification(ctx, protection, now.Add(2*time.Minute))
	if err != nil || n.DeliveryStatus != "pending" {
		t.Fatal(n, err)
	}
	job, err := s.EnqueueTelegramTest(ctx, "test", "placeholder", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err = s.DB.QueryRow("SELECT delivery_status FROM notification WHERE job_id=?", job.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatal("explicit test suppressed", status, err)
	}
}
func TestQuietClaimAndPreSendGateKeepAlreadyLeasedEvidence(t *testing.T) {
	s, at := quietStore(t)
	ctx := context.Background()
	before := at.Add(-time.Second)
	quietPolicy(t, s, before, true)
	n, err := s.EnqueueNotification(ctx, notice("before-boundary"), before)
	if err != nil || n.DeliveryStatus != "pending" {
		t.Fatal(n, err)
	}
	leased, err := s.ClaimNotification(ctx, before, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := s.AuthorizeNotification(ctx, leased, at)
	if err != nil || allowed {
		t.Fatal("pre-send boundary ignored", allowed, err)
	}
	stopped, err := s.Notification(ctx, n.ID)
	if err != nil || stopped.DeliveryStatus != "not_sent" || stopped.Uncertain || stopped.Attempts != 1 {
		t.Fatal(stopped, err)
	}
	if err = s.FinishNotification(ctx, leased, 123, "", nil, false, at); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("suppressed owner wrote sent", err)
	}
	// Saving a policy cannot pretend an already leased/authorized send was recalled.
	quietPolicy(t, s, at, false)
	n, err = s.EnqueueNotification(ctx, notice("in-flight"), at)
	if err != nil {
		t.Fatal(err)
	}
	leased, err = s.ClaimNotification(ctx, at, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err = s.AuthorizeNotification(ctx, leased, at); err != nil || !allowed {
		t.Fatal(allowed, err)
	}
	quietPolicy(t, s, at, true)
	current, err := s.Notification(ctx, n.ID)
	if err != nil || current.DeliveryStatus != "pending" {
		t.Fatal("in-flight evidence overwritten", current, err)
	}
	if err = s.FinishNotification(ctx, leased, 123, "", nil, false, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}
func TestQuietSuppressesQueuedDueAndMaintenanceWithoutResurrecting(t *testing.T) {
	s, now := quietStore(t)
	ctx := context.Background()
	n, err := s.EnqueueNotification(ctx, notice("queued"), now)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.QuietHours(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	q.Maintenance = &model.MaintenanceWindow{StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Minute)}
	saved, err := s.SaveQuietHours(ctx, q, now)
	if err != nil || !saved.Active || saved.Enabled {
		t.Fatal(saved, err)
	}
	if _, err = s.SaveQuietHours(ctx, q, now); !errors.Is(err, ErrQuietChanged) {
		t.Fatal("stale update accepted", err)
	}
	got, err := s.Notification(ctx, n.ID)
	if err != nil || got.DeliveryStatus != "not_sent" || got.SuppressionReason == nil || *got.SuppressionReason != "maintenance" {
		t.Fatal(got, err)
	}
	if _, err = s.ClaimNotification(ctx, now.Add(2*time.Minute), 3, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("maintenance event replayed", err)
	}
	n, err = s.EnqueueNotification(ctx, notice("after-maintenance"), now.Add(2*time.Minute))
	if err != nil || n.DeliveryStatus != "pending" {
		t.Fatal(n, err)
	}
}
func TestQuietClaimSuppressesAQueuedNoticeWhenWindowStarts(t *testing.T) {
	s, now := quietStore(t)
	ctx := context.Background()
	before := now.Add(-time.Second)
	quietPolicy(t, s, before, true)
	n, err := s.EnqueueNotification(ctx, notice("queued-until-window"), before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNotification(ctx, now, 3, false); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err := s.Notification(ctx, n.ID)
	if err != nil || got.DeliveryStatus != "not_sent" || got.Attempts != 0 {
		t.Fatal(got, err)
	}
}

func TestQuietRetainsUncertainEvidenceWhenSuppressingAnExpiredLease(t *testing.T) {
	s, now := quietStore(t)
	ctx := context.Background()
	before := now.Add(-time.Second)
	quietPolicy(t, s, before, true)
	n, err := s.EnqueueNotification(ctx, notice("uncertain"), before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNotification(ctx, before, 3, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNotification(ctx, now.Add(time.Minute), 3, true); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err := s.Notification(ctx, n.ID)
	if err != nil || got.DeliveryStatus != "not_sent" || !got.Uncertain || got.SuppressionReason == nil {
		t.Fatal("uncertain delivery hidden", got, err)
	}
}

func TestNotificationAuthorizationRechecksDeliveryTestJob(t *testing.T) {
	s, now := quietStore(t)
	ctx := context.Background()
	j, err := s.EnqueueTelegramTest(ctx, "test", "placeholder", now)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s.ClaimNotification(ctx, now, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE job SET status='failed' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AuthorizeNotification(ctx, leased, now); allowed || !errors.Is(err, ErrLeaseLost) {
		t.Fatal("cancelled test authorized", allowed, err)
	}
}

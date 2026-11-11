package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func notificationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func channelFixture(t *testing.T, s *Store, revision int64, now time.Time) {
	t.Helper()
	box, _ := security.NewBox(bytes.Repeat([]byte{6}, 32))
	sealed, err := box.Encrypt([]byte(`{"fixture":"not a real channel"}`), "test-channel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveTelegramChannel(context.Background(), sealed, revision, now); err != nil {
		t.Fatal(err)
	}
}
func notice(key string) model.Notification {
	return model.Notification{EventKey: key, Kind: "alert", Severity: "warning", Title: "placeholder title", Body: "placeholder body"}
}
func TestNotificationDedupeNoChannelAndAtomicTestJob(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	n, err := s.EnqueueNotification(ctx, notice("event-one"), now)
	if err != nil || n.DeliveryStatus != "not_sent" {
		t.Fatal(n, err)
	}
	duplicate, err := s.EnqueueNotification(ctx, notice("event-one"), now.Add(time.Second))
	if err != nil || duplicate.ID != n.ID {
		t.Fatal("duplicate event queued", err)
	}
	if _, err = s.EnqueueTelegramTest(ctx, "test", "placeholder", now); !errors.Is(err, ErrTelegramNotConfigured) {
		t.Fatal(err)
	}
	channelFixture(t, s, 0, now)
	old, err := s.Notification(ctx, n.ID)
	if err != nil || old.DeliveryStatus != "not_sent" {
		t.Fatal("old history was retroactively sent", err)
	}
	if _, err = s.EnqueueTelegramTest(ctx, "", "invalid empty title", now); err == nil {
		t.Fatal("invalid test accepted")
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM job").Scan(&count); err != nil || count != 0 {
		t.Fatal("test failure left an orphan job")
	}
	job, err := s.EnqueueTelegramTest(ctx, "test", "placeholder", now)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.EnqueueTelegramTest(ctx, "test", "placeholder", now)
	if err != nil || same.ID != job.ID {
		t.Fatal("repeated active test created another job")
	}
	if _, err = s.ClaimCollectionJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("read worker consumed Telegram send job", err)
	}
	leased, err := s.ClaimNotification(ctx, now, 3, false)
	if err != nil || leased.JobID != job.ID {
		t.Fatal(leased, err)
	}
	if _, err = s.ClaimNotification(ctx, now, 3, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("lease claimed twice", err)
	}
	if err = s.FinishNotification(ctx, leased, 42, "", nil, false, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	final, err := s.Job(ctx, job.ID)
	if err != nil || final.Status != "succeeded" {
		t.Fatal(final, err)
	}
	delivered, err := s.Notification(ctx, leased.ID)
	if err != nil || delivered.DeliveryStatus != "sent" || delivered.MessageID == nil || *delivered.MessageID != 42 {
		t.Fatal(delivered, err)
	}
	_, state, err := s.TelegramChannel(ctx)
	if err != nil || state.LastSuccessAt == nil || state.LastError != nil {
		t.Fatal(state, err)
	}
	if err = s.FinishNotification(ctx, leased, 42, "", nil, false, now.Add(2*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("late duplicate completion succeeded")
	}
}
func TestChannelChangeFencesInFlightSendsAndTestJob(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	channelFixture(t, s, 0, now)
	job, err := s.EnqueueTelegramTest(ctx, "test", "placeholder", now)
	if err != nil {
		t.Fatal(err)
	}
	inFlight, err := s.ClaimNotification(ctx, now, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	channelFixture(t, s, 1, now.Add(time.Second))
	if err = s.FinishNotification(ctx, inFlight, 99, "", nil, false, now.Add(2*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale sender marked new channel healthy")
	}
	n, err := s.Notification(ctx, inFlight.ID)
	if err != nil || n.DeliveryStatus != "failed" || !n.Uncertain || n.LastError.Code != "telegram_configuration_changed" {
		t.Fatal(n, err)
	}
	final, err := s.Job(ctx, job.ID)
	if err != nil || final.Status != "failed" || final.Error.Code != "telegram_configuration_changed" {
		t.Fatal(final, err)
	}
	_, state, err := s.TelegramChannel(ctx)
	if err != nil || state.LastSuccessAt != nil || state.LastError != nil {
		t.Fatal("new channel inherited stale delivery health", err)
	}
}
func TestLeaseRecoveryRequiresExplicitUncertainPolicy(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "retry"}[retry], func(t *testing.T) {
			s := notificationStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			channelFixture(t, s, 0, now)
			n, err := s.EnqueueNotification(ctx, notice("event"), now)
			if err != nil {
				t.Fatal(err)
			}
			abandoned, err := s.ClaimNotification(ctx, now, 3, retry)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := s.ClaimNotification(ctx, now.Add(61*time.Second), 3, retry)
			if retry {
				if err != nil || recovered.ID != n.ID || recovered.Owner == abandoned.Owner || recovered.Attempts != 2 || !recovered.Uncertain {
					t.Fatal(recovered, err)
				}
			} else {
				if !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				failed, err := s.Notification(ctx, n.ID)
				if err != nil || failed.DeliveryStatus != "failed" || !failed.Uncertain || failed.LastError.Code != "telegram_delivery_uncertain" {
					t.Fatal(failed, err)
				}
			}
			if err = s.FinishNotification(ctx, abandoned, 1, "", nil, false, now.Add(62*time.Second)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("expired sender completed work", err)
			}
		})
	}
}
func TestRetryTimeAttemptBoundAndPaging(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	channelFixture(t, s, 0, now)
	job, err := s.EnqueueTelegramTest(ctx, "test", "placeholder", now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimNotification(ctx, now, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	retry := now.Add(1500 * time.Millisecond)
	if err = s.FinishNotification(ctx, first, 0, "telegram_rate_limited", &retry, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNotification(ctx, now.Add(time.Second), 2, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("retried before retry_after elapsed")
	}
	second, err := s.ClaimNotification(ctx, now.Add(2*time.Second), 2, false)
	if err != nil || second.Attempts != 2 {
		t.Fatal(second, err)
	}
	retry = now.Add(4 * time.Second)
	if err = s.FinishNotification(ctx, second, 0, "telegram_unreachable", &retry, false, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNotification(ctx, now.Add(4*time.Second), 2, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("attempt cap ignored", err)
	}
	final, err := s.Job(ctx, job.ID)
	if err != nil || final.Status != "failed" || final.Error.Code != "retry_exhausted" {
		t.Fatal(final, err)
	}
	for _, key := range []string{"second", "third"} {
		if _, err = s.EnqueueNotification(ctx, notice(key), now); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Notifications(ctx, 0, 2)
	if err != nil || len(page) != 2 || page[0].EventKey != "third" || page[1].EventKey != "second" {
		t.Fatal(page, err)
	}
	older, err := s.Notifications(ctx, page[1].Sequence, 2)
	if err != nil || len(older) != 1 || older[0].ID != first.ID {
		t.Fatal(older, err)
	}
}

func TestConcurrentDispatchClaimAndRestartDoNotResendByDefault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Now().UTC()
	first, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	channelFixture(t, first, 0, now)
	n, err := first.EnqueueNotification(ctx, notice("concurrent"), now)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		notification model.Notification
		err          error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, s := range []*Store{first, second} {
		go func(s *Store) { <-start; n, err := s.ClaimNotification(ctx, now, 1, false); results <- result{n, err} }(s)
	}
	close(start)
	claimed, empty := 0, 0
	for range 2 {
		r := <-results
		if r.err == nil {
			claimed++
		} else if errors.Is(r.err, ErrNotFound) {
			empty++
		} else {
			t.Fatal(r.err)
		}
	}
	if claimed != 1 || empty != 1 {
		t.Fatal("notification claimed more than once", claimed, empty)
	}
	first.Close()
	second.Close()
	restarted, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err = restarted.ClaimNotification(ctx, now.Add(61*time.Second), 1, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("restart automatically resent uncertain delivery", err)
	}
	failed, err := restarted.Notification(ctx, n.ID)
	if err != nil || !failed.Uncertain || failed.LastError == nil || failed.LastError.Code != "telegram_delivery_uncertain" {
		t.Fatal("attempt limit hid uncertain outcome", failed, err)
	}
}

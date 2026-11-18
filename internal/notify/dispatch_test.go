package notify

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

type sendFunc func(context.Context, telegram.Destination, string) (telegram.Receipt, error)

func (f sendFunc) Send(ctx context.Context, d telegram.Destination, text string) (telegram.Receipt, error) {
	return f(ctx, d, text)
}

func dispatchFixture(t *testing.T) (*store.Store, Channels, *time.Time) {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, _ := security.NewBox(bytes.Repeat([]byte{7}, 32))
	channels := Channels{Store: s, Box: box}
	if _, err = channels.Replace(context.Background(), telegram.Destination{Token: "123456789:TEST_ONLY_not_a_real_bot_token", ChatID: "-100123456789", ProxyMode: "none"}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	return s, channels, &now
}

func enqueue(t *testing.T, s *store.Store, key string, now time.Time) model.Notification {
	t.Helper()
	n, err := s.EnqueueNotification(context.Background(), model.Notification{EventKey: key, Kind: "alert", Severity: "warning", Title: "标题", Body: "正文"}, now)
	if err != nil || n.DeliveryStatus != "pending" {
		t.Fatal(n, err)
	}
	return n
}

func TestDispatcherSendsOnceAndRecordsChannelHealth(t *testing.T) {
	s, channels, now := dispatchFixture(t)
	ctx := context.Background()
	n := enqueue(t, s, "one", *now)
	calls := 0
	d := Dispatcher{Channels: channels, Now: func() time.Time { return *now }, Sender: sendFunc(func(_ context.Context, dest telegram.Destination, text string) (telegram.Receipt, error) {
		calls++
		if dest.ChatID != "-100123456789" || text != "标题\n正文" {
			t.Fatal("unexpected send", dest.ChatID, text)
		}
		return telegram.Receipt{MessageID: 42}, nil
	})}
	if worked, err := d.RunOne(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if worked, err := d.RunOne(ctx); err != nil || worked || calls != 1 {
		t.Fatal("sent twice", worked, err, calls)
	}
	got, _ := s.Notification(ctx, n.ID)
	if got.DeliveryStatus != "sent" || got.DeliveredAt == nil || got.Uncertain {
		t.Fatal(got)
	}
	_, state, err := channels.Load(ctx)
	if err != nil || state.LastSuccessAt == nil || state.LastError != nil {
		t.Fatal(state, err)
	}
}

func TestDispatcherRetriesOnlyDefiniteNonDelivery(t *testing.T) {
	s, channels, now := dispatchFixture(t)
	ctx := context.Background()
	n := enqueue(t, s, "retry", *now)
	results := []error{
		&telegram.Failure{Code: "telegram_unreachable", Retryable: true},
		&telegram.Failure{Code: "telegram_rate_limited", Retryable: true, RetryAfter: 17 * time.Second},
		nil,
	}
	calls := 0
	d := Dispatcher{Channels: channels, Now: func() time.Time { return *now }, Sender: sendFunc(func(context.Context, telegram.Destination, string) (telegram.Receipt, error) {
		err := results[calls]
		calls++
		if err != nil {
			return telegram.Receipt{}, err
		}
		return telegram.Receipt{MessageID: 7}, nil
	})}
	if _, err := d.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Notification(ctx, n.ID)
	if got.DeliveryStatus != "pending" || got.LastError == nil || got.LastError.Code != "telegram_unreachable" {
		t.Fatal(got)
	}
	// Not due before the first backoff.
	*now = now.Add(9 * time.Second)
	if worked, _ := d.RunOne(ctx); worked {
		t.Fatal("retried before backoff")
	}
	*now = now.Add(time.Second)
	if _, err := d.RunOne(ctx); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	*now = now.Add(16 * time.Second)
	if worked, _ := d.RunOne(ctx); worked {
		t.Fatal("ignored retry_after")
	}
	*now = now.Add(time.Second)
	if _, err := d.RunOne(ctx); err != nil || calls != 3 {
		t.Fatal(err, calls)
	}
	got, _ = s.Notification(ctx, n.ID)
	if got.DeliveryStatus != "sent" || got.Attempts != 3 || got.Uncertain {
		t.Fatal(got)
	}
}

func TestDispatcherStopsOnUncertainOutcome(t *testing.T) {
	s, channels, now := dispatchFixture(t)
	ctx := context.Background()
	n := enqueue(t, s, "uncertain", *now)
	calls := 0
	d := Dispatcher{Channels: channels, Now: func() time.Time { return *now }, Sender: sendFunc(func(context.Context, telegram.Destination, string) (telegram.Receipt, error) {
		calls++
		return telegram.Receipt{}, &telegram.Failure{Code: "telegram_unreachable", Uncertain: true}
	})}
	if _, err := d.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	if worked, _ := d.RunOne(ctx); worked || calls != 1 {
		t.Fatal("uncertain delivery was retried", calls)
	}
	got, _ := s.Notification(ctx, n.ID)
	if got.DeliveryStatus != "failed" || !got.Uncertain || got.LastError == nil || got.LastError.Code != "telegram_delivery_uncertain" {
		t.Fatal(got)
	}
}

func TestDispatcherDoesNotResendAfterExpiredLease(t *testing.T) {
	s, channels, now := dispatchFixture(t)
	ctx := context.Background()
	n := enqueue(t, s, "crashed", *now)
	// A previous process claimed the notice and died before recording a result.
	if _, err := s.ClaimNotification(ctx, *now, MaxAttempts, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Minute)
	d := Dispatcher{Channels: channels, Now: func() time.Time { return *now }, Sender: sendFunc(func(context.Context, telegram.Destination, string) (telegram.Receipt, error) {
		t.Fatal("expired lease was resent")
		return telegram.Receipt{}, nil
	})}
	if worked, err := d.RunOne(ctx); err != nil || worked {
		t.Fatal(worked, err)
	}
	got, _ := s.Notification(ctx, n.ID)
	if got.DeliveryStatus != "failed" || !got.Uncertain {
		t.Fatal(got)
	}
}

func TestDispatcherGivesUpAfterPermanentFailureOrExhaustion(t *testing.T) {
	s, channels, now := dispatchFixture(t)
	ctx := context.Background()
	permanent := enqueue(t, s, "permanent", *now)
	d := Dispatcher{Channels: channels, Now: func() time.Time { return *now }, Sender: sendFunc(func(context.Context, telegram.Destination, string) (telegram.Receipt, error) {
		return telegram.Receipt{}, &telegram.Failure{Code: "telegram_chat_not_found"}
	})}
	if _, err := d.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Notification(ctx, permanent.ID)
	if got.DeliveryStatus != "failed" || got.LastError.Code != "telegram_chat_not_found" || got.Uncertain {
		t.Fatal(got)
	}
	_, state, _ := channels.Load(ctx)
	if state.LastError == nil || state.LastError.Code != "telegram_chat_not_found" {
		t.Fatal(state)
	}
	exhausted := enqueue(t, s, "exhausted", *now)
	calls := 0
	d.Sender = sendFunc(func(context.Context, telegram.Destination, string) (telegram.Receipt, error) {
		calls++
		return telegram.Receipt{}, &telegram.Failure{Code: "telegram_unreachable", Retryable: true}
	})
	for range 20 {
		*now = now.Add(15 * time.Minute)
		if _, err := d.RunOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.Notification(ctx, exhausted.ID)
	if calls != MaxAttempts || got.DeliveryStatus != "failed" || got.Attempts != MaxAttempts {
		t.Fatal(calls, got)
	}
}

func TestDispatcherCompletesTestJob(t *testing.T) {
	s, channels, now := dispatchFixture(t)
	ctx := context.Background()
	j, err := s.EnqueueTelegramTest(ctx, "⚪ 测试消息", "正文", *now)
	if err != nil {
		t.Fatal(err)
	}
	d := Dispatcher{Channels: channels, Now: func() time.Time { return *now }, Sender: sendFunc(func(_ context.Context, _ telegram.Destination, text string) (telegram.Receipt, error) {
		if !strings.HasPrefix(text, "⚪ 测试消息\n") {
			t.Fatal(text)
		}
		return telegram.Receipt{}, &telegram.Failure{Code: "telegram_unauthorized"}
	})}
	if _, err = d.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "failed" || got.Error == nil || got.Error.Code != "telegram_unauthorized" {
		t.Fatal(got, err)
	}
}

func TestUpdateKeepsSecretsAndClearsProxyAuthOnAddressChange(t *testing.T) {
	s, channels, _ := dispatchFixture(t)
	ctx := context.Background()
	user, pass := "proxy-user", "proxy-pass"
	if _, err := channels.Update(ctx, Update{ChatID: "-100123456789", ProxyMode: "socks5", ProxyURL: "socks5://127.0.0.1:1080", ProxyUsername: &user, ProxyPassword: &pass}, time.Now()); err != nil {
		t.Fatal(err)
	}
	d, _, _ := channels.Load(ctx)
	if d.Token != "123456789:TEST_ONLY_not_a_real_bot_token" || d.ProxyUsername != user {
		t.Fatal("omitted token was not kept or auth not saved")
	}
	// Same mode and address: omitted auth is kept.
	if _, err := channels.Update(ctx, Update{ChatID: "-100999", ProxyMode: "socks5", ProxyURL: "socks5://127.0.0.1:1080"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if d, _, _ = channels.Load(ctx); d.ProxyUsername != user || d.ChatID != "-100999" {
		t.Fatal("auth dropped while proxy unchanged")
	}
	// Changed address: old auth is never sent to the new proxy.
	if _, err := channels.Update(ctx, Update{ChatID: "-100999", ProxyMode: "socks5", ProxyURL: "socks5://10.0.0.1:1080"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if d, _, _ = channels.Load(ctx); d.ProxyUsername != "" || d.ProxyPassword != "" {
		t.Fatal("auth retained for a different proxy")
	}
	// Explicit empty strings clear; a lone username is rejected.
	var invalid *FieldError
	if _, err := channels.Update(ctx, Update{ChatID: "-100999", ProxyMode: "socks5", ProxyURL: "socks5://10.0.0.1:1080", ProxyUsername: &user}, time.Now()); !errors.As(err, &invalid) {
		t.Fatal("half credential accepted", err)
	}
	bad := "not-a-token"
	if _, err := channels.Update(ctx, Update{BotToken: &bad, ChatID: "-100999", ProxyMode: "none"}, time.Now()); !errors.As(err, &invalid) || invalid.Fields["bot_token"] != "invalid_format" {
		t.Fatal(err)
	}
	if strings.Contains(invalid.Error(), bad) {
		t.Fatal("validation error echoed a secret")
	}
	entries, err := s.AuditLog(ctx, model.AuditFilter{Action: "telegram_channel_updated", Limit: 20})
	if err != nil || len(entries) != 4 {
		t.Fatal(len(entries), err)
	}
	for _, e := range entries {
		raw := e.Summary + strings.Join(e.Details.ChangedFields, ",")
		if strings.Contains(raw, user) || strings.Contains(raw, pass) || strings.Contains(raw, "TEST_ONLY") {
			t.Fatal("audit exposed a secret")
		}
	}
}

func TestUpdateRequiresTokenOnFirstSave(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := security.NewBox(bytes.Repeat([]byte{8}, 32))
	var invalid *FieldError
	if _, err = (Channels{Store: s, Box: box}).Update(context.Background(), Update{ChatID: "-100123", ProxyMode: "none"}, time.Now()); !errors.As(err, &invalid) || invalid.Fields["bot_token"] != "required" {
		t.Fatal(err)
	}
}

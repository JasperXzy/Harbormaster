package notify

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

// User-confirmed delivery policy: only failures that provably did not reach
// Telegram (connection never written, or an explicit 429) are retried.
// An uncertain outcome, including an expired send lease, stops as failed and
// waits for the administrator, so a message is never knowingly duplicated.
const (
	MaxAttempts    = 5
	retryUncertain = false
)

// Backoff between definite non-deliveries; 429 uses Telegram's retry_after.
var retryDelays = []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}

type Sender interface {
	Send(context.Context, telegram.Destination, string) (telegram.Receipt, error)
}

type Dispatcher struct {
	Channels Channels
	Sender   Sender
	Log      *slog.Logger
	Now      func() time.Time
}

func (d Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for {
			worked, err := d.RunOne(ctx)
			if err != nil && ctx.Err() == nil && d.Log != nil {
				d.Log.Warn("notification_dispatch_failed")
			}
			if !worked || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOne sends at most one notification. It reports whether a lease was taken.
func (d Dispatcher) RunOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if d.Channels.Store == nil || d.Sender == nil {
		return false, errors.New("notification_dispatch_unavailable")
	}
	s := d.Channels.Store
	n, err := s.ClaimNotification(ctx, d.now(), MaxAttempts, retryUncertain)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Local cleanup must still run after cancellation; it never sends.
	finish := func(messageID int64, code string, retryAt *time.Time, uncertain bool) error {
		finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		err := s.FinishNotification(finishCtx, n, messageID, code, retryAt, uncertain, d.now())
		if errors.Is(err, store.ErrLeaseLost) {
			return nil
		}
		return err
	}
	destination, state, err := d.Channels.Load(ctx)
	if err != nil {
		return true, finish(0, "telegram_configuration_unavailable", nil, false)
	}
	if state.Revision != n.ChannelRevision {
		return true, finish(0, "telegram_configuration_changed", nil, false)
	}
	allowed, err := s.AuthorizeNotification(ctx, n, d.now())
	if errors.Is(err, store.ErrLeaseLost) {
		return true, nil
	}
	if err != nil || !allowed {
		return true, err
	}
	// The lease is one minute; the transport times out after 15 seconds.
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	receipt, err := d.Sender.Send(sendCtx, destination, Render(n))
	cancel()
	if err == nil {
		return true, finish(receipt.MessageID, "", nil, false)
	}
	failure := telegram.Classify(err)
	if failure.Uncertain {
		return true, finish(0, "telegram_delivery_uncertain", nil, true)
	}
	if failure.Retryable && n.Attempts < MaxAttempts {
		delay := failure.RetryAfter
		if delay <= 0 {
			delay = retryDelays[min(n.Attempts-1, len(retryDelays)-1)]
		}
		at := d.now().Add(delay)
		return true, finish(0, failure.Code, &at, false)
	}
	return true, finish(0, failure.Code, nil, false)
}

// Render produces the plain-text Telegram message from the stored record, so
// panel history and Telegram always show the same title and body.
func Render(n model.Notification) string {
	if n.Body == "" || n.Body == n.Title {
		return n.Title
	}
	return n.Title + "\n" + n.Body
}

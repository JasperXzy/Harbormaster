// Package notify connects encrypted channel storage to notification delivery.
// Production API update semantics and dispatch policy are wired separately.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

var ErrConfiguration = errors.New("telegram_configuration_unavailable")

type Channels struct {
	Store *store.Store
	Box   *security.Box
}

func (c Channels) Load(ctx context.Context) (telegram.Destination, model.TelegramChannelState, error) {
	sealed, state, err := c.Store.TelegramChannel(ctx)
	if err != nil {
		return telegram.Destination{}, state, err
	}
	if c.Box == nil {
		return telegram.Destination{}, state, ErrConfiguration
	}
	raw, err := c.Box.Decrypt(sealed, model.TelegramCredentialOwner)
	if err != nil {
		return telegram.Destination{}, state, ErrConfiguration
	}
	defer clear(raw)
	var destination telegram.Destination
	if json.Unmarshal(raw, &destination) != nil || telegram.Validate(destination) != nil {
		return telegram.Destination{}, state, ErrConfiguration
	}
	return destination, state, nil
}

// Replace is an internal full-config operation; browser patch/retain/clear rules
// must first produce one complete validated destination without logging it.
func (c Channels) Replace(ctx context.Context, d telegram.Destination, expectedRevision int64, now time.Time) (model.TelegramChannelState, error) {
	if err := telegram.Validate(d); err != nil {
		return model.TelegramChannelState{}, err
	}
	if c.Box == nil {
		return model.TelegramChannelState{}, ErrConfiguration
	}
	previous, state, err := c.Load(ctx)
	if err != nil && !errors.Is(err, store.ErrTelegramNotConfigured) {
		return state, err
	}
	if state.Revision != expectedRevision {
		return state, store.ErrChannelChanged
	}
	if err == nil && previous == d {
		return state, nil
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return model.TelegramChannelState{}, ErrConfiguration
	}
	defer clear(raw)
	sealed, err := c.Box.Encrypt(raw, model.TelegramCredentialOwner)
	if err != nil {
		return model.TelegramChannelState{}, ErrConfiguration
	}
	return c.Store.SaveTelegramChannel(ctx, sealed, expectedRevision, now)
}

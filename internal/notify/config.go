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
	// Only field names reach the audit journal; values are compared in memory.
	details := model.AuditDetails{ChangedFields: []string{}}
	for _, f := range []struct {
		name    string
		changed bool
	}{{"chat_id", previous.ChatID != d.ChatID}, {"proxy_mode", previous.ProxyMode != d.ProxyMode}, {"proxy_url", previous.ProxyURL != d.ProxyURL}} {
		if f.changed {
			details.ChangedFields = append(details.ChangedFields, f.name)
		}
	}
	details.CredentialsChanged = previous.Token != d.Token || previous.ProxyUsername != d.ProxyUsername || previous.ProxyPassword != d.ProxyPassword
	return c.Store.SaveTelegramChannel(ctx, sealed, expectedRevision, details, now)
}

// Update is the browser write path. Omitted secrets are write-only "keep"
// markers. User-confirmed proxy rule: saved authentication survives only while
// the proxy mode and address are unchanged; any change clears it unless new
// authentication is supplied in the same request. Both empty strings clear it.
type Update struct {
	BotToken      *string
	ChatID        string
	ProxyMode     string
	ProxyURL      string
	ProxyUsername *string
	ProxyPassword *string
}

// FieldError reports invalid request fields by name only, never by value.
type FieldError struct{ Fields map[string]string }

func (e *FieldError) Error() string { return "validation_failed" }

func (c Channels) Update(ctx context.Context, in Update, now time.Time) (model.TelegramChannelState, error) {
	previous, state, err := c.Load(ctx)
	configured := err == nil
	if err != nil && !errors.Is(err, store.ErrTelegramNotConfigured) {
		return state, err
	}
	d := telegram.Destination{ChatID: in.ChatID, ProxyMode: in.ProxyMode, ProxyURL: in.ProxyURL}
	if d.ProxyMode == "none" {
		d.ProxyURL = ""
	}
	switch {
	case in.BotToken != nil:
		d.Token = *in.BotToken
	case configured:
		d.Token = previous.Token
	default:
		return state, &FieldError{Fields: map[string]string{"bot_token": "required"}}
	}
	if (in.ProxyUsername == nil) != (in.ProxyPassword == nil) {
		return state, &FieldError{Fields: map[string]string{"proxy_password": "required"}}
	}
	if in.ProxyUsername != nil {
		d.ProxyUsername, d.ProxyPassword = *in.ProxyUsername, *in.ProxyPassword
	} else if configured && previous.ProxyMode == d.ProxyMode && previous.ProxyURL == d.ProxyURL {
		d.ProxyUsername, d.ProxyPassword = previous.ProxyUsername, previous.ProxyPassword
	}
	if fields := telegram.FieldErrors(d); len(fields) > 0 {
		return state, &FieldError{Fields: fields}
	}
	return c.Replace(ctx, d, state.Revision, now)
}

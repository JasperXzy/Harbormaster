package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/notify"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) notificationReads(w http.ResponseWriter, r *http.Request, path string) bool {
	if path == "/notify/telegram" && r.Method == "PUT" {
		p.updateTelegram(w, r)
		return true
	}
	if path == "/notify/telegram/test" {
		if r.Method != "POST" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		p.testTelegram(w, r)
		return true
	}
	if r.Method != "GET" {
		return false
	}
	switch path {
	case "/notify/telegram":
		p.writeTelegramChannel(w, r, 200)
	case "/notifications":
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 200 {
				fieldError(w, "limit", "out_of_range")
				return true
			}
			limit = n
		}
		var before int64
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			if err != nil || len(decoded) > 20 {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
			before, err = strconv.ParseInt(string(decoded), 10, 64)
			if err != nil || before <= 0 {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
		}
		items, err := p.s.Notifications(r.Context(), before, limit+1)
		if err != nil {
			businessError(w, err)
			return true
		}
		var next any
		if len(items) > limit {
			next = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(items[limit-1].Sequence, 10)))
			items = items[:limit]
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
	default:
		return false
	}
	return true
}

func (p *productionPanel) channels() notify.Channels {
	manager := notify.Channels{Store: p.s}
	if p.engine != nil {
		manager.Box = p.engine.Box
	}
	return manager
}

func (p *productionPanel) writeTelegramChannel(w http.ResponseWriter, r *http.Request, status int) {
	d, state, err := p.channels().Load(r.Context())
	channel := model.TelegramChannel{ProxyMode: "none", Status: "not_configured"}
	if errors.Is(err, store.ErrTelegramNotConfigured) {
		writeJSON(w, status, channel)
		return
	}
	if err != nil {
		writeError(w, 503, "telegram_configuration_unavailable")
		return
	}
	channel.BotTokenSet = true
	channel.ChatID = d.ChatID
	channel.ProxyURL = d.ProxyURL
	channel.ProxyAuthSet = d.ProxyUsername != ""
	channel.LastSuccessAt = state.LastSuccessAt
	channel.LastError = state.LastError
	if d.ProxyMode != "" {
		channel.ProxyMode = d.ProxyMode
	}
	channel.Status = "not_tested"
	if state.LastError != nil {
		channel.Status = "failing"
	} else if state.LastSuccessAt != nil {
		channel.Status = "ok"
	}
	writeJSON(w, status, channel)
}

func (p *productionPanel) updateTelegram(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BotToken      *string `json:"bot_token"`
		ChatID        *string `json:"chat_id"`
		ProxyMode     *string `json:"proxy_mode"`
		ProxyURL      *string `json:"proxy_url"`
		ProxyUsername *string `json:"proxy_username"`
		ProxyPassword *string `json:"proxy_password"`
	}
	if !readBody(w, r, &in) {
		return
	}
	for _, field := range []struct {
		name    string
		missing bool
	}{{"chat_id", in.ChatID == nil}, {"proxy_mode", in.ProxyMode == nil}, {"proxy_url", in.ProxyURL == nil}} {
		if field.missing {
			fieldError(w, field.name, "required")
			return
		}
	}
	manager := p.channels()
	if manager.Box == nil {
		writeError(w, 503, "telegram_configuration_unavailable")
		return
	}
	_, err := manager.Update(r.Context(), notify.Update{BotToken: in.BotToken, ChatID: *in.ChatID, ProxyMode: *in.ProxyMode, ProxyURL: *in.ProxyURL, ProxyUsername: in.ProxyUsername, ProxyPassword: in.ProxyPassword}, p.now())
	for _, secret := range []*string{in.BotToken, in.ProxyUsername, in.ProxyPassword} {
		if secret != nil {
			*secret = ""
		}
	}
	var invalid *notify.FieldError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": invalid.Fields}})
	case errors.Is(err, store.ErrChannelChanged):
		writeError(w, 409, "telegram_configuration_changed")
	case errors.Is(err, notify.ErrConfiguration):
		writeError(w, 503, "telegram_configuration_unavailable")
	case err != nil:
		businessError(w, err)
	default:
		p.writeTelegramChannel(w, r, 200)
	}
}

// Test text is the frontend-owned template in docs/design/telegram-templates.md.
const telegramTestTitle = "⚪ 测试消息"
const telegramTestBody = "如果你在 Telegram 里看到这条消息，说明通知已经配置好了。"

func (p *productionPanel) testTelegram(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 {
		var body struct{}
		if !readBody(w, r, &body) {
			return
		}
	}
	if p.engine == nil || p.engine.Box == nil {
		writeError(w, 503, "telegram_configuration_unavailable")
		return
	}
	j, err := p.s.EnqueueTelegramTest(r.Context(), telegramTestTitle, telegramTestBody, p.now())
	if errors.Is(err, store.ErrTelegramNotConfigured) {
		writeError(w, 409, "telegram_not_configured")
		return
	}
	if err != nil {
		businessError(w, err)
		return
	}
	writeJSON(w, 202, map[string]string{"job_id": j.ID})
}

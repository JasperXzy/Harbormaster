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
	if r.Method != "GET" {
		return false
	}
	switch path {
	case "/notify/telegram":
		manager := notify.Channels{Store: p.s}
		if p.engine != nil {
			manager.Box = p.engine.Box
		}
		d, state, err := manager.Load(r.Context())
		channel := model.TelegramChannel{ProxyMode: "none", Status: "not_configured"}
		if errors.Is(err, store.ErrTelegramNotConfigured) {
			writeJSON(w, 200, channel)
			return true
		}
		if err != nil {
			writeError(w, 503, "telegram_configuration_unavailable")
			return true
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
		writeJSON(w, 200, channel)
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

package api

import (
	"errors"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) telegramControl(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/notify/telegram/control" {
		return false
	}
	switch r.Method {
	case "GET":
		cfg, err := p.s.TelegramControl(r.Context())
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, cfg)
		}
		return true
	case "PUT":
		var in model.TelegramControlSettings
		if !readBody(w, r, &in) {
			return true
		}
		saved, err := p.s.SaveTelegramControl(r.Context(), in, p.now().UTC())
		var validation *store.SettingsValidationError
		switch {
		case err == nil:
			writeJSON(w, 200, saved)
		case errors.As(err, &validation):
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": validation.Fields}})
		case errors.Is(err, store.ErrChannelChanged):
			writeError(w, 409, "revision_conflict")
		default:
			businessError(w, err)
		}
		return true
	default:
		writeError(w, 405, "method_not_allowed")
		return true
	}
}

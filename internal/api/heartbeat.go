package api

import (
	"errors"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/heartbeat"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) heartbeatSettings(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/settings/heartbeat" && path != "/settings/heartbeat/test" {
		return false
	}
	if path == "/settings/heartbeat" && r.Method == "GET" {
		_, state, err := p.s.HeartbeatSettings(r.Context())
		if err != nil {
			heartbeatAPIError(w, err)
		} else {
			writeJSON(w, 200, state)
		}
		return true
	}
	if (path == "/settings/heartbeat" && r.Method != "PUT") || (path == "/settings/heartbeat/test" && r.Method != "POST") {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	if p.engine == nil || p.engine.Box == nil {
		writeError(w, 503, "heartbeat_unavailable")
		return true
	}
	if path == "/settings/heartbeat/test" {
		var body struct{}
		if !readBody(w, r, &body) {
			return true
		}
		j, err := p.s.EnqueueHeartbeatTest(r.Context(), p.now())
		if err != nil {
			heartbeatAPIError(w, err)
		} else {
			writeJSON(w, 202, map[string]string{"job_id": j.ID})
		}
		return true
	}
	var in struct {
		Revision *int64  `json:"revision"`
		Enabled  *bool   `json:"enabled"`
		Interval *int    `json:"interval_seconds"`
		URL      *string `json:"url"`
	}
	if !readBody(w, r, &in) {
		return true
	}
	if in.Revision == nil || *in.Revision < 0 {
		fieldError(w, "revision", "out_of_range")
		return true
	}
	if in.Enabled == nil {
		fieldError(w, "enabled", "required")
		return true
	}
	if in.Interval == nil || *in.Interval < 15 || *in.Interval > 86400 {
		fieldError(w, "interval_seconds", "out_of_range")
		return true
	}
	var replacement *[]byte
	if in.URL != nil {
		cipher := []byte{}
		if *in.URL != "" {
			if !heartbeat.ValidURL(*in.URL) {
				fieldError(w, "url", "invalid_format")
				return true
			}
			raw := []byte(*in.URL)
			var err error
			cipher, err = p.engine.Box.Encrypt(raw, model.HeartbeatCredentialOwner)
			clear(raw)
			if err != nil {
				writeError(w, 503, "internal_error")
				return true
			}
		}
		replacement = &cipher
	}
	state, err := p.s.SaveHeartbeat(r.Context(), *in.Revision, *in.Enabled, *in.Interval, replacement, p.now())
	if err != nil {
		heartbeatAPIError(w, err)
	} else {
		writeJSON(w, 200, state)
	}
	return true
}
func heartbeatAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrHeartbeatChanged):
		writeError(w, 409, "heartbeat_configuration_changed")
	case errors.Is(err, store.ErrHeartbeatNotConfigured):
		writeError(w, 409, "heartbeat_not_configured")
	case errors.Is(err, store.ErrHeartbeatInvalid):
		writeError(w, 422, "validation_failed")
	default:
		businessError(w, err)
	}
}

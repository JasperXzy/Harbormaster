package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) integrationRefresh(w http.ResponseWriter, r *http.Request, keyID string) {
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed")
		return
	}
	if p.engine == nil || p.engine.Box == nil || p.engine.Scanner == nil {
		writeError(w, 503, "collector_unavailable")
		return
	}
	var in map[string]json.RawMessage
	if !readBody(w, r, &in) {
		return
	}
	if in == nil {
		writeError(w, 422, "validation_failed")
		return
	}
	accountID := ""
	for key, raw := range in {
		if key != "account_id" {
			fieldError(w, key, "unknown_field")
			return
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &accountID) != nil || len(accountID) > 128 {
			fieldError(w, "account_id", "invalid_format")
			return
		}
	}
	j, err := p.s.EnqueueAPIKeyRefresh(r.Context(), keyID, accountID, p.now().UTC())
	switch {
	case err == nil:
		writeJSON(w, 202, map[string]string{"job_id": j.ID})
	case errors.Is(err, store.ErrRefreshScopeChanged):
		writeError(w, 409, "refresh_scope_changed")
	case errors.Is(err, store.ErrRefreshParameters):
		fieldError(w, "account_id", "invalid_format")
	default:
		apiKeyError(w, err)
	}
}

package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) apiKeys(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/api-keys" && !strings.HasPrefix(path, "/api-keys/") {
		return false
	}
	if path != "/api-keys" {
		id := strings.TrimPrefix(path, "/api-keys/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, 404, "not_found")
			return true
		}
		if r.Method != "DELETE" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		if err := p.s.RevokeAPIKey(r.Context(), id, p.now()); err != nil {
			businessError(w, err)
		} else {
			w.WriteHeader(204)
		}
		return true
	}
	switch r.Method {
	case "GET":
		limit, before := 50, int64(0)
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 200 {
				fieldError(w, "limit", "out_of_range")
				return true
			}
			limit = n
		}
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			if len(raw) > 64 {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			if err != nil {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
			before, err = strconv.ParseInt(string(decoded), 10, 64)
			if err != nil || before <= 0 {
				fieldError(w, "cursor", "invalid_format")
				return true
			}
		}
		items, err := p.s.APIKeys(r.Context(), before, limit+1)
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
	case "POST":
		var in model.APIKeyCreate
		if !readBody(w, r, &in) {
			return true
		}
		if fields := in.Validate(); len(fields) > 0 {
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": fields}})
			return true
		}
		key, token, err := p.s.CreateAPIKey(r.Context(), in, p.now())
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 201, map[string]any{"api_key": key, "token": token})
		}
	default:
		writeError(w, 405, "method_not_allowed")
	}
	return true
}

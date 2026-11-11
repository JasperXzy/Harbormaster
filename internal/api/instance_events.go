package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
)

func (p *productionPanel) instanceEvents(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/instance-events" {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	query := r.URL.Query()
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			fieldError(w, "limit", "out_of_range")
			return true
		}
		limit = n
	}
	var before int64
	if raw := query.Get("cursor"); raw != "" {
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
	items, err := p.s.InstanceEvents(r.Context(), query.Get("account_id"), query.Get("instance_id"), before, limit+1)
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
	return true
}

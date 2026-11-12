package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) systemEvents(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/system-events" {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	q := r.URL.Query()
	filter := model.SystemEventFilter{AccountID: q.Get("account_id"), InstanceID: q.Get("instance_id"), RegionID: q.Get("region_id"), Source: q.Get("source"), Category: q.Get("category")}
	if !filter.Validate() {
		writeError(w, 422, "validation_failed")
		return true
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			fieldError(w, "limit", "out_of_range")
			return true
		}
		limit = n
	}
	var before int64
	if raw := q.Get("cursor"); raw != "" {
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
	items, err := p.s.SystemEvents(r.Context(), filter, before, limit+1)
	if err != nil {
		businessError(w, err)
		return true
	}
	interval := time.Minute
	if p.engine != nil {
		interval = p.engine.SystemEventPollInterval()
	}
	sources, err := p.s.SystemEventSources(r.Context(), filter, p.now().UTC(), 2*interval)
	if err != nil {
		businessError(w, err)
		return true
	}
	var next any
	if len(items) > limit {
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(items[limit-1].Sequence, 10)))
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "sources": sources, "next_cursor": next})
	return true
}

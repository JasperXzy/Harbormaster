package api

import (
	"net/http"
	"strconv"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) idleResources(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/idle-resources" {
		return false
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			fieldError(w, "limit", "out_of_range")
			return true
		}
		limit = n
	}
	f := model.IdleFilter{AccountID: q.Get("account_id"), RegionID: q.Get("region_id"), Kind: q.Get("kind"), Limit: limit + 1}
	if !f.Valid() {
		writeError(w, 422, "validation_failed")
		return true
	}
	if raw := q.Get("cursor"); raw != "" {
		after, ok := model.DecodeIdleCursor(raw, f)
		if !ok {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
		f.After = after
	}
	items, sources, err := p.s.IdleResources(r.Context(), f, p.now().UTC())
	if err != nil {
		businessError(w, err)
		return true
	}
	var next any
	if len(items) > limit {
		next = model.EncodeIdleCursor(items[limit-1].ID, f)
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "sources": sources, "next_cursor": next})
	return true
}

package api

import (
	"net/http"
	"strconv"
)

func (p *productionPanel) trafficReconciliations(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/traffic/reconciliations" {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	q := r.URL.Query()
	limit := 50
	var before int64
	var err error
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err == nil {
		if raw := q.Get("before"); raw != "" {
			before, err = strconv.ParseInt(raw, 10, 64)
			if before < 1 {
				fieldError(w, "before", "invalid_format")
				return true
			}
		}
	}
	scope := q.Get("scope")
	if err != nil || limit < 1 || limit > 200 || scope != "" && scope != "mainland" && scope != "overseas" {
		fieldError(w, "query", "invalid_format")
		return true
	}
	items, more, err := p.s.Reconciliations(r.Context(), q.Get("account_id"), scope, before, limit)
	if err != nil {
		businessError(w, err)
	} else {
		var next any
		if more {
			next = items[len(items)-1].ID
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_before": next})
	}
	return true
}

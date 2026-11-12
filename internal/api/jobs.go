package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) jobHistory(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/jobs" && !strings.HasPrefix(path, "/jobs/") {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	if path != "/jobs" {
		job, err := p.s.Job(r.Context(), strings.TrimPrefix(path, "/jobs/"))
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, job)
		}
		return true
	}
	q := r.URL.Query()
	filter := model.JobFilter{AccountID: q.Get("account_id"), InstanceID: q.Get("instance_id"), Type: q.Get("type"), Status: q.Get("status"), Trigger: q.Get("trigger")}
	if fields := filter.Validate(); len(fields) != 0 {
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": fields}})
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
	items, err := p.s.Jobs(r.Context(), filter, before, limit+1)
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

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) auditLog(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/audit-log" {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	q := r.URL.Query()
	f := model.AuditFilter{Actor: q.Get("actor"), Action: q.Get("action"), TargetType: q.Get("target_type"), TargetID: q.Get("target_id"), AccountID: q.Get("account_id"), Limit: 50}
	cursor := probeCursor{Kind: "audit"}
	if q.Has("from") || q.Has("to") {
		from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
		if e1 != nil || e2 != nil || to.After(p.now()) {
			writeError(w, 422, "validation_failed")
			return true
		}
		f.From, f.To = &from, &to
		cursor.From, cursor.To = from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)
	}
	if !f.Validate() {
		writeError(w, 422, "validation_failed")
		return true
	}
	filters, _ := json.Marshal([]string{f.Actor, f.Action, f.TargetType, f.TargetID, f.AccountID})
	cursor.ID = string(filters)
	limit, after, ok := probePage(w, r, cursor)
	if !ok {
		return true
	}
	f.Limit = limit + 1
	if after != "" {
		var err error
		f.Before, err = strconv.ParseInt(after, 10, 64)
		if err != nil || f.Before < 1 {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
	}
	items, err := p.s.AuditLog(r.Context(), f)
	if err != nil {
		businessError(w, err)
	} else {
		probeList(w, items, limit, cursor, func(v model.AuditEntry) string { return v.ID })
	}
	return true
}

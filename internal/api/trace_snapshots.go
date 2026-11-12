package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) networkTraceSnapshots(w http.ResponseWriter, r *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/network/"), "/")
	if !strings.HasPrefix(path, "/network/") || len(parts) != 3 || parts[0] != "plans" || (parts[2] != "trace-schedule" && parts[2] != "trace-snapshots") {
		return false
	}
	id := parts[1]
	if id == "" || len(id) > 128 {
		writeError(w, 404, "not_found")
		return true
	}
	if parts[2] == "trace-schedule" {
		switch r.Method {
		case "GET":
			out, err := p.s.TraceSchedule(r.Context(), id, p.timezone)
			if err != nil {
				traceAPIError(w, err)
			} else {
				writeJSON(w, 200, out)
			}
		case "PUT":
			in := model.DefaultTraceSchedule()
			keys := []string{"revision", "enabled", "notify_changes", "time_of_day", "max_hops", "count", "timeout_ms"}
			if !readProbeInput(w, r, &in, keys, 3) {
				return true
			}
			out, err := p.s.SaveTraceSchedule(r.Context(), id, in, p.timezone, p.now())
			if err != nil {
				traceAPIError(w, err)
			} else {
				writeJSON(w, 200, out)
			}
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	from, e1 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("to"))
	if e1 != nil || e2 != nil || !from.Before(to) || to.Sub(from) > 31*24*time.Hour || to.After(p.now()) {
		writeError(w, 422, "validation_failed")
		return true
	}
	cursor := probeCursor{Kind: "trace_snapshots", ID: id, From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}
	limit, after, ok := probePage(w, r, cursor)
	if !ok {
		return true
	}
	var before int64
	if after != "" {
		var err error
		before, err = strconv.ParseInt(after, 10, 64)
		if err != nil || before < 1 {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
	}
	items, err := p.s.TraceSnapshots(r.Context(), id, from, to, before, limit+1)
	if err != nil {
		traceAPIError(w, err)
	} else {
		probeList(w, items, limit, cursor, func(v model.TraceSnapshot) string { return v.ID })
	}
	return true
}

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func traceAPIError(w http.ResponseWriter, err error) {
	var precondition *store.TracePreconditionError
	switch {
	case errors.As(err, &precondition):
		writeError(w, 409, precondition.Code)
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "request_conflict")
	case errors.Is(err, store.ErrProbeBusy):
		writeError(w, 409, "probe_queue_full")
	default:
		probeAPIError(w, err)
	}
}
func (p *productionPanel) networkTraces(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/network/traces" && !strings.HasPrefix(path, "/network/traces/") {
		return false
	}
	if path == "/network/traces" {
		switch r.Method {
		case "POST":
			in := model.DefaultTraceSubmission()
			keys := []string{"request_key", "point_kind", "instance_id", "target_id", "target_revision", "host", "family", "method", "port", "max_hops", "count", "timeout_ms"}
			if !readProbeInput(w, r, &in, keys, 9) {
				return true
			}
			out, err := p.s.SubmitTrace(r.Context(), in, p.now())
			if err != nil {
				traceAPIError(w, err)
			} else {
				writeJSON(w, 202, out)
			}
		case "GET":
			p.traceHistory(w, r)
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/network/traces/"), "/")
	if len(parts) > 2 || parts[0] == "" || len(parts) == 2 && parts[1] != "export" {
		writeError(w, 404, "not_found")
		return true
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	detail, err := p.s.Trace(r.Context(), parts[0])
	if err != nil {
		traceAPIError(w, err)
		return true
	}
	if len(parts) == 1 {
		writeJSON(w, 200, detail)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="trace-`+detail.ID+`.txt"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(model.TraceText(detail)))
	}
	return true
}
func (p *productionPanel) traceHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	f := model.TraceFilter{From: from, To: to, PointID: q.Get("point_id"), TargetID: q.Get("target_id"), Limit: 50}
	if e1 != nil || e2 != nil || !f.Validate() || to.After(p.now()) {
		writeError(w, 422, "validation_failed")
		return
	}
	filters, _ := json.Marshal([]string{f.PointID, f.TargetID})
	cursor := probeCursor{Kind: "traces", ID: string(filters), From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}
	limit, after, ok := probePage(w, r, cursor)
	if !ok {
		return
	}
	f.Limit = limit + 1
	if after != "" {
		var err error
		f.Before, err = strconv.ParseInt(after, 10, 64)
		if err != nil || f.Before < 1 {
			fieldError(w, "cursor", "invalid_format")
			return
		}
	}
	items, err := p.s.Traces(r.Context(), f)
	if err != nil {
		traceAPIError(w, err)
	} else {
		probeList(w, items, limit, cursor, func(v model.TraceRun) string { return v.ID })
	}
}

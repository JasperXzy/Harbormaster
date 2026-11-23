package api

import (
	"net/http"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (p *productionPanel) networkDuplexTraces(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/network/duplex-traces" && !strings.HasPrefix(path, "/network/duplex-traces/") {
		return false
	}
	if path == "/network/duplex-traces" {
		if r.Method != "POST" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		in := model.DefaultDuplexTraceSubmission()
		keys := []string{"request_key", "from_instance_id", "to_instance_id", "family", "method", "forward_port", "reverse_port", "max_hops", "count", "timeout_ms"}
		if !readProbeInput(w, r, &in, keys, 7) {
			return true
		}
		out, err := p.s.SubmitDuplexTrace(r.Context(), in, p.now())
		if err != nil {
			traceAPIError(w, err)
		} else {
			writeJSON(w, 202, out)
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/network/duplex-traces/"), "/")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 128 || len(parts) == 2 && parts[1] != "export" {
		writeError(w, 404, "not_found")
		return true
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	d, err := p.s.DuplexTrace(r.Context(), parts[0])
	if err != nil {
		traceAPIError(w, err)
		return true
	}
	if len(parts) == 1 {
		writeJSON(w, 200, d)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="duplex-trace-`+d.ID+`.txt"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(model.DuplexTraceText(d)))
	}
	return true
}

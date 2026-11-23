package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) agentHourlyHistory(w http.ResponseWriter, r *http.Request, path string) bool {
	const suffix = "/agent-hourly-history"
	if !strings.HasPrefix(path, "/instances/") || !strings.HasSuffix(path, suffix) {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), suffix)
	if id == "" || strings.Contains(id, "/") {
		writeError(w, 404, "not_found")
		return true
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	if e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 400*24*time.Hour || from.After(p.now().UTC().Add(time.Minute)) {
		writeError(w, 422, "validation_failed")
		return true
	}
	items, err := p.s.AgentHourlyHistory(r.Context(), id, from.UTC(), to.UTC())
	if err != nil {
		businessError(w, err)
		return true
	}
	writeJSON(w, 200, map[string]any{"items": items, "from": from.UTC(), "to": to.UTC()})
	return true
}

func (p *productionPanel) trafficPricing(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/traffic/pricing" {
		return false
	}
	switch r.Method {
	case "GET":
		pricing, err := p.s.TrafficPricing(r.Context())
		if err != nil {
			businessError(w, err)
			return true
		}
		writeJSON(w, 200, pricing)
		return true
	case "PUT":
		var in model.TrafficPricing
		if !readBody(w, r, &in) {
			return true
		}
		saved, err := p.s.SaveTrafficPricing(r.Context(), in, p.now().UTC())
		var validation *store.SettingsValidationError
		switch {
		case err == nil:
			writeJSON(w, 200, saved)
		case errors.As(err, &validation):
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": validation.Fields}})
		case errors.Is(err, store.ErrEstimationRevision):
			writeError(w, 409, "revision_conflict")
		default:
			businessError(w, err)
		}
		return true
	default:
		writeError(w, 405, "method_not_allowed")
		return true
	}
}

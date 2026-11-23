package api

import (
	"errors"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
	"net/http"
)

func (p *productionPanel) estimationSettings(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/traffic/estimation-settings" {
		return false
	}
	switch r.Method {
	case "GET":
		v, err := p.s.EstimationSettings(r.Context())
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, v)
		}
	case "PUT":
		var in struct {
			Revision     *int64   `json:"revision"`
			LagSeconds   *int     `json:"lag_window_seconds"`
			BandwidthIDs []string `json:"bandwidth_mode_instance_ids"`
		}
		if !readBody(w, r, &in) {
			return true
		}
		if in.Revision == nil || in.LagSeconds == nil || in.BandwidthIDs == nil {
			fieldError(w, "estimation", "required")
			return true
		}
		v, err := p.s.SaveEstimationSettings(r.Context(), model.EstimationSettings{Revision: *in.Revision, LagSeconds: *in.LagSeconds, BandwidthIDs: in.BandwidthIDs})
		if errors.Is(err, store.ErrEstimationRevision) {
			writeError(w, 409, "revision_conflict")
		} else if errors.Is(err, store.ErrEstimationSettings) {
			fieldError(w, "estimation", "invalid_parameters")
		} else if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, v)
		}
	default:
		writeError(w, 405, "method_not_allowed")
	}
	return true
}

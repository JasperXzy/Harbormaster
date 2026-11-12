package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) quietHours(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/notify/quiet-hours" {
		return false
	}
	if r.Method == "GET" {
		state, err := p.s.QuietHours(r.Context(), p.now())
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, state)
		}
		return true
	}
	if r.Method != "PUT" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	var in struct {
		Revision    *int64          `json:"revision"`
		Enabled     *bool           `json:"enabled"`
		Start       *string         `json:"start"`
		End         *string         `json:"end"`
		Maintenance json.RawMessage `json:"maintenance"`
	}
	if !readBody(w, r, &in) {
		return true
	}
	for _, field := range []struct {
		name    string
		missing bool
	}{{"revision", in.Revision == nil}, {"enabled", in.Enabled == nil}, {"start", in.Start == nil}, {"end", in.End == nil}, {"maintenance", len(in.Maintenance) == 0}} {
		if field.missing {
			fieldError(w, field.name, "required")
			return true
		}
	}
	var maintenance *model.MaintenanceWindow
	decoder := json.NewDecoder(bytes.NewReader(in.Maintenance))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&maintenance) != nil {
		fieldError(w, "maintenance", "invalid_format")
		return true
	}
	state, err := p.s.SaveQuietHours(r.Context(), model.QuietHours{Revision: *in.Revision, Enabled: *in.Enabled, Start: *in.Start, End: *in.End, Maintenance: maintenance}, p.now())
	var invalid *store.AlertValidationError
	if errors.As(err, &invalid) {
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": invalid.Fields}})
	} else if errors.Is(err, store.ErrQuietChanged) {
		writeError(w, 409, "quiet_hours_changed")
	} else if err != nil {
		businessError(w, err)
	} else {
		writeJSON(w, 200, state)
	}
	return true
}

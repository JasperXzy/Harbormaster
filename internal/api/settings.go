package api

import (
	"errors"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) settingsPayload(r *http.Request, zone string, ret model.RetentionSettings) (map[string]any, error) {
	eventSeconds := 60
	scanSeconds := 300
	cdtSeconds := 300
	if p.engine != nil {
		scanSeconds = int(p.engine.Interval.Seconds())
		cdtSeconds = int(p.engine.TrafficInterval().Seconds())
		eventSeconds = int(p.engine.SystemEventPollInterval().Seconds())
	}
	offline, err := p.s.AgentOfflineAfter(r.Context())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"business_timezone": zone,
		"retention":         ret,
		"collection": map[string]int{
			"cdt_poll_interval_seconds":          cdtSeconds,
			"agent_report_interval_seconds":      10,
			"agent_offline_after_seconds":        int(offline.Seconds()),
			"resource_scan_interval_seconds":     scanSeconds,
			"system_event_poll_interval_seconds": eventSeconds,
		},
	}, nil
}

func (p *productionPanel) generalSettings(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/settings" {
		return false
	}
	switch r.Method {
	case "GET":
		zone, ret, err := p.s.Settings(r.Context(), p.timezone, p.probeRetentionDays)
		if err != nil {
			writeError(w, 503, "internal_error")
			return true
		}
		out, err := p.settingsPayload(r, zone, ret)
		if err != nil {
			businessError(w, err)
			return true
		}
		writeJSON(w, 200, out)
		return true
	case "PATCH":
		var in model.SettingsUpdate
		if !readBody(w, r, &in) {
			return true
		}
		zone, ret, err := p.s.UpdateSettings(r.Context(), p.timezone, p.probeRetentionDays, in, p.now().UTC())
		var validation *store.SettingsValidationError
		if errors.As(err, &validation) {
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": validation.Fields}})
			return true
		}
		if err != nil {
			businessError(w, err)
			return true
		}
		out, err := p.settingsPayload(r, zone, ret)
		if err != nil {
			businessError(w, err)
			return true
		}
		writeJSON(w, 200, out)
		return true
	default:
		writeError(w, 405, "method_not_allowed")
		return true
	}
}

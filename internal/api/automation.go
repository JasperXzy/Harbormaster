package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func automationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrEstimationRevision):
		writeError(w, 409, "revision_conflict")
	case errors.Is(err, store.ErrAutomationConfig):
		fieldError(w, "automation", "invalid_parameters")
	default:
		businessError(w, err)
	}
}

func (p *productionPanel) automation(w http.ResponseWriter, r *http.Request, path string) bool {
	if strings.HasPrefix(path, "/instances/") && strings.HasSuffix(path, "/intent-confirmation") {
		parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			return false
		}
		if r.Method != "POST" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		var in struct {
			Decision string `json:"decision"`
		}
		if !readBody(w, r, &in) {
			return true
		}
		desired, jobID, err := p.s.ConfirmInstanceIntent(r.Context(), parts[0], in.Decision, p.powerEnabled, p.now().UTC())
		if err != nil {
			automationError(w, err)
			return true
		}
		writeJSON(w, 200, map[string]any{"instance_id": parts[0], "desired_state": desired, "job_id": jobID})
		return true
	}

	if !strings.HasPrefix(path, "/automation/") {
		return false
	}
	switch {
	case path == "/automation/schedules" && r.Method == "GET":
		items, err := p.s.PowerSchedules(r.Context())
		if err != nil {
			automationError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": items})
		}
	case path == "/automation/schedules" && r.Method == "POST" || strings.HasPrefix(path, "/automation/schedules/") && r.Method == "PUT":
		id := ""
		if path != "/automation/schedules" {
			id = strings.TrimPrefix(path, "/automation/schedules/")
			if id == "" || strings.Contains(id, "/") {
				writeError(w, 404, "not_found")
				return true
			}
		}
		var in model.PowerScheduleInput
		if !readBody(w, r, &in) {
			return true
		}
		if in.ConfirmedSelfID != "" && (!p.protectionPolicy.SelfKnown || p.protectionPolicy.SelfInstanceID != in.ConfirmedSelfID) {
			fieldError(w, "confirmed_self_instance_id", "not_current_server")
			return true
		}
		saved, err := p.s.SavePowerSchedule(r.Context(), id, in, p.now().UTC())
		if err != nil {
			automationError(w, err)
		} else {
			status := 200
			if id == "" {
				status = 201
			}
			writeJSON(w, status, saved)
		}
	case strings.HasPrefix(path, "/automation/schedules/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/automation/schedules/")
		rev, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || rev < 1 {
			fieldError(w, "revision", "invalid_format")
			return true
		}
		if err = p.s.DeletePowerSchedule(r.Context(), id, rev, p.now().UTC()); err != nil {
			automationError(w, err)
		} else {
			w.WriteHeader(204)
		}
	case path == "/automation/keepalive" && r.Method == "GET":
		cfg, err := p.s.KeepaliveSettings(r.Context())
		if err != nil {
			automationError(w, err)
		} else {
			writeJSON(w, 200, cfg)
		}
	case path == "/automation/keepalive" && r.Method == "PUT":
		var in model.KeepaliveSettings
		if !readBody(w, r, &in) {
			return true
		}
		saved, err := p.s.SaveKeepaliveSettings(r.Context(), in, p.now().UTC())
		if err != nil {
			automationError(w, err)
		} else {
			writeJSON(w, 200, saved)
		}
	case path == "/automation/monthly-reset" && r.Method == "GET":
		cfg, err := p.s.MonthlyResetSettings(r.Context())
		if err != nil {
			automationError(w, err)
		} else {
			writeJSON(w, 200, cfg)
		}
	case path == "/automation/monthly-reset" && r.Method == "PUT":
		var in model.MonthlyResetSettings
		if !readBody(w, r, &in) {
			return true
		}
		saved, err := p.s.SaveMonthlyResetSettings(r.Context(), in, p.now().UTC())
		if err != nil {
			automationError(w, err)
		} else {
			writeJSON(w, 200, saved)
		}
	case path == "/automation/failovers" && r.Method == "GET":
		items, err := p.s.FailoverRules(r.Context())
		if err != nil {
			automationError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": items})
		}
	case path == "/automation/failovers" && r.Method == "POST" || strings.HasPrefix(path, "/automation/failovers/") && r.Method == "PUT":
		id := ""
		if path != "/automation/failovers" {
			id = strings.TrimPrefix(path, "/automation/failovers/")
			if id == "" || strings.Contains(id, "/") {
				writeError(w, 404, "not_found")
				return true
			}
		}
		var in model.FailoverRuleInput
		if !readBody(w, r, &in) {
			return true
		}
		if fields := in.Validate(); len(fields) > 0 {
			writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": fields}})
			return true
		}
		saved, err := p.s.SaveFailoverRule(r.Context(), id, in, p.now().UTC())
		if err != nil {
			automationError(w, err)
		} else {
			status := 200
			if id == "" {
				status = 201
			}
			writeJSON(w, status, saved)
		}
	case strings.HasPrefix(path, "/automation/failovers/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/automation/failovers/")
		rev, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || rev < 1 {
			fieldError(w, "revision", "invalid_format")
			return true
		}
		if err = p.s.DeleteFailoverRule(r.Context(), id, rev, p.now().UTC()); err != nil {
			automationError(w, err)
		} else {
			w.WriteHeader(204)
		}
	default:
		writeError(w, 405, "method_not_allowed")
	}
	return true
}

func (p *productionPanel) overlayAutomationFields(ctx context.Context, items []map[string]any) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["id"].(string))
	}
	pending, err := p.s.PendingIntentConfirmations(ctx, ids)
	if err != nil {
		return err
	}
	for _, item := range items {
		id, _ := item["id"].(string)
		kind, _ := item["kind"].(string)
		charge, _ := item["charge_type"].(string)
		if kind != "ecs" {
			item["supports_stop_charging"] = false
		} else if charge == "pay_as_you_go" || charge == "spot" {
			item["supports_stop_charging"] = true
		} else if charge == "subscription" {
			item["supports_stop_charging"] = false
		} else {
			item["supports_stop_charging"] = nil
		}
		if c, ok := pending[id]; ok {
			item["pending_intent_confirmation"] = c
		} else {
			item["pending_intent_confirmation"] = nil
		}
	}
	return nil
}

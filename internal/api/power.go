package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) instancePower(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasPrefix(path, "/instances/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "power" {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	if !p.powerEnabled {
		writeError(w, 503, "instance_power_disabled")
		return true
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !model.ValidPowerEventKey(keys[0]) {
		fieldError(w, "idempotency_key", "invalid_format")
		return true
	}
	var in struct {
		Action   string          `json:"action"`
		StopMode json.RawMessage `json:"stop_mode"`
	}
	if !readBody(w, r, &in) {
		return true
	}
	if in.Action != "start" && in.Action != "stop" && in.Action != "reboot" {
		fieldError(w, "action", "invalid_format")
		return true
	}
	mode := ""
	if len(in.StopMode) > 0 {
		if in.Action != "stop" || json.Unmarshal(in.StopMode, &mode) != nil || mode != "keep_charging" && mode != "stop_charging" {
			fieldError(w, "stop_mode", "invalid_format")
			return true
		}
	}
	j, err := p.s.EnqueuePowerJob(r.Context(), parts[0], in.Action, mode, keys[0], p.now().UTC())
	switch {
	case err == nil:
		writeJSON(w, 202, map[string]string{"job_id": j.ID})
	case errors.Is(err, store.ErrUnauthorized), errors.Is(err, store.ErrAPIKeyScope):
		apiKeyError(w, err)
	case errors.Is(err, store.ErrPowerParameters):
		fieldError(w, "stop_mode", "invalid_for_instance")
	case errors.Is(err, store.ErrPowerBusy):
		writeError(w, 409, "instance_power_busy")
	case errors.Is(err, store.ErrPowerEventConflict):
		writeError(w, 409, "idempotency_key_conflict")
	case errors.Is(err, store.ErrAccountChanged):
		writeError(w, 409, "account_changed")
	default:
		businessError(w, err)
	}
	return true
}

func (p *productionPanel) overlayIntent(ctx context.Context, items []map[string]any) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item["id"].(string))
	}
	intents, err := p.s.InstanceIntents(ctx, ids)
	if err != nil {
		return err
	}
	for _, item := range items {
		item["desired_state"], item["desired_state_updated_at"] = nil, nil
		if intent, ok := intents[item["id"].(string)]; ok {
			item["desired_state"], item["desired_state_updated_at"] = intent.DesiredState, intent.UpdatedAt
		}
	}
	return nil
}

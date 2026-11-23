package api

import (
	"encoding/json"
	"errors"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
	"net/http"
	"strconv"
	"strings"
)

func readProtectionInput(w http.ResponseWriter, r *http.Request, out any, keys ...string) bool {
	var raw map[string]json.RawMessage
	if !readBody(w, r, &raw) {
		return false
	}
	if len(raw) != len(keys) {
		fieldError(w, "protection", "required_fields")
		return false
	}
	for _, key := range keys {
		v, ok := raw[key]
		if !ok || string(v) == "null" {
			fieldError(w, key, "required")
			return false
		}
	}
	data, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(data, out) != nil {
		fieldError(w, "protection", "invalid_format")
		return false
	}
	return true
}
func readProtectionRulePayload(w http.ResponseWriter, r *http.Request, out *model.ProtectionRuleInput) bool {
	required := []string{"revision", "name", "enabled", "account_id", "scope", "source_instance_id", "threshold_kind", "threshold", "action", "target_instance_ids", "stop_mode", "reserve_seconds", "dry_run", "confirmed_self_instance_id"}
	var raw map[string]json.RawMessage
	if !readBody(w, r, &raw) {
		return false
	}
	allowed := len(required)
	if v, has := raw["allow_unverified_execution"]; has {
		if string(v) == "null" {
			fieldError(w, "allow_unverified_execution", "required")
			return false
		}
		allowed++
	}
	if len(raw) != allowed {
		fieldError(w, "protection", "required_fields")
		return false
	}
	for _, key := range required {
		v, ok := raw[key]
		if !ok || string(v) == "null" {
			fieldError(w, key, "required")
			return false
		}
	}
	data, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(data, out) != nil {
		fieldError(w, "protection", "invalid_format")
		return false
	}
	return true
}
func protectionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrEstimationRevision) {
		writeError(w, 409, "revision_conflict")
	} else if errors.Is(err, store.ErrProtectionRule) {
		fieldError(w, "protection", "invalid_parameters")
	} else {
		businessError(w, err)
	}
}
func (p *productionPanel) quotaProtection(w http.ResponseWriter, r *http.Request, path string) bool {
	if strings.HasPrefix(path, "/instances/") && strings.HasSuffix(path, "/protection-exemption") {
		parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			return false
		}
		switch r.Method {
		case "PUT":
			var in struct {
				Seconds int `json:"duration_seconds"`
			}
			if !readProtectionInput(w, r, &in, "duration_seconds") {
				return true
			}
			until, err := p.s.SetQuotaExemption(r.Context(), parts[0], in.Seconds, p.now())
			if err != nil {
				protectionError(w, err)
			} else {
				writeJSON(w, 200, map[string]any{"instance_id": parts[0], "expires_at": until})
			}
		case "DELETE":
			if err := p.s.ClearQuotaExemption(r.Context(), parts[0], p.now()); err != nil {
				protectionError(w, err)
			} else {
				w.WriteHeader(204)
			}
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	}
	if !strings.HasPrefix(path, "/protection/") {
		return false
	}
	switch {
	case path == "/protection/status" && r.Method == "GET":
		var self any
		if p.protectionPolicy.SelfInstanceID != "" {
			self = p.protectionPolicy.SelfInstanceID
		}
		writeJSON(w, 200, map[string]any{"power_enabled": p.powerEnabled, "release_enabled": p.releaseEnabled, "self_identity_known": p.protectionPolicy.SelfKnown, "self_instance_id": self, "automatic_limit": 3, "automatic_window_seconds": 600})
	case path == "/protection/rules" && r.Method == "GET":
		items, err := p.s.ProtectionRules(r.Context())
		if err != nil {
			protectionError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": items})
		}
	case path == "/protection/rules" && r.Method == "POST" || strings.HasPrefix(path, "/protection/rules/") && r.Method == "PUT":
		id := strings.TrimPrefix(path, "/protection/rules/")
		if path == "/protection/rules" {
			id = ""
		} else if id == "" || strings.Contains(id, "/") {
			writeError(w, 404, "not_found")
			return true
		}
		var in model.ProtectionRuleInput
		if !readProtectionRulePayload(w, r, &in) {
			return true
		}
		if in.ConfirmedSelfID != "" && (!p.protectionPolicy.SelfKnown || p.protectionPolicy.SelfInstanceID != in.ConfirmedSelfID) {
			fieldError(w, "confirmed_self_instance_id", "not_current_server")
			return true
		}
		rule, err := p.s.SaveProtectionRule(r.Context(), id, in, p.now())
		if err != nil {
			protectionError(w, err)
		} else {
			status := 200
			if id == "" {
				status = 201
			}
			writeJSON(w, status, rule)
		}
	case strings.HasPrefix(path, "/protection/rules/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/protection/rules/")
		rev, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || rev < 1 {
			fieldError(w, "revision", "invalid_format")
			return true
		}
		if err = p.s.DeleteProtectionRule(r.Context(), id, rev, p.now()); err != nil {
			protectionError(w, err)
		} else {
			w.WriteHeader(204)
		}
	case path == "/protection/simulations" && r.Method == "GET":
		items, err := p.s.QuotaSimulations(r.Context(), p.now())
		if err != nil {
			protectionError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"items": items})
		}
	case path == "/protection/simulations" && r.Method == "PUT":
		var in model.QuotaSimulation
		if !readProtectionInput(w, r, &in, "revision", "account_id", "scope", "source_instance_id", "usage_bytes", "expires_at") {
			return true
		}
		result, err := p.s.SaveQuotaSimulation(r.Context(), in, p.now())
		if err != nil {
			protectionError(w, err)
		} else {
			writeJSON(w, 200, result)
		}
	case path == "/protection/simulations" && r.Method == "DELETE":
		q := r.URL.Query()
		rev, err := strconv.ParseInt(q.Get("revision"), 10, 64)
		if err != nil || rev < 1 {
			fieldError(w, "revision", "invalid_format")
			return true
		}
		err = p.s.ClearQuotaSimulation(r.Context(), model.QuotaSimulation{AccountID: q.Get("account_id"), Scope: q.Get("scope"), SourceInstanceID: q.Get("source_instance_id"), Revision: rev}, p.now())
		if err != nil {
			protectionError(w, err)
		} else {
			w.WriteHeader(204)
		}
	case path == "/protection/decisions" && r.Method == "GET":
		limit := 50
		var before int64
		var err error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		if err == nil {
			if raw := r.URL.Query().Get("before"); raw != "" {
				before, err = strconv.ParseInt(raw, 10, 64)
				if before < 1 {
					err = store.ErrProtectionRule
				}
			}
		}
		if err != nil || limit < 1 || limit > 200 {
			fieldError(w, "pagination", "invalid_format")
			return true
		}
		items, more, err := p.s.QuotaDecisions(r.Context(), r.URL.Query().Get("rule_id"), before, limit)
		if err != nil {
			protectionError(w, err)
		} else {
			var next any
			if more {
				next = items[len(items)-1].ID
			}
			writeJSON(w, 200, map[string]any{"items": items, "next_before": next})
		}
	default:
		writeError(w, 405, "method_not_allowed")
	}
	return true
}

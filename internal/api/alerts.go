package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (p *productionPanel) alertRules(w http.ResponseWriter, r *http.Request, path string) bool {
	if path == "/alert-rules" && r.Method == "GET" {
		raw := r.URL.Query().Get("include_reconciliation")
		if raw != "" && raw != "true" && raw != "false" {
			fieldError(w, "include_reconciliation", "invalid_format")
			return true
		}
		rules, err := p.s.AlertRules(r.Context())
		if err != nil {
			businessError(w, err)
		} else {
			if raw != "true" {
				filtered := []model.AlertRule{}
				for _, rule := range rules {
					if rule.Key != "cdt_estimation_deviation" {
						filtered = append(filtered, rule)
					}
				}
				rules = filtered
			}
			writeJSON(w, 200, map[string]any{"items": rules})
		}
		return true
	}
	if !strings.HasPrefix(path, "/alert-rules/") {
		return false
	}
	if r.Method != "PATCH" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	var update model.AlertRuleUpdate
	if !readBody(w, r, &update) {
		return true
	}
	rule, err := p.s.UpdateAlertRule(r.Context(), strings.TrimPrefix(path, "/alert-rules/"), update, p.now())
	var validation *store.AlertValidationError
	if errors.As(err, &validation) {
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": validation.Fields}})
	} else if err != nil {
		businessError(w, err)
	} else {
		writeJSON(w, 200, rule)
	}
	return true
}

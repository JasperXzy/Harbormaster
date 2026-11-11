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
		rules, err := p.s.AlertRules(r.Context())
		if err != nil {
			businessError(w, err)
		} else {
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

package api

import (
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/rampolicy"
)

func (p *productionPanel) ramPolicies(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/ram-policies" {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	writeJSON(w, 200, map[string]any{"items": rampolicy.Catalog()})
	return true
}

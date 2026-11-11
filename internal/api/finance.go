package api

import (
	"net/http"
	"strings"
)

func (p *productionPanel) finance(w http.ResponseWriter, r *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/accounts/"), "/")
	if !strings.HasPrefix(path, "/accounts/") || len(parts) != 2 || parts[1] != "finance" {
		return false
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	loc, err := p.businessLocation(r.Context())
	if err != nil {
		businessError(w, err)
		return true
	}
	data, err := p.s.AccountFinance(r.Context(), parts[0], p.now().In(loc).Format("2006-01"))
	if err != nil {
		businessError(w, err)
	} else {
		writeJSON(w, 200, data)
	}
	return true
}

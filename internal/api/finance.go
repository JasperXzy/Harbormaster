package api

import (
	"net/http"
	"strings"
)

func (p *productionPanel) finance(w http.ResponseWriter, r *http.Request, path string) bool {
	prefix := "/accounts/"
	if strings.HasPrefix(path, "/instances/") {
		prefix = "/instances/"
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if !strings.HasPrefix(path, prefix) || len(parts) != 2 || parts[1] != "finance" {
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
	cycle := p.now().In(loc).Format("2006-01")
	var data any
	if prefix == "/instances/" {
		data, err = p.s.InstanceFinance(r.Context(), parts[0], cycle)
	} else {
		data, err = p.s.AccountFinance(r.Context(), parts[0], cycle)
	}
	if err != nil {
		businessError(w, err)
	} else {
		writeJSON(w, 200, data)
	}
	return true
}

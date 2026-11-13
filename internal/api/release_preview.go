package api

import (
	"net/http"
	"strings"
)

func (p *productionPanel) releasePreview(w http.ResponseWriter, r *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if !strings.HasPrefix(path, "/instances/") || len(parts) != 2 || parts[0] == "" || parts[1] != "release-preview" {
		return false
	}
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	v, err := p.s.ReleasePreview(r.Context(), parts[0], p.now().UTC())
	if err != nil {
		businessError(w, err)
	} else {
		writeJSON(w, 200, v)
	}
	return true
}

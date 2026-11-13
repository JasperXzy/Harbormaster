package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func apiKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster", error="invalid_token"`)
		writeError(w, 401, "unauthenticated")
	case errors.Is(err, store.ErrAPIKeyScope):
		w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster", error="insufficient_scope", scope="instance:control"`)
		writeError(w, 403, "insufficient_scope")
	default:
		businessError(w, err)
	}
}

func (p *productionPanel) integrations(w http.ResponseWriter, r *http.Request, path string) {
	if _, ok := p.access.checkTransport(w, r); !ok {
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster"`)
		writeError(w, 401, "unauthenticated")
		return
	}
	if len(values) != 1 {
		apiKeyError(w, store.ErrUnauthorized)
		return
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		apiKeyError(w, store.ErrUnauthorized)
		return
	}
	key, err := p.s.AuthenticateAPIKey(r.Context(), parts[1], "instance:control", p.now())
	if err != nil {
		apiKeyError(w, err)
		return
	}
	ctx := model.WithJobTrigger(model.WithAPIKeyPrincipal(r.Context(), key.ID), "api")
	r = r.WithContext(ctx)
	target := strings.TrimPrefix(path, "/integrations")
	if target == "/instances/refresh" {
		p.integrationRefresh(w, r, key.ID)
		return
	}
	if strings.HasPrefix(target, "/instances/") && p.instancePower(w, r, target) {
		return
	}
	if strings.HasPrefix(target, "/jobs/") {
		id := strings.TrimPrefix(target, "/jobs/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, 404, "not_found")
			return
		}
		if r.Method != "GET" {
			writeError(w, 405, "method_not_allowed")
			return
		}
		j, err := p.s.APIKeyControlJob(ctx, key.ID, id)
		if err != nil {
			apiKeyError(w, err)
		} else {
			writeJSON(w, 200, j)
		}
		return
	}
	writeError(w, 404, "not_found")
}

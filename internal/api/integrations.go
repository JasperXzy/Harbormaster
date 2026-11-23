package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func apiKeyScopeError(w http.ResponseWriter, err error, requiredScope string) {
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster", error="invalid_token"`)
		writeError(w, 401, "unauthenticated")
	case errors.Is(err, store.ErrAPIKeyScope):
		w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster", error="insufficient_scope", scope="`+requiredScope+`"`)
		writeError(w, 403, "insufficient_scope")
	default:
		businessError(w, err)
	}
}

func apiKeyError(w http.ResponseWriter, err error) {
	apiKeyScopeError(w, err, "instance:control")
}

func extractBearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="harbormaster"`)
		writeError(w, 401, "unauthenticated")
		return "", false
	}
	if len(values) != 1 {
		apiKeyError(w, store.ErrUnauthorized)
		return "", false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		apiKeyError(w, store.ErrUnauthorized)
		return "", false
	}
	return parts[1], true
}

func (p *productionPanel) integrations(w http.ResponseWriter, r *http.Request, path string) {
	if path == "/integrations/summary" {
		// PRD F15.8: widget summary endpoint is exempt from admin_allow_cidrs.
		if _, ok := p.access.checkPublicTransport(w, r); !ok {
			return
		}
		if r.Method != "GET" {
			writeError(w, 405, "method_not_allowed")
			return
		}
		tok, ok := extractBearer(w, r)
		if !ok {
			return
		}
		if _, err := p.s.AuthenticateAPIKey(r.Context(), tok, "summary:read", p.now()); err != nil {
			apiKeyScopeError(w, err, "summary:read")
			return
		}
		summary, err := p.s.WidgetSummary(r.Context(), p.powerEnabled, p.timezone, p.now().UTC())
		if err != nil {
			businessError(w, err)
			return
		}
		writeJSON(w, 200, summary)
		return
	}
	if path == "/integrations/cron/run" {
		// PRD F13.4: URL Cron endpoint accepts ONLY cron:run API keys (never session/password)
		// and uses public transport check so external cron services can reach it safely.
		if _, ok := p.access.checkPublicTransport(w, r); !ok {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, 405, "method_not_allowed")
			return
		}
		tok, ok := extractBearer(w, r)
		if !ok {
			return
		}
		now := p.now().UTC()
		key, err := p.s.AuthenticateAPIKey(r.Context(), tok, "cron:run", now)
		if err != nil {
			apiKeyScopeError(w, err, "cron:run")
			return
		}
		accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
		if r.ContentLength != 0 {
			var in struct {
				AccountID *string `json:"account_id"`
			}
			if !readBody(w, r, &in) {
				return
			}
			if in.AccountID != nil {
				accountID = strings.TrimSpace(*in.AccountID)
			}
		}
		ctx := model.WithJobTrigger(model.WithAPIKeyPrincipal(r.Context(), key.ID), "api")
		j, err := p.s.EnqueueCronRun(ctx, key.ID, accountID, now)
		switch {
		case err == nil:
			if p.engine != nil {
				_ = p.engine.Schedule(ctx)
			}
			if loc, lErr := p.businessLocation(ctx); lErr == nil {
				_ = p.s.RefreshEstimates(ctx, loc, now)
			}
			_ = p.s.EvaluateAlerts(ctx, now)
			var jobID any
			if j != nil {
				jobID = j.ID
			}
			writeJSON(w, 202, map[string]any{"job_id": jobID, "scheduled": true, "triggered_at": now})
		case errors.Is(err, store.ErrRefreshParameters):
			fieldError(w, "account_id", "invalid_format")
		default:
			apiKeyScopeError(w, err, "cron:run")
		}
		return
	}
	if _, ok := p.access.checkTransport(w, r); !ok {
		return
	}
	tok, ok := extractBearer(w, r)
	if !ok {
		return
	}
	key, err := p.s.AuthenticateAPIKey(r.Context(), tok, "instance:control", p.now())
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

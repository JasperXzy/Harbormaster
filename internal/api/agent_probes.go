package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func (a *agentAPI) probes(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != agentproto.ProbeClaimPath && r.URL.Path != agentproto.ProbeResultPath {
		return false
	}
	if a.development {
		writeError(w, 404, "not_found")
		return true
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, 401, "invalid_agent_token")
		return true
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var out any
	var err error
	if r.URL.Path == agentproto.ProbeClaimPath {
		var in agentproto.ProbeClaim
		if !agentBody(w, r, &in, 4096) {
			return true
		}
		out, err = a.s.ClaimAgentProbe(ctx, token, in, time.Now().UTC())
	} else {
		var in agentproto.ProbeReport
		if !agentBody(w, r, &in, 64<<10) {
			return true
		}
		out, err = a.s.AcceptProbeResult(ctx, token, in, time.Now().UTC())
	}
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, 401, "invalid_agent_token")
	case errors.Is(err, store.ErrAccountDisabled):
		writeError(w, 403, "account_disabled")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, store.ErrLeaseLost):
		writeError(w, 409, "probe_expired")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "probe_result_conflict")
	case errors.Is(err, netprobe.ErrInvalid):
		writeError(w, 400, "invalid_probe")
	case err != nil:
		writeError(w, 503, "probe_unavailable")
	default:
		writeJSON(w, 200, out)
	}
	return true
}

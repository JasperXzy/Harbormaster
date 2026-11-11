package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type agentAPI struct {
	s           *store.Store
	development bool
	access      *panelAccess
}

func (a *agentAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Agent authentication is independent of admin cookies, Origin/CSRF and the
	// optional admin allowlist. Production transport is still HTTPS-only.
	if !a.development {
		if a.access == nil {
			writeError(w, 403, "https_required")
			return
		}
		ip, proxy := a.access.source(r)
		if !ip.IsValid() || !(r.TLS != nil || (proxy && r.Header.Get("X-Forwarded-Proto") == "https")) || !strings.EqualFold(r.Host, a.access.host) {
			writeError(w, 403, "https_required")
			return
		}
	}
	if r.Method != "POST" {
		writeError(w, 404, "not_found")
		return
	}
	if r.URL.Path == agentproto.RegisterPath {
		var body agentproto.Registration
		if !agentBody(w, r, &body, 16<<10) {
			return
		}
		if body.Validate() != nil {
			writeError(w, 400, "invalid_registration")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		result, err := a.s.RegisterAgent(ctx, body, time.Now().UTC())
		switch {
		case errors.Is(err, store.ErrRegistration):
			writeError(w, 401, "invalid_registration_code")
		case errors.Is(err, store.ErrRegistrationConsumed):
			writeError(w, 409, "registration_consumed")
		case errors.Is(err, store.ErrIdentity):
			writeError(w, 403, "instance_identity_mismatch")
		case errors.Is(err, store.ErrAccountDisabled):
			writeError(w, 403, "account_disabled")
		case err != nil:
			writeError(w, 503, "registration_unavailable")
		default:
			writeJSON(w, 201, result)
		}
		return
	}
	if r.URL.Path != agentproto.Path && r.URL.Path != agentproto.BatchPath {
		writeError(w, 404, "not_found")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, 401, "invalid_agent_token")
		return
	}
	var reports []agentproto.Report
	if r.URL.Path == agentproto.Path {
		var body agentproto.Report
		if !agentBody(w, r, &body, 16<<10) {
			return
		}
		reports = []agentproto.Report{body}
	} else {
		var body agentproto.Batch
		if !agentBody(w, r, &body, 128<<10) {
			return
		}
		reports = body.Reports
	}
	now := time.Now().UTC()
	if len(reports) == 0 || len(reports) > agentproto.MaxBatch {
		writeError(w, 400, "invalid_report")
		return
	}
	for _, report := range reports {
		if report.Validate(now) != nil {
			writeError(w, 400, "invalid_report")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	duplicates, err := a.s.AcceptReports(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), reports, now)
	switch {
	case errors.Is(err, store.ErrUnauthorized):
		writeError(w, 401, "invalid_agent_token")
	case errors.Is(err, store.ErrAccountDisabled):
		writeError(w, 403, "account_disabled")
	case errors.Is(err, store.ErrConflict):
		writeError(w, 409, "report_conflict")
	case errors.Is(err, store.ErrSampleExpired):
		writeError(w, 400, "sample_expired")
	case err != nil:
		writeError(w, 503, "report_unavailable")
	default:
		duplicate := true
		for _, d := range duplicates {
			duplicate = duplicate && d
		}
		last := reports[len(reports)-1]
		writeJSON(w, 200, agentproto.Ack{AcceptedSequence: last.Sequence, CounterEpoch: last.CounterEpoch, ServerTime: now, Duplicate: duplicate})
	}
}
func agentBody(w http.ResponseWriter, r *http.Request, body any, limit int64) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "unsupported_media_type")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if d.Decode(body) != nil || d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "invalid_report")
		return false
	}
	return true
}

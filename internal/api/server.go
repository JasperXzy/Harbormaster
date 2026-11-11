package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/devmode"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func Handler(s *store.Store) http.Handler {
	return NewHandler(s, false, "Asia/Shanghai", nil)
}

func NewHandler(s *store.Store, dev bool, timezone string, frontend http.Handler) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Ready(ctx); err != nil {
			writeError(w, 503, "not_ready")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	m.HandleFunc("POST "+agentproto.Path, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeError(w, 401, "invalid_agent_token")
			return
		}
		var report agentproto.Report
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		d.DisallowUnknownFields()
		if d.Decode(&report) != nil || d.Decode(new(any)) != io.EOF {
			writeError(w, 400, "invalid_report")
			return
		}
		now := time.Now().UTC()
		if err := report.Validate(now); err != nil {
			writeError(w, 400, "invalid_report")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		dup, err := s.AcceptReport(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), report, now)
		switch {
		case errors.Is(err, store.ErrUnauthorized):
			writeError(w, 401, "invalid_agent_token")
		case errors.Is(err, store.ErrConflict):
			writeError(w, 409, "report_conflict")
		case err != nil:
			writeError(w, 503, "report_unavailable")
		default:
			writeJSON(w, 200, agentproto.Ack{AcceptedSequence: report.Sequence, ServerTime: now, Duplicate: dup})
		}
	})
	// API misses must never receive a successful SPA document.
	if dev {
		m.Handle("/api/v1/", devmode.New(s, timezone))
	}
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not_found") })
	m.HandleFunc("/agent/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not_found") })
	if frontend != nil {
		m.Handle("/", frontend)
	} else {
		m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not_found") })
	}
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}

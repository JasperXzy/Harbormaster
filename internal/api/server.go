package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/JasperXzy/harbormaster/internal/store"
)

func Handler(s *store.Store) http.Handler {
	return NewHandler(s, nil, nil)
}

func NewHandler(s *store.Store, panel, frontend http.Handler) http.Handler {
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
	var mode string
	_ = s.DB.QueryRow("SELECT value FROM app_meta WHERE key='mode'").Scan(&mode)
	var agentAccess *panelAccess
	var agentReleaseDir string
	if production, ok := panel.(*productionPanel); ok {
		access := production.access
		agentAccess = &access
		agentReleaseDir = production.agentReleaseDir
	}
	m.Handle("/agent/", &agentAPI{s: s, development: mode == "development", access: agentAccess, releaseDir: agentReleaseDir})
	// API misses must never receive a successful SPA document.
	if panel != nil {
		m.Handle("/api/v1/", panel)
	}
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not_found") })
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

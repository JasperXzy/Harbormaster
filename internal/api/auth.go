package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentrelease"
	"github.com/JasperXzy/harbormaster/internal/buildinfo"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type productionPanel struct {
	s               *store.Store
	access          panelAccess
	timezone        string
	ttl             time.Duration
	hashing         sync.Mutex
	now             func() time.Time
	engine          *collect.Engine
	publicURL       string
	agentReleaseDir string
	releaseVersions agentrelease.VersionCache
}

func NewProductionPanel(s *store.Store, c config.Config, engine *collect.Engine) http.Handler {
	return &productionPanel{s: s, access: newPanelAccess(c), timezone: c.Timezone, ttl: time.Duration(c.SessionTTLSeconds) * time.Second, now: time.Now, engine: engine, publicURL: c.PublicURL, agentReleaseDir: c.AgentReleaseDir}
}

func (p *productionPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	source, ok := p.access.check(w, r)
	if !ok {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if path == "/setup/status" && r.Method == "GET" {
		initialized, err := p.s.Initialized(r.Context())
		if err != nil {
			writeError(w, 503, "internal_error")
			return
		}
		writeJSON(w, 200, map[string]any{"initialized": initialized, "password_min_length": 12})
		return
	}
	if (path == "/setup" || path == "/session") && r.Method == "POST" {
		p.authenticate(w, r, source, path == "/setup")
		return
	}
	token, expiry, err := p.session(r)
	if err != nil {
		if errors.Is(err, store.ErrUnauthorized) {
			writeError(w, 401, "unauthenticated")
		} else {
			writeError(w, 503, "internal_error")
		}
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sessionCSRF(token))) != 1 {
		writeError(w, 403, "csrf_failed")
		return
	}
	if path == "/session" {
		switch r.Method {
		case "GET":
			sessionResponse(w, 200, token, expiry)
			return
		case "DELETE":
			h := sha256.Sum256([]byte(token))
			if _, err := p.s.DB.ExecContext(r.Context(), "DELETE FROM session WHERE token_hash=?", h[:]); err != nil {
				writeError(w, 503, "internal_error")
				return
			}
			http.SetCookie(w, sessionCookie("", time.Unix(1, 0), -1))
			w.WriteHeader(204)
			return
		default:
			writeError(w, 405, "method_not_allowed")
			return
		}
	}
	if p.agentManagement(w, r, path) || p.notificationReads(w, r, path) || p.alertRules(w, r, path) || p.apiKeys(w, r, path) || p.finance(w, r, path) || p.instanceEvents(w, r, path) || p.ramPolicies(w, r, path) || p.business(w, r, path) {
		return
	}
	if r.Method != "GET" {
		writeError(w, 501, "not_implemented")
		return
	}
	switch path {
	case "/system/info":
		var builtAt any
		if t, err := time.Parse(time.RFC3339, buildinfo.BuildTime); err == nil {
			builtAt = t.UTC().Format(time.RFC3339)
		}
		writeJSON(w, 200, map[string]any{"version": buildinfo.Version, "commit": buildinfo.Commit, "built_at": builtAt, "latest_agent_version": p.releaseVersions.Get(p.agentReleaseDir, p.now()), "data_mode": "live", "read_only": p.engine == nil})
	case "/settings":
		zone := p.timezone
		err := p.s.DB.QueryRowContext(r.Context(), "SELECT value FROM app_meta WHERE key='business_timezone'").Scan(&zone)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeError(w, 503, "internal_error")
			return
		}
		scanSeconds := 300
		cdtSeconds := 300
		if p.engine != nil {
			scanSeconds = int(p.engine.Interval.Seconds())
			cdtSeconds = int(p.engine.TrafficInterval().Seconds())
		}
		offline, err := p.s.AgentOfflineAfter(r.Context())
		if err != nil {
			businessError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"business_timezone": zone, "collection": map[string]int{"cdt_poll_interval_seconds": cdtSeconds, "agent_report_interval_seconds": 10, "agent_offline_after_seconds": int(offline.Seconds()), "resource_scan_interval_seconds": scanSeconds}})
	case "/events":
		p.events(w, r)
	case "/accounts", "/instances", "/traffic/quotas", "/traffic/daily", "/traffic/billing-subjects", "/bandwidth-packages", "/regions", "/notify/telegram", "/alert-rules", "/notifications":
		// A production service must never substitute demo fixtures for missing collectors.
		writeError(w, 501, "not_implemented")
	default:
		writeError(w, 404, "not_found")
	}
}

func sessionCSRF(token string) string {
	h := sha256.Sum256([]byte("harbormaster:session-csrf:v1:" + token))
	return hex.EncodeToString(h[:])
}

func sessionCookie(token string, expiry time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{Name: "hm_session", Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, Expires: expiry, MaxAge: maxAge}
}

func sessionResponse(w http.ResponseWriter, status int, token string, expiry time.Time) {
	writeJSON(w, status, map[string]string{"csrf_token": sessionCSRF(token), "expires_at": expiry.UTC().Format(time.RFC3339Nano)})
}

func (p *productionPanel) session(r *http.Request) (string, time.Time, error) {
	c, err := r.Cookie("hm_session")
	if err != nil || len(c.Value) != 64 {
		return "", time.Time{}, store.ErrUnauthorized
	}
	if _, err = hex.DecodeString(c.Value); err != nil {
		return "", time.Time{}, store.ErrUnauthorized
	}
	h := sha256.Sum256([]byte(c.Value))
	var expiry string
	err = p.s.DB.QueryRowContext(r.Context(), "SELECT expires_at FROM session WHERE token_hash=?", h[:]).Scan(&expiry)
	if errors.Is(err, sql.ErrNoRows) {
		err = store.ErrUnauthorized
	}
	if err != nil {
		return "", time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !p.now().Before(t) {
		return "", time.Time{}, store.ErrUnauthorized
	}
	return c.Value, t, nil
}

func readBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "unsupported_media_type")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil || d.Decode(new(any)) != io.EOF {
		writeError(w, 422, "validation_failed")
		return false
	}
	return true
}

func rateLimited(w http.ResponseWriter, seconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, 429, map[string]any{"error": map[string]any{"code": "rate_limited", "message": "rate_limited", "retry_after_seconds": seconds}})
}

func (p *productionPanel) authenticate(w http.ResponseWriter, r *http.Request, source string, setup bool) {
	if setup {
		initialized, err := p.s.Initialized(r.Context())
		if err != nil {
			writeError(w, 503, "internal_error")
			return
		}
		if initialized {
			writeError(w, 409, "already_initialized")
			return
		}
	}
	wait, err := p.s.TakeAuthAttempt(r.Context(), source, p.now())
	if err != nil {
		writeError(w, 503, "internal_error")
		return
	}
	if wait > 0 {
		rateLimited(w, wait)
		return
	}
	// Argon2 uses 64 MiB; never allocate one working set per concurrent request.
	if !p.hashing.TryLock() {
		rateLimited(w, 1)
		return
	}
	defer p.hashing.Unlock()
	var password, setupToken, timezone string
	if setup {
		var body struct {
			Token    string `json:"setup_token"`
			Password string `json:"password"`
			Timezone string `json:"business_timezone"`
		}
		if !readBody(w, r, &body) {
			return
		}
		password, setupToken, timezone = body.Password, body.Token, body.Timezone
		if err = p.s.CheckSetup(r.Context(), []byte(setupToken), p.now()); err != nil {
			setupError(w, err)
			return
		}
		if timezone == "" {
			timezone = p.timezone
		}
		if _, err = time.LoadLocation(timezone); err != nil {
			fieldError(w, "business_timezone", "invalid_format")
			return
		}
	} else {
		var body struct {
			Password string `json:"password"`
		}
		if !readBody(w, r, &body) {
			return
		}
		password = body.Password
	}
	if len(password) < 12 || len(password) > 1024 {
		if setup {
			fieldError(w, "password", "out_of_range")
		} else {
			writeError(w, 401, "invalid_credentials")
		}
		return
	}
	secret := []byte(password)
	defer clear(secret)
	var hash string
	if setup {
		hash, err = security.HashPassword(secret)
	} else {
		err = p.s.DB.QueryRowContext(r.Context(), "SELECT password_hash FROM administrator WHERE id=1").Scan(&hash)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 401, "invalid_credentials")
			return
		}
		if err == nil && !security.VerifyPassword(hash, secret) {
			writeError(w, 401, "invalid_credentials")
			return
		}
	}
	if err != nil {
		writeError(w, 503, "internal_error")
		return
	}
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		writeError(w, 503, "internal_error")
		return
	}
	token := hex.EncodeToString(raw[:])
	h := sha256.Sum256([]byte(token))
	now := p.now()
	expiry := now.Add(p.ttl)
	status := 200
	if setup {
		err = p.s.CompleteSetup(r.Context(), []byte(setupToken), hash, timezone, h[:], expiry, now)
		status = 201
	} else {
		err = p.s.CreateSession(r.Context(), hash, h[:], expiry, now)
	}
	if err != nil {
		if setup {
			setupError(w, err)
		} else if errors.Is(err, store.ErrUnauthorized) {
			writeError(w, 401, "invalid_credentials")
		} else {
			writeError(w, 503, "internal_error")
		}
		return
	}
	http.SetCookie(w, sessionCookie(token, expiry, int(p.ttl.Seconds())))
	sessionResponse(w, status, token, expiry)
}

func fieldError(w http.ResponseWriter, field, code string) {
	writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": map[string]string{field: code}}})
}

func setupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInitialized):
		writeError(w, 409, "already_initialized")
	case errors.Is(err, store.ErrSetupToken):
		writeError(w, 403, "invalid_setup_token")
	default:
		writeError(w, 503, "internal_error")
	}
}

func (p *productionPanel) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, 503, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, payload any) bool {
		ctl := http.NewResponseController(w)
		_ = ctl.SetWriteDeadline(p.now().Add(5 * time.Second))
		defer ctl.SetWriteDeadline(time.Time{})
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		return ctl.Flush() == nil
	}
	seq, err := p.s.JobChangeHead(r.Context())
	if err != nil {
		return
	}
	noticeSeq, err := p.s.NotificationChangeHead(r.Context())
	if err != nil {
		return
	}
	if !send("ping", map[string]any{}) {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	updates := time.NewTicker(5 * time.Second)
	defer updates.Stop()
	agentState := map[string]string{}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, _, err := p.session(r); err != nil || !send("ping", map[string]any{}) {
				return
			}
		case <-updates.C:
			if _, _, err := p.session(r); err != nil {
				return
			}
			if !p.agentEvents(r.Context(), agentState, send) {
				return
			}
			notices, nextNotice, err := p.s.NotificationChanges(r.Context(), noticeSeq)
			if err != nil {
				return
			}
			noticeSeq = nextNotice
			for _, notice := range notices {
				event := "notification.updated"
				if notice.Created {
					event = "notification.created"
				}
				if !send(event, notice.Notification) {
					return
				}
			}
			jobs, next, err := p.s.JobChanges(r.Context(), seq)
			if err != nil {
				return
			}
			seq = next
			for _, j := range jobs {
				if !send("job.updated", j) {
					return
				}
				if (j.Status == "succeeded" || j.Status == "failed") && (j.Type == "refresh" || j.Type == "permission_check") {
					for _, id := range j.AccountIDs {
						quotas, err := p.quotaRows(r.Context(), id)
						if err != nil {
							return
						}
						for _, q := range quotas {
							if !send("quota.updated", q) {
								return
							}
						}
					}
				}
			}
		}
	}
}

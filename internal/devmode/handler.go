package devmode

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type handler struct {
	s     *store.Store
	loc   *time.Location
	login sync.Mutex
}

func New(s *store.Store, timezone string) http.Handler {
	loc, _ := time.LoadLocation(timezone)
	if loc == nil {
		loc = time.UTC
	}
	return &handler{s: s, loc: loc}
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, status int, code string) {
	respond(w, status, object{"error": object{"code": code, "message": code}})
}
func csrf(token string) string {
	h := sha256.Sum256([]byte("hm-dev-csrf:" + token))
	return hex.EncodeToString(h[:])
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Harbormaster-Mode", "development")
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if r.Method != "GET" {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				failure(w, 403, "csrf_failed")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			failure(w, 403, "csrf_failed")
			return
		}
	}
	if path == "/setup/status" && r.Method == "GET" {
		respond(w, 200, object{"initialized": true, "password_min_length": 12})
		return
	}
	if path == "/session" && r.Method == "POST" {
		h.signIn(w, r)
		return
	}
	token, expiry, ok := h.session(r)
	if !ok {
		failure(w, 401, "unauthenticated")
		return
	}
	if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrf(token))) != 1 {
		failure(w, 403, "csrf_failed")
		return
	}
	if path == "/session" {
		switch r.Method {
		case "GET":
			respond(w, 200, object{"csrf_token": csrf(token), "expires_at": expiry})
			return
		case "DELETE":
			sum := sha256.Sum256([]byte(token))
			if _, err := h.s.DB.ExecContext(r.Context(), "DELETE FROM session WHERE token_hash=?", sum[:]); err != nil {
				failure(w, 503, "internal_error")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "hm_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			respond(w, 204, nil)
			return
		}
	}
	if r.Method != "GET" {
		failure(w, 501, "not_implemented")
		return
	}
	if path == "/events" {
		h.events(w, r)
		return
	}
	now := time.Now().UTC()
	f := makeFixtures(now, h.loc)
	switch path {
	case "/system/info":
		respond(w, 200, object{"version": "dev-m0", "commit": "unknown", "built_at": nil, "latest_agent_version": "dev", "data_mode": "demo", "read_only": true})
	case "/settings":
		respond(w, 200, object{"business_timezone": h.loc.String(), "collection": object{"cdt_poll_interval_seconds": 300, "agent_report_interval_seconds": 10, "agent_offline_after_seconds": 30, "resource_scan_interval_seconds": 300}})
	case "/regions":
		respond(w, 200, object{"items": []object{{"id": "ap-southeast-1", "mainland": false}, {"id": "cn-hongkong", "mainland": false}, {"id": "cn-shanghai", "mainland": true}}})
	case "/accounts":
		respond(w, 200, object{"items": f.Accounts})
	case "/instances":
		items := filter(f.Instances, "account_id", r.URL.Query().Get("account_id"))
		items = filter(items, "kind", r.URL.Query().Get("kind"))
		paged, cursor, err := paginate(items, r)
		if err != nil {
			failure(w, 422, "validation_failed")
			return
		}
		respond(w, 200, object{"items": paged, "next_cursor": cursor, "updated_at": now.Format(time.RFC3339)})
	case "/traffic/quotas":
		respond(w, 200, object{"items": filter(f.Quotas, "account_id", r.URL.Query().Get("account_id"))})
	case "/traffic/observations":
		respond(w, 200, object{"items": []object{}, "next_cursor": nil})
	case "/traffic/billing-subjects":
		respond(w, 200, object{"items": filter(f.Subjects, "account_id", r.URL.Query().Get("account_id"))})
	case "/bandwidth-packages":
		respond(w, 200, object{"items": f.Packages})
	case "/traffic/daily":
		id := r.URL.Query().Get("account_id")
		if len(filter(f.Accounts, "id", id)) == 0 || id == "" {
			failure(w, 404, "not_found")
			return
		}
		local := now.In(h.loc)
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, h.loc)
		days := []object{}
		for day := 1; day <= local.Day(); day++ {
			days = append(days, object{"date": start.AddDate(0, 0, day-1).Format("2006-01-02"), "mainland_bytes": 0, "overseas_bytes": int64(day) * 100000000, "provisional": day == local.Day()})
		}
		respond(w, 200, object{"account_id": id, "cycle_start": start.UTC().Format(time.RFC3339), "cycle_end": start.AddDate(0, 1, 0).UTC().Format(time.RFC3339), "data_timezone": h.loc.String(), "days": days, "updated_at": now.Format(time.RFC3339)})
	case "/bandwidth-packages/demo-package/hourly":
		respond(w, 200, object{"package_id": "demo-package", "period_seconds": 3600, "periods": []object{{"start": now.Truncate(time.Hour).Format(time.RFC3339), "rx_bytes": int64(3 << 30), "tx_bytes": int64(2 << 30), "metered_bytes": int64(3 << 30)}}, "updated_at": now.Format(time.RFC3339)})
	case "/bandwidth-packages/demo-package-hk/hourly":
		respond(w, 200, object{"package_id": "demo-package-hk", "period_seconds": 3600, "periods": []object{}, "updated_at": nil})
	case "/notify/telegram":
		respond(w, 200, object{"bot_token_set": false, "chat_id": "", "proxy_mode": "none", "proxy_url": "", "status": "not_configured", "last_error": nil, "last_success_at": nil})
	case "/alert-rules":
		respond(w, 200, object{"items": f.Rules})
	case "/notifications":
		items, cursor, err := paginate(f.Notifications, r)
		if err != nil {
			failure(w, 422, "validation_failed")
			return
		}
		respond(w, 200, object{"items": items, "next_cursor": cursor})
	default:
		failure(w, 404, "not_found")
	}
}

func filter(in []object, key, value string) []object {
	if value == "" {
		return in
	}
	out := []object{}
	for _, o := range in {
		if o[key] == value {
			out = append(out, o)
		}
	}
	return out
}
func paginate(in []object, r *http.Request) ([]object, any, error) {
	limit := 50
	var err error
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil || limit < 1 || limit > 200 {
			return nil, nil, fmt.Errorf("invalid limit")
		}
	}
	start := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		b, e := base64.RawURLEncoding.DecodeString(c)
		if e != nil {
			return nil, nil, e
		}
		start, err = strconv.Atoi(string(b))
		if err != nil || start < 0 || start > len(in) {
			return nil, nil, fmt.Errorf("invalid cursor")
		}
	}
	end := min(start+limit, len(in))
	var next any
	if end < len(in) {
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return in[start:end], next, nil
}

func (h *handler) signIn(w http.ResponseWriter, r *http.Request) {
	// Bound concurrent Argon2 allocations even in the local development server.
	if !h.login.TryLock() {
		failure(w, 429, "rate_limited")
		return
	}
	defer h.login.Unlock()
	var body struct {
		Password string `json:"password"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
		failure(w, 422, "validation_failed")
		return
	}
	var hash string
	if h.s.DB.QueryRowContext(r.Context(), "SELECT password_hash FROM administrator WHERE id=1").Scan(&hash) != nil {
		failure(w, 503, "internal_error")
		return
	}
	if !security.VerifyPassword(hash, []byte(body.Password)) {
		failure(w, 401, "invalid_credentials")
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		failure(w, 503, "internal_error")
		return
	}
	token := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.s.DB.ExecContext(r.Context(), "INSERT INTO session VALUES(?,?)", sum[:], expires); err != nil {
		failure(w, 503, "internal_error")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hm_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 3600})
	respond(w, 200, object{"csrf_token": csrf(token), "expires_at": expires})
}

func (h *handler) session(r *http.Request) (string, string, bool) {
	c, err := r.Cookie("hm_session")
	if err != nil || len(c.Value) != 64 {
		return "", "", false
	}
	sum := sha256.Sum256([]byte(c.Value))
	var expiry string
	if h.s.DB.QueryRowContext(r.Context(), "SELECT expires_at FROM session WHERE token_hash=?", sum[:]).Scan(&expiry) != nil {
		return "", "", false
	}
	t, err := time.Parse(time.RFC3339, expiry)
	return c.Value, expiry, err == nil && time.Now().Before(t)
}

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		failure(w, 503, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, v any) bool {
		ctl := http.NewResponseController(w)
		_ = ctl.SetWriteDeadline(time.Now().Add(5 * time.Second))
		defer ctl.SetWriteDeadline(time.Time{})
		b, _ := json.Marshal(v)
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send("ping", object{}) {
		return
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	updates := time.NewTicker(10 * time.Second)
	defer updates.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, _, ok = h.session(r); !ok {
				return
			}
			if !send("ping", object{}) {
				return
			}
		case now := <-updates.C:
			if _, _, ok = h.session(r); !ok {
				return
			}
			f := makeFixtures(now, h.loc)
			for _, q := range f.Quotas {
				if !send("quota.updated", q) {
					return
				}
			}
		}
	}
}

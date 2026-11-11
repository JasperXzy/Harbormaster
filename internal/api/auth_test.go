package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

const testSetupToken = "test-bootstrap-token-not-a-real-secret-0001"
const testPassword = "test-password-not-a-real-secret"

func testPanel(t *testing.T) *productionPanel {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p := NewProductionPanel(s, config.Config{PublicURL: "https://panel.example", Timezone: "Asia/Shanghai", SessionTTLSeconds: 43200}, nil).(*productionPanel)
	if err := s.PrepareSetup(context.Background(), []byte(testSetupToken), p.now()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHTTPSCookieAndSSERevocation(t *testing.T) {
	p := testPanel(t)
	ts := httptest.NewUnstartedServer(nil)
	p.access = newPanelAccess(config.Config{PublicURL: "https://" + ts.Listener.Addr().String()})
	ts.Config.Handler = NewHandler(p.s, p, nil)
	ts.StartTLS()
	defer ts.Close()
	client := ts.Client()
	client.Timeout = 20 * time.Second
	client.Jar, _ = cookiejar.New(nil)
	body, _ := json.Marshal(map[string]string{"setup_token": testSetupToken, "password": testPassword})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/setup", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatal("HTTPS setup failed", res.StatusCode)
	}
	res, err = client.Get(ts.URL + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("secure cookie not retained by browser-style jar")
	}
	stream, err := client.Get(ts.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("SSE unavailable")
	}
	r := bufio.NewReader(stream.Body)
	for _, want := range []string{"event: ping\n", "data: {}\n", "\n"} {
		line, err := r.ReadString('\n')
		if err != nil || line != want {
			t.Fatal("bad SSE handshake", err)
		}
	}
	job, err := p.s.EnqueueJob(context.Background(), "refresh", "all", []string{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadString('\n')
	if err != nil || line != "event: job.updated\n" {
		t.Fatal("job update not delivered", err)
	}
	line, err = r.ReadString('\n')
	if err != nil || !strings.Contains(line, job.ID) || !strings.Contains(line, `"status":"queued"`) {
		t.Fatal("incorrect job event", err)
	}
	if line, err = r.ReadString('\n'); err != nil || line != "\n" {
		t.Fatal("incorrect event framing")
	}
	if err := p.s.ResetPassword(context.Background(), "$argon2id$test-replacement"); err != nil {
		t.Fatal(err)
	}
	// Revocation must close an already-open stream by the next 15-second ping.
	if _, err = r.ReadString('\n'); err != io.EOF {
		t.Fatal("SSE survived session revocation", err)
	}
	res, err = client.Get(ts.URL + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("revoked HTTPS session still active")
	}
}

func panelRequest(p *productionPanel, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "https://panel.example/api/v1"+path, strings.NewReader(string(b)))
	r.RemoteAddr = "203.0.113.10:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://panel.example")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func setupPanel(t *testing.T, p *productionPanel) (*http.Cookie, string) {
	t.Helper()
	w := panelRequest(p, "POST", "/setup", map[string]string{"setup_token": testSetupToken, "password": testPassword, "business_timezone": "Asia/Singapore"}, nil, "")
	if w.Code != 201 {
		t.Fatalf("setup status %d: %s", w.Code, w.Body)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return w.Result().Cookies()[0], session.CSRF
}

func TestProductionAuthLifecycle(t *testing.T) {
	p := testPanel(t)
	w := panelRequest(p, "GET", "/setup/status", nil, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"initialized":false`) {
		t.Fatal("not initially uninitialized")
	}
	w = panelRequest(p, "POST", "/setup", map[string]string{"setup_token": "incorrect", "password": testPassword}, nil, "")
	if w.Code != 403 {
		t.Fatal("invalid setup token accepted")
	}
	w = panelRequest(p, "POST", "/setup", map[string]string{"setup_token": testSetupToken, "password": testPassword, "business_timezone": "not/a/timezone"}, nil, "")
	if w.Code != 422 {
		t.Fatal("invalid timezone accepted")
	}
	cookie, csrf := setupPanel(t, p)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 43200 || len(cookie.Value) != 64 || csrf == cookie.Value {
		t.Fatal("unsafe session cookie or CSRF")
	}
	var n int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM setup_credential").Scan(&n); err != nil || n != 0 {
		t.Fatal("bootstrap survived initialization")
	}
	var hash string
	if err := p.s.DB.QueryRow("SELECT password_hash FROM administrator").Scan(&hash); err != nil || !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, testPassword) {
		t.Fatal("password not hashed")
	}
	w = panelRequest(p, "POST", "/setup", map[string]string{"setup_token": testSetupToken, "password": testPassword}, nil, "")
	if w.Code != 409 {
		t.Fatal("setup replay accepted", w.Code)
	}
	w = panelRequest(p, "GET", "/settings", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Asia/Singapore") {
		t.Fatal("setup timezone not persisted")
	}
	w = panelRequest(p, "GET", "/session", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), csrf) {
		t.Fatal("session lookup failed")
	}
	w = panelRequest(p, "GET", "/system/info", nil, nil, "")
	if w.Code != 401 {
		t.Fatal("anonymous panel access")
	}
	w = panelRequest(p, "DELETE", "/session", nil, cookie, "wrong")
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	w = panelRequest(p, "DELETE", "/session", nil, cookie, csrf)
	if w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 || !w.Result().Cookies()[0].Secure {
		t.Fatal("logout failed")
	}
	w = panelRequest(p, "GET", "/session", nil, cookie, "")
	if w.Code != 401 {
		t.Fatal("session survived logout")
	}
	w = panelRequest(p, "POST", "/session", map[string]string{"password": testPassword}, nil, "")
	if w.Code != 200 {
		t.Fatalf("login failed %d: %s", w.Code, w.Body)
	}
	cookie = w.Result().Cookies()[0]
	newHash, err := security.HashPassword([]byte("replacement-test-password"))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.s.ResetPassword(context.Background(), newHash); err != nil {
		t.Fatal(err)
	}
	if panelRequest(p, "GET", "/session", nil, cookie, "").Code != 401 {
		t.Fatal("reset did not revoke session")
	}
	staleSession := sha256.Sum256([]byte("test-stale-login"))
	if err = p.s.CreateSession(context.Background(), hash, staleSession[:], p.now().Add(time.Hour), p.now()); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("old verified password raced reset", err)
	}
	// Advance beyond the persisted rate window, not the session lifetime.
	now := time.Now().Add(6 * time.Minute)
	p.now = func() time.Time { return now }
	if w = panelRequest(p, "POST", "/session", map[string]string{"password": testPassword}, nil, ""); w.Code != 401 {
		t.Fatal("old password still accepted")
	}
	if w = panelRequest(p, "POST", "/session", map[string]string{"password": "replacement-test-password"}, nil, ""); w.Code != 200 {
		t.Fatal("new password rejected", w.Code)
	}
	cookie = w.Result().Cookies()[0]
	now = now.Add(12 * time.Hour)
	if panelRequest(p, "GET", "/session", nil, cookie, "").Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestAuthOriginAndProxyBoundaries(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	p.access = newPanelAccess(config.Config{PublicURL: "https://panel.example", TrustedProxies: []string{"127.0.0.1/32"}, AdminAllowCIDRs: []string{"203.0.113.10/32"}})
	for _, tc := range []struct {
		name, url, peer, origin, xff, proto string
		status                              int
	}{
		{"tls", "https://panel.example", "203.0.113.10:5", "https://panel.example", "", "", 200},
		{"wrong-host", "https://evil.example", "203.0.113.10:5", "https://panel.example", "", "", 403},
		{"wrong-origin", "https://panel.example", "203.0.113.10:5", "https://evil.example", "", "", 403},
		{"missing-origin", "https://panel.example", "203.0.113.10:5", "", "", "", 403},
		{"forged-https", "http://panel.example", "203.0.113.10:5", "https://panel.example", "203.0.113.10", "https", 403},
		{"proxy", "http://panel.example", "127.0.0.1:5", "https://panel.example", "203.0.113.10", "https", 200},
		{"forged-leftmost", "http://panel.example", "127.0.0.1:5", "https://panel.example", "203.0.113.10, 198.51.100.9", "https", 403},
		{"untrusted-proxy", "http://panel.example", "198.51.100.9:5", "https://panel.example", "203.0.113.10", "https", 403},
		{"invalid-xff", "http://panel.example", "127.0.0.1:5", "https://panel.example", "invalid", "https", 403},
		{"missing-xff", "http://panel.example", "127.0.0.1:5", "https://panel.example", "", "https", 403},
		{"allowlist", "https://panel.example", "198.51.100.9:5", "https://panel.example", "203.0.113.10", "", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.url+"/api/v1/session", strings.NewReader(`{"password":"`+testPassword+`"}`))
			r.RemoteAddr = tc.peer
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Forwarded-For", tc.xff)
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d want %d", w.Code, tc.status)
			}
		})
	}
	// Authenticated writes need both same-origin and the session-bound CSRF token.
	r := httptest.NewRequest("DELETE", "https://panel.example/api/v1/session", nil)
	r.RemoteAddr = "203.0.113.10:5"
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("valid CSRF bypassed origin policy")
	}
	// Panel allowlist does not block health probes or replace agent-token auth.
	h := NewHandler(p.s, p, nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://panel.example/healthz", nil))
	if w.Code != 200 {
		t.Fatal("health endpoint restricted by panel policy")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "https://panel.example/agent/v1/reports", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatal("agent endpoint inherited panel policy")
	}
}

func TestProductionRateLimitPersists(t *testing.T) {
	p := testPanel(t)
	for i := 0; i < 5; i++ {
		w := panelRequest(p, "POST", "/setup", map[string]string{"setup_token": "invalid", "password": testPassword}, nil, "")
		if w.Code != 403 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	// A fresh handler models a process restart; the limit lives in SQLite.
	q := NewProductionPanel(p.s, config.Config{PublicURL: "https://panel.example", Timezone: "Asia/Shanghai", SessionTTLSeconds: 43200}, nil).(*productionPanel)
	w := panelRequest(q, "POST", "/setup", map[string]string{"setup_token": testSetupToken, "password": testPassword}, nil, "")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "retry_after_seconds") {
		t.Fatal("limit lost on handler restart")
	}
	q.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	setupPanel(t, q)
}

func TestAuthInputBoundaries(t *testing.T) {
	p := testPanel(t)
	for _, tc := range []struct {
		body, content string
		status        int
	}{
		{`{"password":"x"}`, "text/plain", 415},
		{`{"password":"x","unexpected":1}`, "application/json", 422},
		{`{"password":"x"} {}`, "application/json", 422},
		{`{"password":"` + strings.Repeat("x", 20<<10) + `"}`, "application/json", 422},
	} {
		r := httptest.NewRequest("POST", "https://panel.example/api/v1/session", strings.NewReader(tc.body))
		r.RemoteAddr = "203.0.113.10:5"
		r.Header.Set("Origin", "https://panel.example")
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal("unexpected input accepted", w.Code, tc.status)
		}
	}
}

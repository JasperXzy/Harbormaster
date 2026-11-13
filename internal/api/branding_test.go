package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/branding"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestBrandingAPIKeepsAuthenticationRevisionAndImageBoundaries(t *testing.T) {
	p := testPanel(t)
	if w := panelRequest(p, "GET", "/settings/branding", nil, nil, ""); w.Code != 401 {
		t.Fatal("pre-login visibility was not authorized", w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	callImage := func(method, path, media string, raw []byte, session *http.Cookie, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://panel.example/api/v1"+path, bytes.NewReader(raw))
		r.RemoteAddr = "203.0.113.10:12345"
		r.Header.Set("Origin", "https://panel.example")
		r.Header.Set("Content-Type", media)
		r.Header.Set("X-CSRF-Token", token)
		if session != nil {
			r.AddCookie(session)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	w := panelRequest(p, "GET", "/settings/branding", nil, cookie, "")
	var state model.Branding
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Name != model.DefaultBrandingName || state.Revision != 0 || state.Logo != nil || state.UpdatedAt != nil {
		t.Fatal(w.Code, w.Body)
	}
	if w := callImage("GET", "/settings/branding/logo", "", nil, cookie, ""); w.Code != 404 {
		t.Fatal("default image fabricated", w.Code)
	}
	name := map[string]any{"revision": 0, "name": "  港口 fixture  "}
	if w := panelRequest(p, "PUT", "/settings/branding", name, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	for _, bad := range []map[string]any{
		{"revision": 0}, {"name": "new"}, {"revision": -1, "name": "new"},
		{"revision": 0, "name": " "}, {"revision": 0, "name": "line\nline"},
		{"revision": 0, "name": strings.Repeat("界", 101)},
		{"revision": 0, "name": "new", "logo_url": "https://invalid.example/image"},
	} {
		if w := panelRequest(p, "PUT", "/settings/branding", bad, cookie, csrf); w.Code != 422 {
			t.Fatal("invalid replacement accepted", w.Code, w.Body)
		}
	}
	w = panelRequest(p, "PUT", "/settings/branding", name, cookie, csrf)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Revision != 1 || state.Name != "港口 fixture" {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "PUT", "/settings/branding", name, cookie, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "branding_changed") {
		t.Fatal("stale name edit accepted", w.Code, w.Body)
	}
	makePNG := func(width int) []byte {
		var out bytes.Buffer
		if err := png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, width, 3))); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	image := append(makePNG(9), []byte("unretained-upload-trailer-fixture")...)
	if w := callImage("PUT", "/settings/branding/logo?revision=1", "image/png", image, cookie, ""); w.Code != 403 {
		t.Fatal("image write bypassed CSRF", w.Code)
	}
	for _, query := range []string{"", "?revision=-1", "?revision=1&revision=1", "?revision=01", "?revision=bad"} {
		if w := callImage("PUT", "/settings/branding/logo"+query, "image/png", image, cookie, csrf); w.Code != 422 {
			t.Fatal("invalid image revision accepted", w.Code, w.Body)
		}
	}
	if w := callImage("PUT", "/settings/branding/logo?revision=0", "image/png", image, cookie, csrf); w.Code != 409 {
		t.Fatal("stale image edit accepted", w.Code)
	}
	w = callImage("PUT", "/settings/branding/logo?revision=1", "image/png", image, cookie, csrf)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Revision != 2 || state.Logo == nil || state.Logo.Width != 9 || state.Logo.Height != 3 || state.Name != "港口 fixture" {
		t.Fatal(w.Code, w.Body)
	}
	imagePath := strings.TrimPrefix(state.Logo.URL, "/api/v1")
	w = callImage("GET", imagePath, "", nil, cookie, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) || bytes.Contains(w.Body.Bytes(), []byte("unretained-upload-trailer-fixture")) {
		t.Fatal("unsafe or incorrect image response", w.Code, w.Header())
	}
	if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatal(err)
	}
	if w := callImage("GET", imagePath, "", nil, nil, ""); w.Code != 401 {
		t.Fatal("anonymous image read accepted", w.Code)
	}
	for _, bad := range []struct {
		media string
		raw   []byte
		code  int
	}{
		{"image/svg+xml", []byte(`<svg/>`), 415},
		{"image/jpeg", image, 422},
		{"image/png", []byte("https://invalid.example/image.png"), 422},
		{"image/png", makePNG(513), 422},
		{"image/png", make([]byte, branding.MaxLogoBytes+1), 413},
	} {
		if w := callImage("PUT", "/settings/branding/logo?revision=2", bad.media, bad.raw, cookie, csrf); w.Code != bad.code {
			t.Fatal("bad image accepted", w.Code, w.Body)
		}
	}
	previousAccess := p.access
	p.access = newPanelAccess(config.Config{PublicURL: "https://panel.example", AdminAllowCIDRs: []string{"198.51.100.0/24"}})
	if w := callImage("GET", imagePath, "", nil, cookie, ""); w.Code != 403 {
		t.Fatal("Logo read bypassed admin CIDR", w.Code)
	}
	p.access = previousAccess
	if w := callImage("DELETE", "/settings/branding/logo?revision=1", "", nil, cookie, csrf); w.Code != 409 {
		t.Fatal("stale delete accepted", w.Code)
	}
	w = callImage("DELETE", "/settings/branding/logo?revision=2", "", nil, cookie, csrf)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Revision != 3 || state.Logo != nil || state.Name != "港口 fixture" {
		t.Fatal(w.Code, w.Body)
	}
	w = callImage("DELETE", "/settings/branding/logo?revision=3", "", nil, cookie, csrf)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Revision != 3 {
		t.Fatal("empty delete was not idempotent", w.Code, w.Body)
	}
	var jobs, audits int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("branding created background/cloud work", err, jobs)
	}
	if err := p.s.DB.QueryRow("SELECT count(*) FROM audit_log WHERE action='branding_updated' AND actor_kind='admin'").Scan(&audits); err != nil || audits != 3 {
		t.Fatal("missing or fake audit events", err, audits)
	}
	if w := panelRequest(p, "DELETE", "/session", nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := callImage("GET", imagePath, "", nil, cookie, ""); w.Code != 401 {
		t.Fatal("revoked session read an asset", w.Code)
	}
}

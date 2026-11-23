package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestHeartbeatSettingsEncryptURLAndOnlyQueueTests(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{7}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box}
	var calls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	secretURL := target.URL + "/private-test-token?key=private-query"
	if w := panelRequest(p, "GET", "/settings/heartbeat", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "PUT", "/settings/heartbeat", map[string]any{"revision": 0, "enabled": true, "interval_seconds": 60, "url": secretURL}, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "PUT", "/settings/heartbeat", map[string]any{"revision": 0, "enabled": true, "interval_seconds": 60, "url": secretURL}, cookie, csrf)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
		t.Fatal(w.Code, w.Body)
	}
	cipher, state, err := p.s.HeartbeatSettings(context.Background())
	if err != nil || !state.URLConfigured || state.Revision != 1 || bytes.Contains(cipher, []byte("private-test-token")) {
		t.Fatal("URL not safely stored", err)
	}
	plain, err := box.Decrypt(cipher, model.HeartbeatCredentialOwner)
	if err != nil || string(plain) != secretURL {
		t.Fatal("cipher destination mismatch", err)
	}
	clear(plain)
	w = panelRequest(p, "GET", "/settings/heartbeat", nil, cookie, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), secretURL) || strings.Contains(w.Body.String(), "private-query") {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "POST", "/settings/heartbeat/test", struct{}{}, cookie, csrf)
	var queued struct {
		ID string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &queued) != nil || queued.ID == "" {
		t.Fatal(w.Code, w.Body)
	}
	job, err := p.s.Job(context.Background(), queued.ID)
	if err != nil || job.Type != "heartbeat_test" || job.Status != "queued" || job.Trigger != "panel" {
		t.Fatal(job, err)
	}
	if calls.Load() != 0 {
		t.Fatal("configuration or test endpoint sent synchronously")
	}
	w = panelRequest(p, "PUT", "/settings/heartbeat", map[string]any{"revision": 1, "enabled": false, "interval_seconds": 90}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	preserved, state, err := p.s.HeartbeatSettings(context.Background())
	if err != nil || !bytes.Equal(preserved, cipher) || state.Revision != 2 {
		t.Fatal("omitted URL changed credential", err)
	}
	w = panelRequest(p, "PUT", "/settings/heartbeat", map[string]any{"revision": 1, "enabled": false, "interval_seconds": 90, "url": ""}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("stale update accepted", w.Code)
	}
	w = panelRequest(p, "PUT", "/settings/heartbeat", map[string]any{"revision": 2, "enabled": false, "interval_seconds": 90, "url": ""}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	_, state, err = p.s.HeartbeatSettings(context.Background())
	if err != nil || state.URLConfigured {
		t.Fatal("URL not cleared", err)
	}
	w = panelRequest(p, "POST", "/settings/heartbeat/test", struct{}{}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("missing destination test accepted", w.Code)
	}
}

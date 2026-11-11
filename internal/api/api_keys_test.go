package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestAPIKeyManagementKeepsAuthenticationBoundaries(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	input := map[string]any{"name": "widget", "scopes": []string{"summary:read"}}
	if w := panelRequest(p, "POST", "/api-keys", input, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "POST", "/api-keys", input, cookie, ""); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	for _, invalid := range []map[string]any{{"name": "widget", "scopes": []string{"*"}}, {"name": "widget", "scopes": []string{}}, {"name": "widget", "scopes": []string{"summary:read"}, "token": "caller-selected-secret"}} {
		if w := panelRequest(p, "POST", "/api-keys", invalid, cookie, csrf); w.Code != 422 {
			t.Fatal("invalid grants accepted", w.Code)
		}
	}
	w := panelRequest(p, "POST", "/api-keys", input, cookie, csrf)
	var created struct {
		Key   model.APIKey `json:"api_key"`
		Token string       `json:"token"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Key.ID == "" || len(created.Token) != 71 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid creation response", w.Code)
	}
	if _, err := p.s.AuthenticateAPIKey(context.Background(), created.Token, "summary:read", p.now()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api-keys", "/api-keys?limit=1"} {
		w = panelRequest(p, "GET", path, nil, cookie, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), created.Token) || strings.Contains(w.Body.String(), "token_hash") || strings.Contains(w.Body.String(), `"token"`) {
			t.Fatal("list leaked secret or failed", w.Code)
		}
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?cursor=!!!", "?cursor=MA", "?cursor=LTE"} {
		if w := panelRequest(p, "GET", "/api-keys"+query, nil, cookie, ""); w.Code != 422 {
			t.Fatal(query, w.Code)
		}
	}
	// A scoped API key is not an administrator session, even if sent as Bearer.
	r := httptest.NewRequest("GET", "https://panel.example/api/v1/api-keys", nil)
	r.Header.Set("Authorization", "Bearer "+created.Token)
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("API key became admin session", w.Code)
	}
	r = httptest.NewRequest("POST", "https://panel.example/agent/v1/reports", strings.NewReader(`{"version":1,"agent_id":"not-an-agent","counter_epoch":"test","sequence":1,"sampled_at":"`+p.now().UTC().Format("2006-01-02T15:04:05Z")+`","uptime_ms":1,"rx_bytes":0,"tx_bytes":0,"agent_version":"test"}`))
	r.Header.Set("Authorization", "Bearer "+created.Token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	NewHandler(p.s, p, nil).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("API key became agent token", w.Code)
	}
	if w := panelRequest(p, "DELETE", "/api-keys/"+created.Key.ID, nil, cookie, ""); w.Code != 403 {
		t.Fatal("revoke CSRF bypass", w.Code)
	}
	for range 2 {
		if w := panelRequest(p, "DELETE", "/api-keys/"+created.Key.ID, nil, cookie, csrf); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	if _, err := p.s.AuthenticateAPIKey(context.Background(), created.Token, "summary:read", p.now()); err == nil {
		t.Fatal("revoked key accepted")
	}
	w = panelRequest(p, "GET", "/api-keys", nil, cookie, "")
	var listed struct {
		Items []model.APIKey `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 || listed.Items[0].RevokedAt == nil {
		t.Fatal("revoked metadata missing")
	}
}

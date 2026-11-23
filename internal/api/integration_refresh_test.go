package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func refreshPanel(t *testing.T) (*productionPanel, *apiScanner, model.Account) {
	t.Helper()
	p := testPanel(t)
	box, err := security.NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	a, _, err := p.engine.CreateAccount(context.Background(), model.AccountCreate{Name: "refresh test", Site: "china", RegionIDs: []string{"cn-shanghai"}, Credential: model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "synthetic-refresh-test-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.engine.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p, scanner, a
}

func TestIntegrationRefreshUsesScopedBearerAndBackgroundCollector(t *testing.T) {
	p, scanner, account := refreshPanel(t)
	ctx := context.Background()
	cookie, csrf := setupPanel(t, p)
	key := func(scope string) (model.APIKey, string) {
		t.Helper()
		k, token, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "test", Scopes: []string{scope}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return k, token
	}
	a, tokenA := key("instance:control")
	_, tokenB := key("instance:control")
	_, tokenC := key("instance:control")
	_, summary := key("summary:read")
	_, cron := key("cron:run")
	path := "/integrations/instances/refresh"
	body := map[string]string{"account_id": account.ID}
	for _, test := range []struct {
		token string
		want  int
	}{{"", 401}, {"wrong", 401}, {summary, 403}, {cron, 403}} {
		w := integrationRequest(p, "POST", path, test.token, "", body, cookie)
		if w.Code != test.want || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatal("refresh authentication", w.Code, w.Body)
		}
	}
	if w := integrationRequest(p, "POST", path+"?access_token="+tokenA, "", "", body, cookie); w.Code != 401 {
		t.Fatal("refresh used URL token or cookie fallback", w.Code)
	}
	for _, body := range []any{nil, []string{}, map[string]any{"account_id": nil}, map[string]any{"account_id": true}, map[string]any{"account_id": strings.Repeat("x", 129)}, map[string]any{"token": tokenA}, map[string]any{"trigger": "cli"}, map[string]any{"command": "arbitrary"}} {
		if w := integrationRequest(p, "POST", path, tokenA, "", body, nil); w.Code != 422 {
			t.Fatal("invalid refresh body", w.Code, w.Body)
		}
	}
	if w := integrationRequest(p, "GET", path, tokenA, "", nil, nil); w.Code != 405 {
		t.Fatal(w.Code, w.Body)
	}
	if w := integrationRequest(p, "POST", path, tokenA, "", map[string]string{"account_id": "missing"}, nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	// Sharing a panel read does not transfer power ownership or change its origin.
	panel := panelRequest(p, "POST", "/instances/refresh", body, cookie, csrf)
	jobID := acceptedPowerID(t, panel)
	if w := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, tokenA, "", nil, nil); w.Code != 404 {
		t.Fatal("GET joined unrequested work", w.Code)
	}
	if p.powerEnabled {
		t.Fatal("fixture accidentally enabled cloud writes")
	}
	w := integrationRequest(p, "POST", path, tokenA, "", body, nil)
	if got := acceptedPowerID(t, w); got != jobID {
		t.Fatal("did not coalesce existing read")
	}
	if scanner.calls != 1 {
		t.Fatal("request performed synchronous cloud I/O")
	}
	for _, token := range []string{tokenA, tokenB} {
		if token == tokenB {
			if w := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, token, "", nil, nil); w.Code != 404 {
				t.Fatal("other key read shared task without joining", w.Code)
			}
			if got := acceptedPowerID(t, integrationRequest(p, "POST", path, token, "", body, nil)); got != jobID {
				t.Fatal("read sharing duplicated work")
			}
		}
		w := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, token, "", nil, nil)
		var j model.Job
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.Trigger != "panel" || j.Type != "refresh" || j.Status != "queued" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "synthetic-refresh-test-secret") || strings.Contains(w.Body.String(), "lease_") {
			t.Fatal("refresh task leaked credentials or lease")
		}
	}
	if err := p.s.RevokeAPIKey(ctx, a.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		target := path
		if method == "GET" {
			target = "/integrations/jobs/" + jobID
		}
		if w := integrationRequest(p, method, target, tokenA, "", body, nil); w.Code != 401 {
			t.Fatal("revoked refresh key accepted", w.Code)
		}
	}
	if ran, err := p.engine.RunOne(ctx); err != nil || !ran || scanner.calls != 2 {
		t.Fatal("independently authorized collection was cancelled", ran, err)
	}
	w = integrationRequest(p, "GET", "/integrations/jobs/"+jobID, tokenB, "", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"succeeded"`) {
		t.Fatal(w.Code, w.Body)
	}
	if w := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, tokenC, "", nil, nil); w.Code != 404 {
		t.Fatal("completed history escaped membership", w.Code)
	}
	var desired string
	if err := p.s.DB.QueryRow("SELECT desired_state FROM instance WHERE id=?", account.ID+"_instance_1").Scan(&desired); err != nil || desired != "stopped" {
		t.Fatal("refresh changed instance intent", desired, err)
	}
	// A completed read does not prevent requesting fresh data later.
	fresh := acceptedPowerID(t, integrationRequest(p, "POST", path, tokenB, "", body, nil))
	if fresh == jobID {
		t.Fatal("completed refresh reused indefinitely")
	}
	j, err := p.s.Job(ctx, fresh)
	if err != nil || j.Trigger != "api" || j.Status != "queued" || scanner.calls != 2 {
		t.Fatal(j, err)
	}
	if w := integrationRequest(p, "GET", "/accounts", tokenB, "", nil, nil); w.Code != 401 {
		t.Fatal("refresh granted an administrator session", w.Code)
	}
}

func TestIntegrationRefreshAllScopeConflictAndAvailability(t *testing.T) {
	p, scanner, account := refreshPanel(t)
	ctx := context.Background()
	k, token, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "all", Scopes: []string{"instance:control"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := "/integrations/instances/refresh"
	first := acceptedPowerID(t, integrationRequest(p, "POST", path, token, "", map[string]any{}, nil))
	second, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{Name: "another", Site: "china", RegionIDs: []string{"cn-shanghai"}, Credential: model.Credential{AccessKeyID: "SecondTestKey123456", AccessKeySecret: "second-synthetic-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	w := integrationRequest(p, "POST", path, token, "", map[string]any{}, nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "refresh_scope_changed") {
		t.Fatal("all refresh silently omitted account", w.Code, w.Body)
	}
	j, err := p.s.APIKeyControlJob(ctx, k.ID, first)
	if err != nil || len(j.AccountIDs) != 1 || j.AccountIDs[0] != account.ID || scanner.calls != 1 {
		t.Fatal(j, err)
	}
	if got := acceptedPowerID(t, integrationRequest(p, "POST", path, token, "", map[string]string{"account_id": second.ID}, nil)); got == first {
		t.Fatal("different account joined wrong read scope")
	}
	if _, err = p.engine.UpdateAccount(ctx, second.ID, model.AccountUpdate{Enabled: new(false)}); err != nil {
		t.Fatal(err)
	}
	if w = integrationRequest(p, "POST", path, token, "", map[string]string{"account_id": second.ID}, nil); w.Code != 409 || !strings.Contains(w.Body.String(), "account_disabled") {
		t.Fatal(w.Code, w.Body)
	}
	engine := p.engine
	p.engine = nil
	if w = integrationRequest(p, "POST", path, token, "", map[string]any{}, nil); w.Code != 503 || !strings.Contains(w.Body.String(), "collector_unavailable") {
		t.Fatal(w.Code, w.Body)
	}
	p.engine = engine
	// Transport and header validation happen before the refresh dispatcher.
	for _, scheme := range []string{"http", "https"} {
		host := "panel.example"
		if scheme == "https" {
			host = "wrong.example"
		}
		r := httptest.NewRequest("POST", scheme+"://"+host+"/api/v1"+path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("refresh bypassed transport boundary", w.Code)
		}
	}
}

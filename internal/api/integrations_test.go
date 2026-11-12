package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func integrationRequest(p *productionPanel, method, path, token, event string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "https://panel.example/api/v1"+path, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if event != "" {
		r.Header.Set("Idempotency-Key", event)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}
func TestIntegrationPowerUsesScopedBearerAndCancelsRevokedWork(t *testing.T) {
	p := testPanel(t)
	ctx := context.Background()
	box, _ := security.NewBox(bytes.Repeat([]byte{6}, 32))
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	p.powerEnabled = true
	a, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{Name: "integration", Site: "china", RegionIDs: []string{"cn-shanghai"}, Credential: model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "not-a-real-secret-for-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.engine.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	create := func(scope string) (model.APIKey, string) {
		k, token, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "test", Scopes: []string{scope}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return k, token
	}
	key, token := create("instance:control")
	_, other := create("instance:control")
	_, limited := create("summary:read")
	cookie, csrf := setupPanel(t, p)
	id := a.ID + "_instance_1"
	path := "/integrations/instances/" + id + "/power"
	event := model.NewID("event_")
	body := map[string]string{"action": "stop", "stop_mode": "keep_charging"}
	for _, test := range []struct {
		token  string
		cookie *http.Cookie
		status int
	}{{"", nil, 401}, {"", cookie, 401}, {"invalid", cookie, 401}, {limited, nil, 403}} {
		w := integrationRequest(p, "POST", path, test.token, event, body, test.cookie)
		if w.Code != test.status || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatal(test.status, w.Code, w.Body)
		}
	}
	if w := integrationRequest(p, "POST", path+"?access_token="+token, "", event, body, nil); w.Code != 401 {
		t.Fatal("URL token accepted")
	}
	p.powerEnabled = false
	if w := integrationRequest(p, "POST", path, token, event, body, nil); w.Code != 503 {
		t.Fatal("disabled control accepted", w.Code, w.Body)
	}
	p.powerEnabled = true
	w := integrationRequest(p, "POST", path, token, event, body, nil)
	jobID := acceptedPowerID(t, w)
	if scanner.calls != 1 || strings.Contains(w.Body.String(), token) {
		t.Fatal("submission called cloud or leaked bearer")
	}
	retry := acceptedPowerID(t, integrationRequest(p, "POST", path, token, event, body, nil))
	if retry != jobID {
		t.Fatal("token retry duplicated task")
	}
	detail := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, token, "", nil, nil)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"trigger":"api"`) || !strings.Contains(detail.Body.String(), `"status":"queued"`) || detail.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(detail.Code, detail.Body)
	}
	if w := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, other, "", nil, nil); w.Code != 404 {
		t.Fatal("other key read job", w.Code, w.Body)
	}
	if w := integrationRequest(p, "POST", path, other, event, body, nil); w.Code != 409 {
		t.Fatal("cross-key dedupe accepted", w.Code, w.Body)
	}
	if w := integrationRequest(p, "GET", "/accounts", token, "", nil, nil); w.Code != 401 {
		t.Fatal("bearer became administrator", w.Code, w.Body)
	}
	if w := integrationRequest(p, "GET", "/integrations/api-keys", token, "", nil, nil); w.Code != 404 {
		t.Fatal("integration escaped route allowlist", w.Code, w.Body)
	}
	head, err := p.s.JobChangeHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w := panelRequest(p, "DELETE", "/api-keys/"+key.ID, nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w := integrationRequest(p, "GET", "/integrations/jobs/"+jobID, token, "", nil, nil); w.Code != 401 {
		t.Fatal("revoked key still authorized", w.Code, w.Body)
	}
	if w := integrationRequest(p, "POST", path, token, event, body, nil); w.Code != 401 {
		t.Fatal("revoked retry accepted", w.Code, w.Body)
	}
	admin := panelRequest(p, "GET", "/jobs/"+jobID, nil, cookie, "")
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), "api_key_revoked") {
		t.Fatal("admin lost cancellation evidence", admin.Code, admin.Body)
	}
	intents, err := p.s.InstanceIntents(ctx, []string{id})
	if err != nil || intents[id].DesiredState != "stopped" {
		t.Fatal("revocation reset intent", intents, err)
	}
	after, err := p.s.JobChangeHead(ctx)
	if err != nil || after <= head {
		t.Fatal("revocation did not publish job change", after, err)
	}
	keys, err := p.s.APIKeys(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ID == key.ID && k.LastUsedAt == nil {
			t.Fatal("successful bearer use not recorded")
		}
	}
}

func TestIntegrationTransportAndHeaderBoundary(t *testing.T) {
	p := testPanel(t)
	_, token, err := p.s.CreateAPIKey(context.Background(), model.APIKeyCreate{Name: "transport", Scopes: []string{"instance:control"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		scheme, host string
		auth         []string
		want         int
	}{
		{"http", "panel.example", []string{"Bearer " + token}, 403},
		{"https", "wrong.example", []string{"Bearer " + token}, 403},
		{"https", "panel.example", []string{"Bearer " + token, "Bearer " + token}, 401},
		{"https", "panel.example", []string{"Basic " + token}, 401},
		{"https", "panel.example", []string{"bearer " + token}, 404},
	} {
		r := httptest.NewRequest("GET", tc.scheme+"://"+tc.host+"/api/v1/integrations/jobs/missing", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		for _, a := range tc.auth {
			r.Header.Add("Authorization", a)
		}
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(tc.scheme, tc.host, w.Code, w.Body)
		}
	}
}

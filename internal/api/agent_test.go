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

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

func TestAgentHTTPRegistrationBatchAndIndependentAuth(t *testing.T) {
	p := testPanel(t)
	for _, q := range []string{
		"INSERT INTO account VALUES('a','test','china','now')",
		"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','test','cn-shanghai','running','stopped','now')",
		`INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{"id":"ecs","counts_toward_cdt":true}',1,'now')`,
	} {
		if _, err := p.s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	code, _, err := p.s.AgentGrant(context.Background(), "ecs", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(p.s, p, nil)
	request := func(origin, path, token string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", origin+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	reg := agentproto.Registration{Version: 1, Code: code, Nonce: strings.Repeat("a", 64), Metadata: agentproto.Metadata{InstanceID: "i-test", RegionID: "cn-shanghai"}, AgentVersion: "test"}
	for _, origin := range []string{"http://panel.example", "https://wrong.example"} {
		if w := request(origin, agentproto.RegisterPath, "", reg); w.Code != 403 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := request("https://panel.example", agentproto.RegisterPath, "", reg)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), code) || strings.Contains(w.Body.String(), "hm_agent_") {
		t.Fatal("credential response leak")
	}
	var registered agentproto.Registered
	if err = json.Unmarshal(w.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, reg.Nonce)
	report := agentproto.Report{Version: 1, AgentID: registered.AgentID, CounterEpoch: "epoch", Sequence: 1, UptimeMS: 1000, SampledAt: time.Now().UTC()}
	if w = request("https://panel.example", agentproto.BatchPath, "", agentproto.Batch{Reports: []agentproto.Report{report}}); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = request("https://panel.example", agentproto.BatchPath, token, agentproto.Batch{Reports: []agentproto.Report{report}})
	var ack agentproto.Ack
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ack) != nil || ack.CounterEpoch != "epoch" || ack.AcceptedSequence != 1 {
		t.Fatal(w.Code, w.Body)
	}
	w = request("https://panel.example", agentproto.BatchPath, token, map[string]any{"reports": []agentproto.Report{report}, "command": "anything"})
	if w.Code != 400 {
		t.Fatal("unknown commands accepted")
	}
	// Admin credentials and CSRF are required for revocation, not for reports.
	cookie, csrf := setupPanel(t, p)
	if w = panelRequest(p, http.MethodDelete, "/instances/ecs/agent/token", nil, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, http.MethodDelete, "/instances/ecs/agent/token", nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("https://panel.example", agentproto.Path, token, report); w.Code != 401 {
		t.Fatal("revoked token accepted")
	}
}

func TestAgentEventsClearRatesAndCoalesce(t *testing.T) {
	p := testPanel(t)
	if _, err := p.s.DB.Exec("INSERT INTO account VALUES('a','test','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec(`INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{"id":"ecs","counts_toward_cdt":true}',1,'now')`); err != nil {
		t.Fatal(err)
	}
	previous := map[string]string{}
	calls := 0
	send := func(event string, payload any) bool {
		calls++
		raw, _ := json.Marshal(payload)
		if event == "instance.realtime" && !strings.Contains(string(raw), `"realtime":null`) {
			t.Fatal(string(raw))
		}
		return true
	}
	for range 2 {
		if !p.agentEvents(context.Background(), previous, send) {
			t.Fatal("events failed")
		}
	}
	if calls != 2 {
		t.Fatal("unchanged events repeated", calls)
	}
}

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/agentrelease"
	"github.com/JasperXzy/harbormaster/internal/model"
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

func TestInstallCommandChecksArtifactsBeforeIssuingGrant(t *testing.T) {
	p := testPanel(t)
	for _, q := range []string{
		"INSERT INTO account VALUES('a','test','china','now')",
		"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','test','cn-shanghai','running','stopped','now')",
		`INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{"id":"ecs"}',1,'now')`,
	} {
		if _, err := p.s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cookie, csrf := setupPanel(t, p)
	checkSystemVersion := func(want any) {
		t.Helper()
		w := panelRequest(p, "GET", "/system/info", nil, cookie, "")
		var info map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || info["latest_agent_version"] != want {
			t.Fatal("system info advertised wrong agent release", w.Body.String())
		}
	}
	checkSystemVersion(nil)
	path := "/instances/ecs/agent/install-command"
	if w := panelRequest(p, "POST", path, nil, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w := panelRequest(p, "POST", path, nil, cookie, csrf); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	var count int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM agent_registration").Scan(&count); err != nil || count != 0 {
		t.Fatal("unavailable release issued grant")
	}
	dir := t.TempDir()
	var manifest strings.Builder
	for _, name := range agentrelease.Files {
		raw := []byte("test artifact " + name)
		if name == "VERSION" {
			raw = []byte("v1.2.0\n")
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(raw), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(manifest.String()), 0600); err != nil {
		t.Fatal(err)
	}
	p.agentReleaseDir = dir
	checkSystemVersion("v1.2.0")
	checkHint := func(want *bool) {
		t.Helper()
		items := []map[string]any{{"id": "ecs"}}
		if err := p.overlayAgents(context.Background(), items); err != nil {
			t.Fatal(err)
		}
		a := items[0]["agent"].(model.AgentStatus)
		if a.AvailableVersion == nil || *a.AvailableVersion != "v1.2.0" {
			t.Fatal("available release missing", a)
		}
		if want == nil {
			if a.UpdateAvailable != nil {
				t.Fatal("unknown installation compared", a)
			}
			return
		}
		if a.UpdateAvailable == nil || *a.UpdateAvailable != *want {
			t.Fatal("incorrect upgrade hint", a)
		}
	}
	checkHint(nil)
	code, _, err := p.s.AgentGrant(context.Background(), "ecs", p.now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.s.RegisterAgent(context.Background(), agentproto.Registration{Version: 1, Code: code, Nonce: strings.Repeat("b", 64), AgentVersion: "v1.1.0", Metadata: agentproto.Metadata{InstanceID: "i-test", RegionID: "cn-shanghai"}}, p.now())
	if err != nil {
		t.Fatal(err)
	}
	newer := true
	checkHint(&newer)
	sawHint := false
	if !p.agentEvents(context.Background(), map[string]string{}, func(event string, payload any) bool {
		if event == "agent.status" {
			raw, _ := json.Marshal(payload)
			sawHint = strings.Contains(string(raw), `"update_available":true`) && strings.Contains(string(raw), `"available_version":"v1.2.0"`)
		}
		return true
	}) || !sawHint {
		t.Fatal("SSE upgrade hint missing")
	}
	if err := p.s.RevokeAgent(context.Background(), "ecs", p.now()); err != nil {
		t.Fatal(err)
	}
	checkHint(nil)
	w := panelRequest(p, "POST", path, nil, cookie, csrf)
	if w.Code != 201 || !strings.Contains(w.Body.String(), "hm_reg_") || !strings.Contains(w.Body.String(), "sha256sum") {
		t.Fatal(w.Code, w.Body)
	}
	var firstHash []byte
	if err := p.s.DB.QueryRow("SELECT code_hash FROM agent_registration WHERE instance_id='ecs'").Scan(&firstHash); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "POST", path, nil, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var secondHash []byte
	if err := p.s.DB.QueryRow("SELECT code_hash FROM agent_registration WHERE instance_id='ecs'").Scan(&secondHash); err != nil || bytes.Equal(firstHash, secondHash) {
		t.Fatal("grant did not rotate")
	}
	h := NewHandler(p.s, p, nil)
	for _, origin := range []string{"https://panel.example", "http://panel.example"} {
		r := httptest.NewRequest("GET", origin+"/agent/v1/releases/hm-agent.service", nil)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 200
		if strings.HasPrefix(origin, "http:") {
			expected = 403
		}
		if w.Code != expected {
			t.Fatal(w.Code, w.Body)
		}
	}
	if _, err := p.s.DB.Exec("UPDATE resource_snapshot SET present=0"); err != nil {
		t.Fatal(err)
	}
	if w = panelRequest(p, "POST", path, nil, cookie, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "agent_not_supported") {
		t.Fatal(w.Code, w.Body)
	}
	// A cached hint must never let a now-corrupt release mint a new grant.
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("v9.9.9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if w = panelRequest(p, "POST", path, nil, cookie, csrf); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}

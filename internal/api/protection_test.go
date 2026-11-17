package api

import (
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
	"time"
)

func TestQuotaProtectionAPIIsLocalSessionOnlyRevisionFenced(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	for _, path := range []string{"/protection/status", "/protection/rules", "/protection/simulations", "/protection/decisions"} {
		if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	a := model.Account{ID: "a", Name: "a", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}}
	raw, _ := json.Marshal(a)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO account VALUES('a','a','china','now')", nil},
		{"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a',?,1)", []any{string(raw)}},
		{"INSERT INTO instance VALUES('ecs','a','i-test','ecs','name','cn-shanghai','running','stopped','now')", nil},
		{"INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')", nil},
	} {
		if _, err := p.s.DB.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	in := model.ProtectionRuleInput{Name: "quota", Enabled: true, AccountID: "a", Scope: "mainland", ThresholdKind: "percent", Threshold: 95, Action: "stop_notify", Targets: []string{"ecs"}, StopMode: "keep_charging", ReserveSeconds: 60, DryRun: true}
	if w := panelRequest(p, "POST", "/protection/rules", in, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "POST", "/protection/rules", map[string]any{"name": "missing fields"}, cookie, csrf); w.Code != 422 {
		t.Fatal(w.Code, w.Body)
	}
	in.ConfirmedSelfID = "ecs"
	if w := panelRequest(p, "POST", "/protection/rules", in, cookie, csrf); w.Code != 422 {
		t.Fatal("unknown self confirmation accepted", w.Code)
	}
	in.ConfirmedSelfID = ""
	w := panelRequest(p, "POST", "/protection/rules", in, cookie, csrf)
	var rule model.ProtectionRule
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &rule) != nil || rule.Revision != 1 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "PUT", "/protection/rules/"+rule.ID, in, cookie, csrf); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	for _, path := range []string{"/protection/status", "/protection/rules", "/protection/decisions?limit=1"} {
		if w := panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	sim := model.QuotaSimulation{AccountID: "a", Scope: "mainland", Usage: 300000, ExpiresAt: p.now().Add(time.Minute)}
	w = panelRequest(p, "PUT", "/protection/simulations", sim, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "DELETE", "/protection/simulations?account_id=a&scope=mainland&revision=1", nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "PUT", "/instances/ecs/protection-exemption", map[string]int{"duration_seconds": 60}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "DELETE", "/instances/ecs/protection-exemption", nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w := panelRequest(p, "DELETE", "/protection/rules/"+rule.ID+"?revision=1", nil, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	var n int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job WHERE type='instance_power'").Scan(&n); err != nil || n != 0 {
		t.Fatal("management API sent or queued cloud action", n, err)
	}
}

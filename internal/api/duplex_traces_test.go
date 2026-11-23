package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func duplexBody(key string) map[string]any {
	return map[string]any{"request_key": strings.Repeat(key, 32), "from_instance_id": "ecs", "to_instance_id": "swas", "family": "ipv4", "method": "tcp", "forward_port": 443, "reverse_port": 8443, "count": 1, "max_hops": 3}
}
func seedDuplexEndpoints(t *testing.T, p *productionPanel) (map[string]string, map[string]string) {
	t.Helper()
	for _, query := range []string{
		"INSERT INTO account VALUES('a','synthetic','china','now')",
		"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','from (test)','cn-shanghai','running','stopped','now')",
		"INSERT INTO instance VALUES('swas','a','swas-test','swas','to (test)','cn-shanghai','running','stopped','now')",
		`INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')`,
		`INSERT INTO resource_snapshot VALUES('a','swas','swas','cn-shanghai','{}',1,'now')`,
	} {
		if _, err := p.s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	now := p.now()
	agents, tokens := map[string]string{}, map[string]string{}
	for i, id := range []string{"ecs", "swas"} {
		address := []string{"1.1.1.1", "8.8.8.8"}[i]
		if _, err := p.s.DB.Exec("INSERT INTO instance_probe_address(instance_id,credential_version,observed_at,ipv4_json,ipv6_json) VALUES(?,(SELECT credential_version FROM account_settings WHERE account_id='a'),?,?,NULL)", id, now.UTC().Format(time.RFC3339Nano), `["`+address+`"]`); err != nil {
			t.Fatal(err)
		}
		code, _, err := p.s.AgentGrant(ctx, id, now)
		if err != nil {
			t.Fatal(err)
		}
		registration := agentproto.Registration{Version: 1, Code: code, Nonce: strings.Repeat("a", 64), Metadata: agentproto.Metadata{InstanceID: []string{"i-test", "i-underlying"}[i], RegionID: "cn-shanghai"}, AgentVersion: "test"}
		g, err := p.s.RegisterAgent(ctx, registration, now)
		if err != nil {
			t.Fatal(err)
		}
		agents[id], tokens[id] = g.AgentID, agentproto.DeriveToken(code, registration.Nonce)
		if _, err = p.s.AcceptReport(ctx, tokens[id], agentproto.Report{Version: 1, AgentID: g.AgentID, CounterEpoch: "duplex", Sequence: 1, SampledAt: now, UptimeMS: 10000}, now); err != nil {
			t.Fatal(err)
		}
		if _, err = p.s.ClaimAgentTrace(ctx, tokens[id], agentproto.ProbeClaim{AgentID: g.AgentID, Owner: strings.Repeat("a", 64)}, now); err != nil {
			t.Fatal(err)
		}
	}
	return agents, tokens
}

func TestDuplexTraceAPIAuthStrictInputAtomicSubmissionAndReadOnlyExport(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)
	_, tokens := seedDuplexEndpoints(t, p)
	path := "/network/duplex-traces"
	if w := panelRequest(p, "POST", path, duplexBody("d"), nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "POST", path, duplexBody("d"), cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { delete(v, "reverse_port") },
		func(v map[string]any) { v["from_instance_id"] = nil },
		func(v map[string]any) { v["to_instance_id"] = "ecs" },
		func(v map[string]any) { v["forward_port"] = 0 },
		func(v map[string]any) { v["method"] = "icmp" },
		func(v map[string]any) { v["family"] = "any" },
		func(v map[string]any) { v["max_hops"] = 30; v["count"] = 5 },
		func(v map[string]any) { v["command"] = "arbitrary" },
		func(v map[string]any) { v["host"] = "1.1.1.1" },
	} {
		body := duplexBody("d")
		change(body)
		if w := panelRequest(p, "POST", path, body, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := panelRequest(p, "POST", path, duplexBody("d"), cookie, csrf)
	var a model.DuplexTraceAccepted
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &a) != nil || a.DuplexID == "" || a.Forward.JobID == a.Reverse.JobID || a.Forward.TraceID == a.Reverse.TraceID {
		t.Fatal(w.Code, w.Body)
	}
	var count int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job WHERE status='queued'").Scan(&count); err != nil || count != 2 {
		t.Fatal("HTTP executed measurement", count, err)
	}
	for _, suffix := range []string{"", "/export"} {
		if w = panelRequest(p, "GET", path+"/"+a.DuplexID+suffix, nil, nil, ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	w = panelRequest(p, "GET", path+"/"+a.DuplexID, nil, cookie, "")
	var d model.DuplexTrace
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.Forward.Status != "queued" || d.Reverse.Status != "queued" || d.From.PublicAddress != "1.1.1.1" || d.To.PublicAddress != "8.8.8.8" || d.Forward.Request.Host != "8.8.8.8" || d.Reverse.Request.Host != "1.1.1.1" {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", path+"/"+a.DuplexID+"/export", nil, cookie, "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Header().Get("Content-Disposition"), a.DuplexID) || !strings.Contains(w.Body.String(), "[forward]") || !strings.Contains(w.Body.String(), "[reverse]") {
		t.Fatal(w.Code, w.Body)
	}
	for _, secret := range tokens {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("export leaked agent token")
		}
	}
	// GETs remain observations even when cached discovery changes underneath.
	if _, err := p.s.DB.Exec("UPDATE instance_probe_address SET ipv4_json=NULL"); err != nil {
		t.Fatal(err)
	}
	if w = panelRequest(p, "GET", path+"/"+a.DuplexID, nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job WHERE status='queued'").Scan(&count); err != nil || count != 2 {
		t.Fatal("GET changed queue state", count, err)
	}
	w = panelRequest(p, "POST", path, duplexBody("d"), cookie, csrf)
	var again model.DuplexTraceAccepted
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &again) != nil || again != a {
		t.Fatal("accepted retry reevaluated addresses", w.Code, w.Body)
	}
	changed := duplexBody("d")
	changed["reverse_port"] = 443
	if w = panelRequest(p, "POST", path, changed, cookie, csrf); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "POST", path, duplexBody("n"), cookie, csrf); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	for _, suffix := range []string{"/missing", "/missing/export", "/", "/" + a.DuplexID + "/unknown", "/" + a.DuplexID + "/export/extra"} {
		if w = panelRequest(p, "GET", path+suffix, nil, cookie, ""); w.Code != 404 {
			t.Fatal(suffix, w.Code, w.Body)
		}
	}
	if w = panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "PUT", path+"/"+a.DuplexID, nil, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code)
	}
}

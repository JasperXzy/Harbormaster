package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestAgentTraceHTTPRequiresHTTPSBoundTokenAndFixedPayload(t *testing.T) {
	p := testPanel(t)
	for _, sql := range []string{
		"INSERT INTO account VALUES('a','test','china','now')",
		"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','test','cn-shanghai','running','stopped','now')",
		`INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')`,
	} {
		if _, err := p.s.DB.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	ctx, now := context.Background(), time.Now().UTC()
	grant, _, err := p.s.AgentGrant(ctx, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	registration := agentproto.Registration{Version: 1, Code: grant, Nonce: strings.Repeat("a", 64), Metadata: agentproto.Metadata{InstanceID: "i-test", RegionID: "cn-shanghai"}, AgentVersion: "test"}
	identity, err := p.s.RegisterAgent(ctx, registration, now)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(grant, registration.Nonce)
	h := NewHandler(p.s, p, nil)
	call := func(origin, path, auth string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", origin+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	claim := agentproto.ProbeClaim{AgentID: identity.AgentID, Owner: strings.Repeat("b", 64)}
	for _, path := range []string{agentproto.TraceClaimPath, agentproto.TraceResultPath} {
		for _, origin := range []string{"http://panel.example", "https://wrong.example"} {
			if w := call(origin, path, token, claim); w.Code != 403 {
				t.Fatal(w.Code, w.Body)
			}
		}
		if w := call("https://panel.example", path, "", claim); w.Code != 401 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := call("https://panel.example", agentproto.TraceClaimPath, token, map[string]any{"agent_id": claim.AgentID, "owner": claim.Owner, "command": "arbitrary"}); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	q := netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, MaxHops: 3, Count: 1, TimeoutMS: 1000}
	j, err := p.s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("t", 32), q, now)
	if err != nil {
		t.Fatal(err)
	}
	w := call("https://panel.example", agentproto.ProbeClaimPath, token, claim)
	var old agentproto.ProbeClaimed
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &old) != nil || old.Task != nil {
		t.Fatal("old agent got trace", w.Code, w.Body)
	}
	w = call("https://panel.example", agentproto.TraceClaimPath, token, claim)
	var claimed agentproto.TraceClaimed
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &claimed) != nil || claimed.Task == nil || claimed.Task.ID != j.ID || claimed.Task.Request != q {
		t.Fatal(w.Code, w.Body)
	}
	end := time.Now().UTC()
	report := agentproto.TraceReport{AgentID: identity.AgentID, TaskID: j.ID, Owner: claim.Owner, Result: netprobe.TraceResult{StartedAt: end, FinishedAt: end, Address: q.Host, ResolvedAddresses: []string{q.Host}, Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: end, Address: new(q.Host), RTTMS: new(float64(1)), Reply: new("tcp_syn_ack")}}}}}}
	for i := 0; i < 2; i++ {
		w = call("https://panel.example", agentproto.TraceResultPath, token, report)
		var ack agentproto.ProbeAck
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ack) != nil || ack.TaskID != j.ID || ack.Duplicate != (i == 1) {
			t.Fatal(w.Code, w.Body)
		}
	}
	report.Result.StopReason = strings.Repeat("x", 128<<10)
	if w = call("https://panel.example", agentproto.TraceResultPath, token, report); w.Code != 400 {
		t.Fatal("oversized result", w.Code)
	}
	if err = p.s.RevokeAgent(ctx, "ecs", time.Now()); err != nil {
		t.Fatal(err)
	}
	if w = call("https://panel.example", agentproto.TraceClaimPath, token, claim); w.Code != 401 {
		t.Fatal("revoked token", w.Code)
	}
	for _, path := range []string{agentproto.TraceClaimPath, agentproto.TraceResultPath} {
		r := httptest.NewRequest("POST", "http://demo"+path, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		(&agentAPI{s: p.s, development: true}).ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatal("demo trace enabled", w.Code)
		}
	}
}

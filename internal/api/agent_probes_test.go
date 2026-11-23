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

func TestAgentProbeHTTPRequiresBoundTokenAndFixedPayload(t *testing.T) {
	p := testPanel(t)
	for _, q := range []string{"INSERT INTO account VALUES('a','test','china','now')", "INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)", "INSERT INTO instance VALUES('ecs','a','i-test','ecs','test','cn-shanghai','running','stopped','now')", `INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')`} {
		if _, err := p.s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	ctx := context.Background()
	code, _, err := p.s.AgentGrant(ctx, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	reg := agentproto.Registration{Version: 1, Code: code, Nonce: strings.Repeat("a", 64), Metadata: agentproto.Metadata{InstanceID: "i-test", RegionID: "cn-shanghai"}, AgentVersion: "test"}
	identity, err := p.s.RegisterAgent(ctx, reg, now)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, reg.Nonce)
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
	for _, origin := range []string{"http://panel.example", "https://wrong.example"} {
		if w := call(origin, agentproto.ProbeClaimPath, token, claim); w.Code != 403 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := call("https://panel.example", agentproto.ProbeClaimPath, "", claim); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("https://panel.example", agentproto.ProbeClaimPath, token, map[string]any{"agent_id": claim.AgentID, "owner": claim.Owner, "command": "arbitrary"}); w.Code != 400 {
		t.Fatal(w.Code)
	}
	q := netprobe.Request{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, IntervalMS: 100}
	j, err := p.s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("k", 32), q, now)
	if err != nil {
		t.Fatal(err)
	}
	w := call("https://panel.example", agentproto.ProbeClaimPath, token, claim)
	var claimed agentproto.ProbeClaimed
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &claimed) != nil || claimed.Task == nil || claimed.Task.ID != j.ID {
		t.Fatal(w.Code, w.Body)
	}
	end := time.Now().UTC()
	latency := 1.0
	report := agentproto.ProbeReport{AgentID: identity.AgentID, TaskID: j.ID, Owner: claim.Owner, Result: netprobe.Result{StartedAt: end, FinishedAt: end, Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: end, RTTMS: &latency}}}}
	for n := 0; n < 2; n++ {
		w = call("https://panel.example", agentproto.ProbeResultPath, token, report)
		var ack agentproto.ProbeAck
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ack) != nil || ack.Duplicate != (n == 1) {
			t.Fatal(w.Code, w.Body)
		}
	}
	if err = p.s.RevokeAgent(ctx, "ecs", time.Now()); err != nil {
		t.Fatal(err)
	}
	if w = call("https://panel.example", agentproto.ProbeResultPath, token, report); w.Code != 401 {
		t.Fatal("revoked agent accepted", w.Code)
	}
}

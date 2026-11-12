package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestProbeAlertAPIConfigurationAndDefaultRules(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	ctx := context.Background()
	plan := model.DefaultProbePlan()
	plan.Name = "alerts"
	plan.PointKind = "server"
	plan.TargetID = "target_cloudflare_dns"
	plan.Family = "ipv4"
	plan.Method = "icmp"
	plan, err := p.s.SaveProbePlan(ctx, "", plan, p.now())
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"params": map[string]any{"probe_thresholds": []any{map[string]any{"plan_id": plan.ID, "latency_ms": 120}}, "duration_seconds": 600, "recovery_samples": 3}}
	if w := panelRequest(p, "PATCH", "/alert-rules/network_latency", payload, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "PATCH", "/alert-rules/network_latency", payload, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "PATCH", "/alert-rules/network_latency", payload, cookie, csrf)
	var rule model.AlertRule
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rule) != nil || rule.Urgent || *rule.Params.DurationSeconds != 600 || *rule.Params.RecoverySamples != 3 || len(*rule.Params.ProbeThresholds) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	for _, params := range []map[string]any{
		{"probe_thresholds": []any{}, "duration_seconds": 600},
		{"probe_thresholds": []any{map[string]any{"plan_id": plan.ID}}, "duration_seconds": 600, "recovery_samples": 3},
		{"probe_thresholds": []any{}, "duration_seconds": 0, "recovery_samples": 3},
		{"probe_thresholds": []any{}, "duration_seconds": 600, "recovery_samples": 0},
		{"probe_thresholds": []any{map[string]any{"plan_id": "missing", "latency_ms": 120}}, "duration_seconds": 600, "recovery_samples": 3},
	} {
		if w = panelRequest(p, "PATCH", "/alert-rules/network_latency", map[string]any{"params": params}, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w = panelRequest(p, "GET", "/alert-rules", nil, cookie, "")
	var list struct {
		Items []model.AlertRule `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatal(w.Code, w.Body)
	}
	found := 0
	for _, r := range list.Items {
		if !model.NetworkAlertRule(r.Key) {
			continue
		}
		found++
		if !r.Enabled || r.Urgent || *r.Params.RecoverySamples != 3 {
			t.Fatal(r)
		}
		if r.Key == "network_unreachable" && *r.Params.ConsecutiveFailures != 3 {
			t.Fatal(r)
		}
	}
	if found != 3 {
		t.Fatal(found)
	}
}

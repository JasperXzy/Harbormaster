package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestAlertRuleAPIUsesAvailableSourcesAndSharedOfflineTimeout(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	w := panelRequest(p, "GET", "/alert-rules", nil, cookie, "")
	var list struct {
		Items []model.AlertRule `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 7 {
		t.Fatal(w.Code, w.Body)
	}
	for _, rule := range list.Items {
		if rule.Key == "cdt_usage" || rule.Key == "cdt_forecast_exceed" {
			t.Fatal("unverified metering rule advertised")
		}
	}
	input := map[string]any{"params": map[string]int{"offline_after_seconds": 90}}
	if w = panelRequest(p, "PATCH", "/alert-rules/agent_offline", input, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/agent_offline", input, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/settings", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"agent_offline_after_seconds":90`) {
		t.Fatal(w.Code, w.Body)
	}
	for _, input := range []any{map[string]any{"params": map[string]int{"offline_after_seconds": 1}}, map[string]any{"params": map[string]int{"consecutive_failures": 3}}} {
		if w = panelRequest(p, "PATCH", "/alert-rules/agent_offline", input, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/cdt_usage", map[string]bool{"enabled": true}, cookie, csrf); w.Code != 404 {
		t.Fatal("unavailable rule became enabled")
	}
	for _, days := range []int{-1, 366} {
		if w = panelRequest(p, "PATCH", "/alert-rules/instance_expiring", map[string]any{"params": map[string]int{"before_days": days}}, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/instance_expiring", map[string]any{"params": map[string]int{"before_days": 3}}, cookie, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), `"before_days":3`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/account_api_failure", map[string]any{"params": map[string]int{"consecutive_failures": 3, "before_days": 7}}, cookie, csrf); w.Code != 422 {
		t.Fatal("expiry params accepted on another rule", w.Code)
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/spec_bandwidth_insufficient", map[string]any{"params": map[string]int{}}, cookie, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), `"urgent":false`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/spec_bandwidth_insufficient", map[string]any{"params": map[string]int{"consecutive_failures": 3}}, cookie, csrf); w.Code != 422 {
		t.Fatal("parameterless rule accepted a threshold", w.Code)
	}
	if w = panelRequest(p, "PATCH", "/alert-rules/instance_public_ip_changed", map[string]any{"enabled": false, "params": map[string]int{}}, cookie, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), `"urgent":false`) {
		t.Fatal("IP rule cannot be disabled", w.Code, w.Body)
	}
	for _, key := range []string{"before_days", "consecutive_failures", "offline_after_seconds"} {
		if w = panelRequest(p, "PATCH", "/alert-rules/instance_public_ip_changed", map[string]any{"params": map[string]int{key: 1}}, cookie, csrf); w.Code != 422 {
			t.Fatal("IP rule accepted an unrelated threshold", key, w.Code)
		}
	}
}

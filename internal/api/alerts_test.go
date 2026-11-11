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
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 3 {
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
}

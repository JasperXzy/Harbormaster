package api

import (
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
)

func TestReconciliationHistoryAuthenticationAndValidation(t *testing.T) {
	p := testPanel(t)
	if w := panelRequest(p, "GET", "/traffic/reconciliations", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	w := panelRequest(p, "GET", "/alert-rules?include_reconciliation=true", nil, cookie, "")
	var list struct {
		Items []model.AlertRule `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 13 {
		t.Fatal(w.Code, w.Body)
	}
	for _, r := range list.Items {
		if r.Key == "cdt_estimation_deviation" && (!r.Enabled || r.Params.RecoverySamples == nil || *r.Params.RecoverySamples != 3) {
			t.Fatal("confirmed recovery policy missing")
		}
	}

	for _, path := range []string{"/traffic/reconciliations", "/traffic/reconciliations?account_id=missing&scope=overseas&limit=1"} {
		if w := panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, query := range []string{"scope=swas", "before=0", "before=oops", "limit=201", "limit=oops"} {
		if w := panelRequest(p, "GET", "/traffic/reconciliations?"+query, nil, cookie, ""); w.Code != 422 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	if w := panelRequest(p, "PATCH", "/alert-rules/cdt_estimation_deviation", map[string]any{"enabled": true, "params": map[string]int{"consecutive_failures": 3}}, cookie, csrf); w.Code != 422 {
		t.Fatal("enabled without recovery policy", w.Code, w.Body)
	}
	if w := panelRequest(p, "PATCH", "/alert-rules/cdt_estimation_deviation", map[string]any{"enabled": true, "params": map[string]int{"consecutive_failures": 3, "recovery_samples": 3}}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}

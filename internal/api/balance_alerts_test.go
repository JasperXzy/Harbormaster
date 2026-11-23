package api

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestBalanceAlertConfigurationValidationAndAccountScope(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box, Interval: time.Minute}
	a, _, err := p.engine.CreateAccount(context.Background(), model.AccountCreate{Name: "balance fixture", Site: "china", AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "fixture-balance-secret-never-real", RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"account_id": a.ID, "currency": "CNY", "amount": "0.100000000001"}
	input := map[string]any{"params": map[string]any{"balance_thresholds": []any{entry}}}
	if w := panelRequest(p, "PATCH", "/alert-rules/balance_low", input, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "PATCH", "/alert-rules/balance_low", input, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "PATCH", "/alert-rules/balance_low", input, cookie, csrf)
	var rule model.AlertRule
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rule) != nil || rule.Urgent || rule.Params.BalanceThresholds == nil || (*rule.Params.BalanceThresholds)[0].Amount != "0.100000000001" {
		t.Fatal(w.Code, w.Body)
	}
	for _, change := range []map[string]any{
		{"account_id": "missing"}, {"currency": "cny"}, {"currency": "US"}, {"amount": 0.1}, {"amount": "1e2"}, {"amount": "1/3"}, {"amount": "0.0000000000001"}, {"amount": strings.Repeat("9", 80)},
	} {
		bad := map[string]any{"account_id": a.ID, "currency": "CNY", "amount": "10"}
		for key, value := range change {
			bad[key] = value
		}
		if w := panelRequest(p, "PATCH", "/alert-rules/balance_low", map[string]any{"params": map[string]any{"balance_thresholds": []any{bad}}}, cookie, csrf); w.Code != 422 {
			t.Fatal("invalid threshold accepted", change, w.Code, w.Body)
		}
	}
	for _, params := range []any{map[string]any{}, map[string]any{"balance_thresholds": nil}, map[string]any{"balance_thresholds": []any{entry, entry}}, map[string]any{"balance_thresholds": []any{entry}, "before_days": 1}} {
		if w := panelRequest(p, "PATCH", "/alert-rules/balance_low", map[string]any{"params": params}, cookie, csrf); w.Code != 422 {
			t.Fatal("invalid balance parameters accepted", w.Code, w.Body)
		}
	}
	if w := panelRequest(p, "PATCH", "/alert-rules/spec_bandwidth_insufficient", input, cookie, csrf); w.Code != 422 {
		t.Fatal("balance targets accepted on another rule", w.Code)
	}
	// Failed updates leave the prior exact threshold intact.
	rules, err := p.s.AlertRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.Key == "balance_low" && (*r.Params.BalanceThresholds)[0].Amount != "0.100000000001" {
			t.Fatal("invalid write changed saved rule", r)
		}
	}
	w = panelRequest(p, "PATCH", "/alert-rules/balance_low", map[string]any{"params": map[string]any{"balance_thresholds": []any{}}}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"balance_thresholds":[]`) {
		t.Fatal("cannot clear targets", w.Code, w.Body)
	}
}

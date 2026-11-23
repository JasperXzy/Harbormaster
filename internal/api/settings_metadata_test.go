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

func TestSettingsPatchAndInstanceMetadataLocalOnly(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}

	w := panelRequest(p, "GET", "/settings", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"agent_raw_days":7`) || !strings.Contains(w.Body.String(), `"agent_hourly_months":13`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = panelRequest(p, "PATCH", "/settings", map[string]any{"business_timezone": "Asia/Tokyo"}, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	if w = panelRequest(p, "PATCH", "/settings", map[string]any{"business_timezone": "Invalid/Zone"}, cookie, csrf); w.Code != 422 {
		t.Fatal("invalid timezone accepted", w.Code)
	}
	if w = panelRequest(p, "PATCH", "/settings", map[string]any{"retention": map[string]any{"agent_raw_days": 1}}, cookie, csrf); w.Code != 422 {
		t.Fatal("too small raw retention accepted", w.Code)
	}
	w = panelRequest(p, "PATCH", "/settings", map[string]any{
		"business_timezone": "Asia/Tokyo",
		"retention": map[string]any{
			"latency_probe_days":  45,
			"agent_raw_days":      14,
			"agent_hourly_months": 24,
			"audit_log_days":      365,
			"job_history_days":    120,
		},
	}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"business_timezone":"Asia/Tokyo"`) || !strings.Contains(w.Body.String(), `"agent_raw_days":14`) {
		t.Fatal(w.Code, w.Body.String())
	}

	// Create account + seed two instances via scanner
	in := map[string]any{"name": "meta-acc", "site": "china", "access_key_id": "MetaAccessKeyID12345", "access_key_secret": "synthetic-secret-value-not-real", "region_ids": []string{"cn-shanghai"}}
	w = panelRequest(p, "POST", "/accounts", in, cookie, csrf)
	var created struct {
		Account model.Account `json:"account"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := p.engine.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", "/instances", nil, cookie, "")
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) == 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	instID := list.Items[0]["id"].(string)
	if list.Items[0]["note"] != "" || list.Items[0]["group"] != nil {
		t.Fatal("unexpected default metadata", list.Items[0])
	}

	callsBefore := scanner.calls
	w = panelRequest(p, "PATCH", "/instances/"+instID+"/metadata", map[string]any{
		"note":  "主力转发入口\n请勿随意停机",
		"group": "核心入口",
		"tags":  []string{"转发", "CN2", "转发"},
	}, cookie, csrf)
	var meta model.InstanceMetadata
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &meta) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if meta.Note != "主力转发入口\n请勿随意停机" || meta.Group == nil || *meta.Group != "核心入口" || len(meta.Tags) != 2 || scanner.calls != callsBefore {
		t.Fatal("metadata mismatch or cloud call triggered", meta, scanner.calls)
	}

	w = panelRequest(p, "GET", "/instances?group=%E6%A0%B8%E5%BF%83%E5%85%A5%E5%8F%A3&tag=CN2", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0]["id"] != instID {
		t.Fatal("filtered instances mismatch", w.Code, w.Body.String())
	}
	w = panelRequest(p, "GET", "/instances?group=missing", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 0 {
		t.Fatal("expected empty group filter", list.Items)
	}

	// Verify audit recorded Chinese summary and before/after values
	audits, err := p.s.AuditLog(context.Background(), model.AuditFilter{Limit: 10})
	if err != nil || len(audits) < 2 {
		t.Fatal(err, audits)
	}
	if audits[0].Action != "instance_metadata_updated" || audits[0].Summary != "修改实例备注、分组与标签" || audits[0].Details.After["group"] != "核心入口" {
		t.Fatal("unexpected metadata audit", audits[0])
	}
}

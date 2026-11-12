package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
)

func TestCPUReadsAgeCachedSamplesWithoutReplacingSourceTimes(t *testing.T) {
	p := testPanel(t)
	p.engine = &collect.Engine{Store: p.s}
	cookie, _ := setupPanel(t, p)
	now := p.now().UTC()
	checked, observed := now.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(-11*time.Minute).Format(time.RFC3339Nano)
	payload := map[string]any{"id": "i", "cpu": map[string]any{"usage": map[string]any{"status": "ok", "value": 0, "observed_at": observed, "checked_at": checked}, "credit_balance": map[string]any{"status": "unknown", "value": nil, "observed_at": nil, "checked_at": checked}}}
	raw, _ := json.Marshal(payload)
	if _, err := p.s.DB.Exec("INSERT INTO account VALUES('a','test','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','ecs','i','cn-shanghai',?,1,'now')", string(raw)); err != nil {
		t.Fatal(err)
	}
	w := panelRequest(p, "GET", "/instances", nil, cookie, "")
	var result struct {
		Items []map[string]any `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 1 {
		t.Fatal(w.Code, w.Body)
	}
	cpu := result.Items[0]["cpu"].(map[string]any)
	usage := cpu["usage"].(map[string]any)
	credits := cpu["credit_balance"].(map[string]any)
	if usage["status"] != "stale" || usage["value"] != float64(0) || usage["observed_at"] != observed || usage["checked_at"] != checked || credits["value"] != nil {
		t.Fatal(cpu)
	}
	var persisted string
	if err := p.s.DB.QueryRow("SELECT json_extract(payload_json,'$.cpu.usage.status') FROM resource_snapshot WHERE id='i'").Scan(&persisted); err != nil || persisted != "ok" {
		t.Fatal("read rewrote stored sample", persisted, err)
	}
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("read enqueued cloud calls", jobs, err)
	}
}

package api

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func networkTargetBody() map[string]any {
	return map[string]any{"revision": 0, "name": "custom", "kind": "custom", "host": "1.1.1.1", "instance_id": nil, "tcp_port": 443, "enabled": true}
}
func TestNetworkManagementAPIAuthDefaultsPaginationAndNoSynchronousWork(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)
	for _, path := range []string{"/network/targets", "/network/points", "/network/plans"} {
		if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	if w := panelRequest(p, "POST", "/network/targets", networkTargetBody(), cookie, ""); w.Code != 403 {
		t.Fatal("csrf bypass", w.Code)
	}
	w := panelRequest(p, "POST", "/network/targets", networkTargetBody(), cookie, csrf)
	var target model.NetworkTarget
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &target) != nil || target.Revision != 1 {
		t.Fatal(w.Code, w.Body)
	}
	body := map[string]any{"revision": 0, "name": "local", "point_kind": "server", "instance_id": nil, "target_id": target.ID, "family": "ipv4", "method": "tcp", "enabled": true}
	w = panelRequest(p, "POST", "/network/plans", body, cookie, csrf)
	var plan model.ProbePlan
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &plan) != nil || plan.FrequencySeconds != 300 || plan.Count != 5 || plan.TimeoutMS != 2000 || plan.IntervalMS != 1000 || plan.LastRun != nil {
		t.Fatal(w.Code, w.Body)
	}
	for _, path := range []string{"/network/targets/" + target.ID, "/network/plans/" + plan.ID, "/network/points", "/network/plans"} {
		if w = panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("HTTP configured or read endpoint probed", jobs, err)
	}
	w = panelRequest(p, "GET", "/network/targets?limit=2", nil, cookie, "")
	var page struct {
		Items []model.NetworkTarget `json:"items"`
		Next  *string               `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 2 || page.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	next := url.QueryEscape(*page.Next)
	if w = panelRequest(p, "GET", "/network/plans?cursor="+next, nil, cookie, ""); w.Code != 422 {
		t.Fatal("cross-list cursor", w.Code)
	}
	if w = panelRequest(p, "GET", "/network/targets?limit=2&cursor="+next, nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "DELETE", "/network/targets/"+target.ID, map[string]any{"revision": 1}, cookie, csrf); w.Code != 409 {
		t.Fatal("referenced target removed", w.Code)
	}
	body["revision"] = 1
	body["enabled"] = false
	if w = panelRequest(p, "PUT", "/network/plans/"+plan.ID, body, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "PUT", "/network/plans/"+plan.ID, body, cookie, csrf); w.Code != 409 {
		t.Fatal("stale revision", w.Code)
	}
	// Explicitly enable the replacement and run only the scheduler, never a probe.
	body["revision"] = 2
	body["enabled"] = true
	if w = panelRequest(p, "PUT", "/network/plans/"+plan.ID, body, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := p.s.ScheduleProbePlans(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	path := "/network/plans/" + plan.ID + "/runs?" + url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}}.Encode()
	w = panelRequest(p, "GET", path, nil, cookie, "")
	var runs struct {
		Items []model.ProbeRun `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &runs) != nil || len(runs.Items) != 1 || runs.Items[0].Status != "queued" || runs.Items[0].Result != nil {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", "/network/plans/"+plan.ID+"/runs", nil, cookie, ""); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "DELETE", "/network/plans/"+plan.ID, map[string]any{"revision": 3}, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 200 {
		t.Fatal("deleted plan history lost", w.Code, w.Body)
	}
	if w = panelRequest(p, "DELETE", "/network/targets/"+target.ID, map[string]any{"revision": 1}, cookie, csrf); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestNetworkManagementRejectsUnknownAndInvalidFields(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	for _, mutate := range []func(map[string]any){func(v map[string]any) { delete(v, "enabled") }, func(v map[string]any) { v["enabled"] = nil }, func(v map[string]any) { v["host"] = "http://example.com" }, func(v map[string]any) { v["host"] = "100.100.100.200" }, func(v map[string]any) { v["command"] = "anything" }, func(v map[string]any) { v["id"] = "overridden" }, func(v map[string]any) { v["tcp_port"] = 65536 }, func(v map[string]any) { v["kind"] = "instance" }} {
		body := networkTargetBody()
		mutate(body)
		w := panelRequest(p, "POST", "/network/targets", body, cookie, csrf)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, field := range []string{"frequency_seconds", "count", "timeout_ms", "interval_ms"} {
		body := map[string]any{"revision": 0, "name": "bad", "point_kind": "server", "instance_id": nil, "target_id": "target_cloudflare_dns", "family": "ipv4", "method": "icmp", "enabled": false, field: 0}
		w := panelRequest(p, "POST", "/network/plans", body, cookie, csrf)
		if w.Code != 422 {
			t.Fatal(field, w.Code, w.Body)
		}
	}
}

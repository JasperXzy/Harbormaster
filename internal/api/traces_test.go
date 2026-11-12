package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func traceBody(key string) map[string]any {
	return map[string]any{"request_key": strings.Repeat(key, 32), "point_kind": "server", "instance_id": nil, "target_id": nil, "target_revision": nil, "host": "trace.invalid", "family": "ipv4", "method": "icmp", "port": 0}
}
func traceHistoryURL(from, to time.Time) string {
	return "/network/traces?" + url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}.Encode()
}
func TestTracePanelSubmissionHistoryDetailAndTextExport(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)
	for _, path := range []string{"/network/traces", "/network/traces/1", "/network/traces/1/export"} {
		if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
			t.Fatal("anonymous trace data", path, w.Code)
		}
	}
	if w := panelRequest(p, "POST", "/network/traces", traceBody("a"), cookie, ""); w.Code != 403 {
		t.Fatal("csrf bypass", w.Code)
	}
	w := panelRequest(p, "POST", "/network/traces", traceBody("a"), cookie, csrf)
	var a model.TraceAccepted
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &a) != nil || a.JobID == "" || a.TraceID == "" {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/network/traces/"+a.TraceID, nil, cookie, "")
	var d model.TraceDetail
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.Status != "queued" || d.Result != nil || d.Outcome != nil || d.Request.MaxHops != 30 || d.Request.Count != 3 || d.Request.TimeoutMS != 1000 {
		t.Fatal(w.Code, w.Body)
	}
	var status string
	if err := p.s.DB.QueryRow("SELECT status FROM job WHERE id=?", a.JobID).Scan(&status); err != nil || status != "queued" {
		t.Fatal("HTTP synchronously executed a probe", status, err)
	}
	claim, err := p.s.ClaimServerTrace(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	result := netprobe.TraceResult{StartedAt: now, FinishedAt: now.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{}}}}
	for i := 1; i <= 3; i++ {
		result.Hops[0].Samples = append(result.Hops[0].Samples, netprobe.TraceSample{Sequence: i, StartedAt: now, Address: new("1.1.1.1"), RTTMS: new(float64(i)), Reply: new("echo_reply"), ICMPType: new(0), ICMPCode: new(0)})
	}
	if err = p.s.FinishServerTrace(context.Background(), claim, result, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	w = panelRequest(p, "GET", "/network/traces/"+a.TraceID, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.Result == nil || !d.Result.Reached || d.Outcome.SampleCount != 3 {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/network/traces/"+a.TraceID+"/export", nil, cookie, "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Header().Get("Content-Disposition"), "trace-"+a.TraceID+".txt") || !strings.Contains(w.Body.String(), "1 3 1.1.1.1 3.000 echo_reply") {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), claim.Job.Owner) {
		t.Fatal("export leaked lease owner")
	}
	w = panelRequest(p, "POST", "/network/traces", traceBody("a"), cookie, csrf)
	var repeated model.TraceAccepted
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &repeated) != nil || repeated != a {
		t.Fatal("retry not idempotent", w.Code, w.Body)
	}
	changed := traceBody("a")
	changed["count"] = 1
	if w = panelRequest(p, "POST", "/network/traces", changed, cookie, csrf); w.Code != 409 {
		t.Fatal("changed retry", w.Code)
	}
	if w = panelRequest(p, "POST", "/network/traces", traceBody("b"), cookie, csrf); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	now = now.Add(time.Second)
	path := traceHistoryURL(now.Add(-time.Hour), now) + "&limit=1"
	w = panelRequest(p, "GET", path, nil, cookie, "")
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
		Next  *string                      `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	if _, ok := page.Items[0]["result"]; ok {
		t.Fatal("list returned raw packets")
	}
	if w = panelRequest(p, "GET", path+"&cursor="+url.QueryEscape(*page.Next)+"&point_id=changed", nil, cookie, ""); w.Code != 422 {
		t.Fatal("cross-filter cursor", w.Code)
	}
	w = panelRequest(p, "GET", path+"&cursor="+url.QueryEscape(*page.Next), nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next != nil || string(page.Items[0]["id"]) != `"`+a.TraceID+`"` {
		t.Fatal(w.Code, w.Body)
	}
	for _, path := range []string{"/network/traces/unknown", "/network/traces/99999/export", "/network/traces/1/extra", "/network/traces/"} {
		if w = panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	if w = panelRequest(p, "GET", "/network/traces", nil, cookie, ""); w.Code != 422 {
		t.Fatal("missing range", w.Code)
	}
	if w = panelRequest(p, "GET", traceHistoryURL(now.Add(-32*24*time.Hour), now), nil, cookie, ""); w.Code != 422 {
		t.Fatal("unbounded range", w.Code)
	}
	if w = panelRequest(p, "GET", traceHistoryURL(now, now.Add(time.Hour)), nil, cookie, ""); w.Code != 422 {
		t.Fatal("future range", w.Code)
	}
}

func TestTracePanelRejectsInvalidInputsAndUnavailableTargets(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "host") }, func(v map[string]any) { v["request_key"] = nil }, func(v map[string]any) { v["count"] = 0 }, func(v map[string]any) { v["max_hops"] = 31 }, func(v map[string]any) { v["timeout_ms"] = 2001 },
		func(v map[string]any) { v["count"] = 5; v["timeout_ms"] = 1000 }, func(v map[string]any) { v["port"] = 443 }, func(v map[string]any) { v["method"] = "tcp" }, func(v map[string]any) { v["method"] = "mtr" },
		func(v map[string]any) { v["host"] = "100.100.100.200" }, func(v map[string]any) { v["host"] = "http://example.com" }, func(v map[string]any) { v["family"] = "auto" }, func(v map[string]any) { v["host"] = "1.1.1.1"; v["family"] = "ipv6" },
		func(v map[string]any) { v["command"] = "arbitrary" }, func(v map[string]any) { v["instance_id"] = "ecs" }, func(v map[string]any) { v["point_kind"] = "agent" }, func(v map[string]any) { v["target_id"] = "target_cloudflare_dns"; v["target_revision"] = 1 },
	} {
		body := traceBody("a")
		mutate(body)
		w := panelRequest(p, "POST", "/network/traces", body, cookie, csrf)
		if w.Code != 422 {
			t.Fatal("invalid trace accepted", body, w.Code, w.Body)
		}
	}
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM network_trace_run").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal(jobs, err)
	}
	body := traceBody("b")
	body["host"] = nil
	body["target_id"] = "target_cloudflare_dns"
	body["target_revision"] = 99
	if w := panelRequest(p, "POST", "/network/traces", body, cookie, csrf); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	target, err := p.s.NetworkTarget(context.Background(), "target_cloudflare_dns")
	if err != nil {
		t.Fatal(err)
	}
	target.Enabled = false
	target, err = p.s.SaveNetworkTarget(context.Background(), target.ID, target, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body["target_revision"] = target.Revision
	if w := panelRequest(p, "POST", "/network/traces", body, cookie, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "target_disabled") {
		t.Fatal(w.Code, w.Body)
	}
	body = traceBody("c")
	body["point_kind"] = "agent"
	body["instance_id"] = "missing"
	if w := panelRequest(p, "POST", "/network/traces", body, cookie, csrf); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
}

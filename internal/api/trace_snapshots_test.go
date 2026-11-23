package api

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func snapshotURL(id string, from, to time.Time) string {
	return "/network/plans/" + id + "/trace-snapshots?" + url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}.Encode()
}
func TestTraceSnapshotAPIConfigurationAuthHistoryAndReadOnlyGET(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC().Truncate(time.Minute)
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)
	in := model.DefaultProbePlan()
	in.Name = "snapshots"
	in.PointKind = "server"
	in.TargetID = "target_cloudflare_dns"
	in.Family = "ipv4"
	in.Method = "icmp"
	in.Enabled = true
	plan, err := p.s.SaveProbePlan(context.Background(), "", in, now)
	if err != nil {
		t.Fatal(err)
	}
	path := "/network/plans/" + plan.ID + "/trace-schedule"
	body := map[string]any{"revision": 0, "enabled": true, "notify_changes": false}
	if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "PUT", path, body, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "GET", path, nil, cookie, "")
	var spec model.TraceSchedule
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &spec) != nil || spec.Revision != 0 || spec.Enabled || spec.Active || spec.NextRunAt != nil || spec.TimeOfDay != "03:00" {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "PUT", path, body, cookie, csrf)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &spec) != nil || spec.Revision != 1 || !spec.Active || spec.Count != 3 || !spec.NextRunAt.After(now) {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "PUT", path, body, cookie, csrf); w.Code != 409 {
		t.Fatal("stale revision", w.Code, w.Body)
	}
	for _, invalid := range []map[string]any{
		{"revision": 1, "enabled": true},
		{"revision": 1, "enabled": nil, "notify_changes": false},
		{"revision": 1, "enabled": true, "notify_changes": false, "time_of_day": "24:00"},
		{"revision": 1, "enabled": true, "notify_changes": false, "count": 5},
		{"revision": 1, "enabled": true, "notify_changes": false, "command": "arbitrary"},
	} {
		if w = panelRequest(p, "PUT", path, invalid, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		body = map[string]any{"revision": spec.Revision, "enabled": true, "notify_changes": false, "time_of_day": now.Add(time.Minute).In(loc).Format("15:04")}
		w = panelRequest(p, "PUT", path, body, cookie, csrf)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &spec) != nil {
			t.Fatal(w.Code, w.Body)
		}
		now = *spec.NextRunAt
		if err = p.s.ScheduleTraceSnapshots(context.Background(), p.timezone, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	history := snapshotURL(plan.ID, now.Add(-time.Hour), now) + "&limit=1"
	w = panelRequest(p, "GET", history, nil, cookie, "")
	var page struct {
		Items []model.TraceSnapshot `json:"items"`
		Next  *string               `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == nil || page.Items[0].TraceID == nil {
		t.Fatal(w.Code, w.Body)
	}
	second := page.Items[0]
	var pending int
	if err = p.s.DB.QueryRow("SELECT count(*) FROM trace_snapshot_run WHERE comparison_json IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatal("GET evaluated pending comparison", pending, err)
	}
	w = panelRequest(p, "GET", history+"&cursor="+url.QueryEscape(*page.Next), nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next != nil || page.Items[0].ID == second.ID {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", snapshotURL(plan.ID, now.Add(-32*24*time.Hour), now), nil, cookie, ""); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "GET", snapshotURL("unknown", now.Add(-time.Hour), now), nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "POST", path, body, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "GET", history+"&cursor=invalid", nil, cookie, ""); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "GET", "/alert-rules", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var rules struct {
		Items []model.AlertRule `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &rules)
	found := false
	for _, rule := range rules.Items {
		if rule.Key == "network_route_change" {
			found = true
			if rule.Urgent || len(rule.Validate()) != 0 {
				t.Fatal(rule)
			}
		}
	}
	if !found {
		t.Fatal("route event rule absent", w.Body)
	}
}

func TestTraceScheduleCannotEnableDisabledProbePlan(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	in := model.DefaultProbePlan()
	in.Name = "disabled"
	in.PointKind = "server"
	in.TargetID = "target_cloudflare_dns"
	in.Family = "ipv4"
	in.Method = "icmp"
	plan, err := p.s.SaveProbePlan(context.Background(), "", in, p.now())
	if err != nil {
		t.Fatal(err)
	}
	w := panelRequest(p, "PUT", "/network/plans/"+plan.ID+"/trace-schedule", map[string]any{"revision": 0, "enabled": true, "notify_changes": false}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}

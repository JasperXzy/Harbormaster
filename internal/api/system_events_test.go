package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestSystemEventAPIReadsHistorySourcesAndStablePagination(t *testing.T) {
	p := testPanel(t)
	if w := panelRequest(p, "GET", "/system-events", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	ctx := context.Background()
	now := p.now().UTC()
	w := panelRequest(p, "GET", "/system-events", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"sources":[]`) {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := p.s.DB.Exec("INSERT INTO account VALUES('a','test','china','now')"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(model.Account{ID: "a", Enabled: true, RegionIDs: []string{"cn-shanghai"}})
	if _, err := p.s.DB.Exec("INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a',?,1)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec("INSERT INTO instance VALUES('local','a','i-test','ecs','test','cn-shanghai','running','stopped','now')"); err != nil {
		t.Fatal(err)
	}
	j, err := p.s.EnqueueJob(ctx, "refresh", "system-events:a", []string{"a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	j, err = p.s.ClaimCollectionJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.s.BeginAccountScan(ctx, "a", j.Owner, now)
	if err != nil {
		t.Fatal(err)
	}
	events := []model.CloudSystemEvent{{CloudEventID: "old", CloudInstanceID: "i-test", Category: "maintenance", EventType: "SystemMaintenance.Reboot", ProviderStatus: "Scheduled"}, {CloudEventID: "new", CloudInstanceID: "i-unknown", Category: "spot_interruption", EventType: "Instance:PreemptibleInstanceInterruption", ProviderStatus: "Executed"}}
	if err = p.s.SaveSystemEvents(ctx, j, a, []model.SystemEventObservation{{AccountID: "a", RegionID: "cn-shanghai", Source: "ecs_history", FetchedAt: now, Events: events}}, now); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items   []model.SystemEvent       `json:"items"`
		Sources []model.SystemEventSource `json:"sources"`
		Next    *string                   `json:"next_cursor"`
	}
	w = panelRequest(p, "GET", "/system-events?limit=1", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].CloudEventID != "new" || page.Items[0].InstanceID != nil || len(page.Sources) != 1 || page.Sources[0].Status != "ok" || page.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/system-events?cursor="+*page.Next, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].InstanceID == nil || *page.Items[0].InstanceID != "local" {
		t.Fatal(w.Code, w.Body)
	}
	for _, query := range []string{"source=invalid", "category=invalid", "region_id=bad/region", "limit=201", "limit=0", "cursor=bad!", "cursor=MA"} {
		if w := panelRequest(p, "GET", "/system-events?"+query, nil, cookie, ""); w.Code != 422 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"instance_id=local", 1}, {"category=spot_interruption", 1}, {"source=cms_spot", 0}, {"account_id=missing", 0}, {"region_id=cn-hongkong", 0}} {
		w = panelRequest(p, "GET", "/system-events?"+tc.query, nil, cookie, "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != tc.count {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
	if w := panelRequest(p, "POST", "/system-events", map[string]any{}, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code)
	}
	p.now = func() time.Time { return now.Add(3 * time.Minute) }
	w = panelRequest(p, "GET", "/system-events", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Sources[0].Status != "stale" {
		t.Fatal(w.Code, w.Body)
	}
	var count int
	if err = p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&count); err != nil || count != 1 {
		t.Fatal("GET enqueued cloud work", count, err)
	}
}

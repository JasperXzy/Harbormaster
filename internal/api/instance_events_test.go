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

type eventScanner struct{ apiScanner }

func (s *eventScanner) Scan(ctx context.Context, a model.Account, credential model.Credential, permissions bool, before func() error) model.ScanResult {
	result := s.apiScanner.Scan(ctx, a, credential, permissions, before)
	if s.calls > 1 && len(result.Snapshots) > 0 {
		result.Snapshots[0].Payload["status"] = "stopped"
	}
	return result
}

func TestInstanceEventAPIAuthenticationFiltersAndStablePagination(t *testing.T) {
	p := testPanel(t)
	ctx := context.Background()
	if w := panelRequest(p, "GET", "/instance-events", nil, nil, ""); w.Code != 401 {
		t.Fatal("anonymous event history exposed", w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	box, err := security.NewBox(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &eventScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	account, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{Name: "events", Site: "china", AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "test-secret-for-events", RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.engine.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []model.InstanceEvent `json:"items"`
		Next  *string               `json:"next_cursor"`
	}
	w := panelRequest(p, "GET", "/instance-events?limit=1", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	firstID, firstInstance, cursor := page.Items[0].ID, page.Items[0].InstanceID, *page.Next
	if _, err := p.engine.Enqueue(ctx, "refresh", account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.engine.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", "/instance-events?limit=1&cursor="+cursor, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next != nil || page.Items[0].ID == firstID || page.Items[0].Kind != "discovered" {
		t.Fatal("new event disturbed older cursor", w.Code, w.Body)
	}
	for _, query := range []string{"account_id=" + account.ID, "instance_id=" + firstInstance, "account_id=" + account.ID + "&instance_id=" + firstInstance} {
		w = panelRequest(p, "GET", "/instance-events?"+query, nil, cookie, "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) == 0 {
			t.Fatal(query, w.Code, w.Body)
		}
		for _, event := range page.Items {
			if event.AccountID != account.ID || (strings.Contains(query, "instance_id=") && event.InstanceID != firstInstance) {
				t.Fatal("event filter ignored", query, event)
			}
		}
	}
	for _, query := range []string{"account_id=missing", "instance_id=missing", "account_id=missing&instance_id=" + firstInstance} {
		w = panelRequest(p, "GET", "/instance-events?"+query, nil, cookie, "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 || page.Next != nil {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=abc", "cursor=bad!", "cursor=MA", "cursor=LTE", "cursor=OTIyMzM3MjAzNjg1NDc3NTgwOA"} {
		if w := panelRequest(p, "GET", "/instance-events?"+query, nil, cookie, ""); w.Code != 422 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	if w := panelRequest(p, "POST", "/instance-events", map[string]any{}, cookie, csrf); w.Code != 405 {
		t.Fatal("event creation exposed as browser write", w.Code)
	}
	if scanner.calls != 2 {
		t.Fatal("history reads triggered cloud calls", scanner.calls)
	}
	w = panelRequest(p, "GET", "/instance-events", nil, cookie, "")
	for _, forbidden := range []string{"test-secret-for-events", "TestAccessKeyID123456", "sequence", "lease_owner", "value_json"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("event response exposed internal data", forbidden)
		}
	}
}

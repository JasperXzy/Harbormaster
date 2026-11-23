package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestJobHistoryAPIIsReadOnlyFilteredAndStable(t *testing.T) {
	p := testPanel(t)
	if w := panelRequest(p, "GET", "/jobs", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := p.s.EnqueueJob(model.WithJobTrigger(ctx, "panel"), "refresh", "first", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.s.EnqueueJob(model.WithJobTrigger(ctx, "cli"), "permission_check", "second", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []model.Job `json:"items"`
		Next  *string     `json:"next_cursor"`
	}
	w := panelRequest(p, "GET", "/jobs?limit=1", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	cursor := *page.Next
	for _, private := range []string{"sequence", "scope", "lease_owner", "attempts"} {
		if strings.Contains(w.Body.String(), `"`+private+`"`) {
			t.Fatal("private job data leaked", private)
		}
	}
	if _, err := p.s.EnqueueJob(ctx, "refresh", "third", nil, now); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", "/jobs?limit=1&cursor="+cursor, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.Next != nil {
		t.Fatal("page shifted after enqueue", w.Code, w.Body)
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"trigger=panel", 1}, {"type=permission_check&status=queued&trigger=cli", 1}, {"account_id=missing", 0}, {"instance_id=missing", 0}, {"status=failed", 0},
	} {
		w = panelRequest(p, "GET", "/jobs?"+tc.query, nil, cookie, "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != tc.count {
			t.Fatal(tc.query, w.Code, w.Body)
		}
	}
	for _, query := range []string{"limit=201", "limit=0", "cursor=bad!", "cursor=MA", "status=invalid", "type=stop", "trigger=forged"} {
		if w := panelRequest(p, "GET", "/jobs?"+query, nil, cookie, ""); w.Code != 422 {
			t.Fatal(query, w.Code)
		}
	}
	w = panelRequest(p, "GET", "/jobs/"+first.ID, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"trigger":"panel"`) || !strings.Contains(w.Body.String(), `"instance_id":null`) {
		t.Fatal("detail metadata inconsistent", w.Code, w.Body)
	}
	if w := panelRequest(p, "GET", "/jobs/missing", nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := panelRequest(p, "POST", "/jobs", map[string]any{}, cookie, csrf); w.Code != 405 {
		t.Fatal("history endpoint accepts writes", w.Code)
	}
	var count int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&count); err != nil || count != 3 {
		t.Fatal("reads created work", count, err)
	}
}

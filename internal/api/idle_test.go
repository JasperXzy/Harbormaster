package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

type idleAPIScanner struct {
	calls int
	at    time.Time
}

func (s *idleAPIScanner) Scan(_ context.Context, a model.Account, _ model.Credential, _ bool, before func() error) model.ScanResult {
	s.calls++
	if err := before(); err != nil {
		return model.ScanResult{Failure: "account_changed"}
	}
	items := []model.IdleCandidate{}
	for _, id := range []string{"eip-alpha", "eip-beta"} {
		items = append(items, model.IdleCandidate{ID: a.ID + "_eip_cn-shanghai_" + id, AccountID: a.ID, RegionID: "cn-shanghai", Kind: "eip", CloudID: id, Name: id, Reason: "unbound_eip", ChargeType: "pay_as_you_go", CloudStatus: "Available", InstanceIDs: []string{}, ObservedAt: s.at})
	}
	return model.ScanResult{Idle: []model.IdleObservation{{Region: "cn-shanghai", Kind: "eip", QueriedAt: s.at, Data: &model.IdleData{Scanned: 3, UnclassifiedIDs: []string{"eip-unknown"}, Candidates: items}}}}
}

func TestIdleAPIReadsCoverageAndPaginatedCacheWithoutCloud(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	scanner := &idleAPIScanner{at: now}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	a, _, err := p.engine.CreateAccount(context.Background(), model.AccountCreate{Name: "idle fixture", Site: "china", AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "idle-fixture-secret-not-real", RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := panelRequest(p, "GET", "/idle-resources", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	w := panelRequest(p, "GET", "/idle-resources", nil, cookie, "")
	var page struct {
		Items   []model.IdleCandidate `json:"items"`
		Sources []model.IdleSource    `json:"sources"`
		Next    *string               `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 || len(page.Sources) != 4 {
		t.Fatal(w.Code, w.Body)
	}
	for _, source := range page.Sources {
		if source.Status != "never_fetched" || source.Scanned != nil || source.Candidates != nil {
			t.Fatal(source)
		}
	}
	if _, err := p.engine.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	base := "/idle-resources?account_id=" + a.ID + "&kind=eip&limit=1"
	w = panelRequest(p, "GET", base, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == nil || len(page.Sources) != 1 || page.Sources[0].Status != "partial" || *page.Sources[0].Unclassified != 1 {
		t.Fatal(w.Code, w.Body)
	}
	first := page.Items[0]
	cursor := *page.Next
	if first.MonthlyCost != nil || first.Currency != nil || first.EvidenceStatus != "current" || !strings.Contains(w.Body.String(), `"monthly_cost":null`) {
		t.Fatal(first, w.Body)
	}
	w = panelRequest(p, "GET", base+"&cursor="+url.QueryEscape(cursor), nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID <= first.ID || page.Next != nil {
		t.Fatal(w.Code, w.Body)
	}
	for _, path := range []string{"/idle-resources?kind=other", "/idle-resources?limit=201", "/idle-resources?region_id=cn-shanghai.evil", "/idle-resources?cursor=!", "/idle-resources?kind=disk&cursor=" + url.QueryEscape(cursor)} {
		if w := panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 422 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	if w := panelRequest(p, "POST", "/idle-resources", map[string]any{}, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code, w.Body)
	}
	p.now = func() time.Time { return now.Add(11 * time.Minute) }
	w = panelRequest(p, "GET", base, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Items[0].SourceStatus != "stale" || page.Items[0].EvidenceStatus != "retained" || !page.Items[0].ObservedAt.Equal(now) {
		t.Fatal(w.Code, w.Body)
	}
	if scanner.calls != 1 {
		t.Fatal("GET triggered cloud collection", scanner.calls)
	}
}

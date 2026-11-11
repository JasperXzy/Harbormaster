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

type apiScanner struct{ calls int }

func (s *apiScanner) Scan(_ context.Context, a model.Account, _ model.Credential, _ bool, before func() error) model.ScanResult {
	s.calls++
	if before() != nil {
		return model.ScanResult{Failure: "account_changed"}
	}
	items := []model.Snapshot{}
	zero := int64(0)
	for _, suffix := range []string{"1", "2"} {
		id := a.ID + "_instance_" + suffix
		items = append(items, model.Snapshot{Kind: "ecs", ID: id, Region: "cn-shanghai", Payload: map[string]any{"id": id, "cloud_instance_id": "i-test-" + suffix, "name": "test", "account_id": a.ID, "kind": "ecs", "region_id": "cn-shanghai", "status": "running", "updated_at": time.Now().UTC().Format(time.RFC3339)}})
	}
	return model.ScanResult{Snapshots: items, CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}, CDT: &model.CDTObservation{AccountID: a.ID, FetchedAt: time.Now().UTC(), Overseas: &zero, Quality: "inferred"}, Permissions: &model.PermissionCheck{CheckedAt: time.Now().UTC(), Items: []model.PermissionItem{{Key: "ecs_read", Result: "passed"}, {Key: "instance_power", Result: "not_verified"}}}}
}
func TestAccountAPIUsesEncryptedTasksAndSnapshotReads(t *testing.T) {
	p := testPanel(t)
	box, err := security.NewBox(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: 5 * time.Minute}
	cookie, csrf := setupPanel(t, p)
	in := map[string]any{"name": "test account", "site": "china", "access_key_id": "TestAccessKeyID123456", "access_key_secret": "secret-for-api-test-never-real", "region_ids": []string{"cn-shanghai"}}
	w := panelRequest(p, "POST", "/accounts", in, cookie, "incorrect")
	if w.Code != 403 {
		t.Fatal("account creation bypassed CSRF")
	}
	w = panelRequest(p, "POST", "/accounts", in, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), in["access_key_secret"].(string)) || strings.Contains(w.Body.String(), in["access_key_id"].(string)) {
		t.Fatal("create response contains credential")
	}
	var created struct {
		Account model.Account `json:"account"`
		JobID   string        `json:"permission_check_job_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if scanner.calls != 0 {
		t.Fatal("POST performed synchronous cloud I/O")
	}
	for _, path := range []string{"/accounts", "/accounts/" + created.Account.ID + "/finance", "/instances", "/regions?site=china", "/jobs/" + created.JobID, "/traffic/billing-subjects", "/bandwidth-packages"} {
		w = panelRequest(p, "GET", path, nil, cookie, "")
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	if scanner.calls != 0 {
		t.Fatal("GET performed cloud I/O")
	}
	financePath := "/accounts/" + created.Account.ID + "/finance"
	if w := panelRequest(p, "GET", financePath, nil, nil, ""); w.Code != 401 {
		t.Fatal("anonymous finance exposed", w.Code)
	}
	if w := panelRequest(p, "GET", "/accounts/missing/finance", nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", financePath, nil, cookie, "")
	var finance model.AccountFinance
	if json.Unmarshal(w.Body.Bytes(), &finance) != nil || finance.Balance.Data != nil || finance.Bill.Data != nil || finance.Balance.Status != "never_synced" {
		t.Fatal("missing finances fabricated", w.Body)
	}
	if _, err = p.engine.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", "/jobs/"+created.JobID, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"succeeded"`) || !strings.Contains(w.Body.String(), "not_verified") {
		t.Fatal("job status not persisted", w.Body)
	}
	w = panelRequest(p, "GET", "/instances?limit=1", nil, cookie, "")
	var page struct {
		Items  []map[string]any `json:"items"`
		Cursor *string          `json:"next_cursor"`
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Cursor == nil {
		t.Fatal("first cursor page", w.Body)
	}
	firstID := page.Items[0]["id"]
	w = panelRequest(p, "GET", "/instances?limit=1&cursor="+*page.Cursor, nil, cookie, "")
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Cursor != nil || page.Items[0]["id"] == firstID {
		t.Fatal("second cursor page", w.Body)
	}
	if scanner.calls != 1 {
		t.Fatal("snapshot listing triggered cloud")
	}
	w = panelRequest(p, "GET", "/traffic/quotas?account_id="+created.Account.ID, nil, cookie, "")
	var quotas struct {
		Items []model.QuotaStatus `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &quotas) != nil || len(quotas.Items) != 2 {
		t.Fatal("quota response unavailable", w.Body)
	}
	for _, q := range quotas.Items {
		if q.EstimatedBytes != nil || q.EffectiveBytes != nil || q.OfficialAsOf != nil {
			t.Fatal("unverified source exposed as estimate")
		}
		if q.Scope == "overseas" && (q.OfficialBytes == nil || *q.OfficialBytes != 0 || q.FetchedAt == nil || q.OfficialQuality != "inferred") {
			t.Fatal("explicit zero lost")
		}
		if q.Scope == "mainland" && q.OfficialBytes != nil {
			t.Fatal("absent scope changed into zero")
		}
	}
	w = panelRequest(p, "GET", "/traffic/daily?account_id="+created.Account.ID, nil, cookie, "")
	var daily struct {
		Source  string           `json:"source_status"`
		Days    []map[string]any `json:"days"`
		Updated any              `json:"updated_at"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &daily) != nil || daily.Source != "daily_source_unverified" || daily.Updated != nil || len(daily.Days) == 0 {
		t.Fatal("daily source status incorrect")
	}
	for _, d := range daily.Days {
		if d["mainland_bytes"] != nil || d["overseas_bytes"] != nil {
			t.Fatal("daily usage invented from cumulative observations")
		}
	}
	w = panelRequest(p, "GET", "/traffic/observations?account_id="+created.Account.ID, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"official_quality":"inferred"`) {
		t.Fatal("observation history unavailable")
	}
	if scanner.calls != 1 {
		t.Fatal("traffic GET performed cloud query")
	}
	w = panelRequest(p, "DELETE", "/accounts/"+created.Account.ID, nil, cookie, csrf)
	if w.Code != 204 {
		t.Fatal("disable failed", w.Code)
	}
	w = panelRequest(p, "GET", "/instances", nil, cookie, "")
	if !strings.Contains(w.Body.String(), "i-test-1") {
		t.Fatal("disable deleted snapshots")
	}
	w = panelRequest(p, "POST", "/accounts/"+created.Account.ID+"/permission-check", map[string]any{}, cookie, csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "account_disabled") {
		t.Fatal("disabled account queried")
	}
	w = panelRequest(p, "GET", "/accounts", nil, cookie, "")
	if !strings.Contains(w.Body.String(), `"enabled":false`) || strings.Contains(w.Body.String(), "secret-for-api-test") {
		t.Fatal("account list incorrect")
	}
	w = panelRequest(p, "PATCH", "/accounts/"+created.Account.ID, map[string]any{"enabled": true}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal("reenable failed")
	}
	w = panelRequest(p, "POST", "/instances/refresh", map[string]any{"account_id": created.Account.ID}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal("refresh not asynchronous")
	}
	jobs, head, err := p.s.JobChanges(context.Background(), 0)
	if err != nil || head == 0 || len(jobs) == 0 {
		t.Fatal("durable job changes unavailable")
	}
	for _, j := range jobs {
		raw, _ := json.Marshal(j)
		if bytes.Contains(raw, []byte(in["access_key_id"].(string))) || bytes.Contains(raw, []byte(in["access_key_secret"].(string))) {
			t.Fatal("job event leaks credential")
		}
	}
}

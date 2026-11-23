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
		items[len(items)-1].Payload["ipv6"] = map[string]any{"addresses": []string{"2001:db8::" + suffix}, "status": "ok", "source": "ecs_instances", "observed_at": "2026-11-12T00:00:00Z", "checked_at": "2026-11-12T00:00:00Z", "last_error": nil}
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
	job, err := p.s.Job(context.Background(), created.JobID)
	if err != nil || job.Trigger != "panel" || len(job.AccountIDs) != 1 || job.AccountIDs[0] != created.Account.ID {
		t.Fatal("account creation did not record panel origin", job, err)
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
	if ipv6, ok := page.Items[0]["ipv6"].(map[string]any); !ok || ipv6["status"] != "ok" || ipv6["observed_at"] != "2026-11-12T00:00:00Z" || len(ipv6["addresses"].([]any)) != 1 {
		t.Fatal("stored IPv6 missing from instance API", page.Items[0])
	}
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
	w = panelRequest(p, "GET", "/traffic/quotas?account_id=all", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &quotas) != nil || len(quotas.Items) != 2 || quotas.Items[0].AccountID != "all" || quotas.Items[0].Aggregation == nil || quotas.Items[0].Aggregation.AccountCount != 1 {
		t.Fatal("aggregate quotas", w.Body)
	}
	w = panelRequest(p, "GET", "/traffic/daily?account_id=all", nil, cookie, "")
	var total model.DailyTraffic
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &total) != nil || total.AccountID != "all" || total.Aggregation == nil || total.Aggregation.AccountCount != 1 || total.Days[0].Overseas != nil {
		t.Fatal("aggregate daily", w.Body)
	}
	w = panelRequest(p, "GET", "/traffic/daily?account_id="+created.Account.ID, nil, cookie, "")
	var daily struct {
		Source  string           `json:"source_status"`
		Days    []map[string]any `json:"days"`
		Updated any              `json:"updated_at"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &daily) != nil || daily.Source != "unavailable" || daily.Updated != nil || len(daily.Days) == 0 {
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
	w = panelRequest(p, "PATCH", "/accounts/"+created.Account.ID, map[string]any{"track_mainland": false}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"track_mainland":false`) || !strings.Contains(w.Body.String(), `"track_overseas":true`) {
		t.Fatal("track_mainland disable failed", w.Code, w.Body.String())
	}
	w = panelRequest(p, "GET", "/traffic/quotas?account_id="+created.Account.ID, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &quotas) != nil || len(quotas.Items) != 1 || quotas.Items[0].Scope != "overseas" {
		t.Fatal("disabled mainland scope still returned in account quotas", w.Body.String())
	}
	w = panelRequest(p, "GET", "/traffic/quotas?account_id=all", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &quotas) != nil || len(quotas.Items) != 1 || quotas.Items[0].Scope != "overseas" {
		t.Fatal("disabled mainland scope still returned in aggregate quotas", w.Body.String())
	}
	w = panelRequest(p, "PATCH", "/accounts/"+created.Account.ID, map[string]any{"track_mainland": true}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"track_mainland":true`) {
		t.Fatal("track_mainland reenable failed", w.Code, w.Body.String())
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

func TestAccountCDTConfirmationAndSubjectOverrides(t *testing.T) {
	p := testPanel(t)
	box, err := security.NewBox(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: &apiScanner{}, Interval: 5 * time.Minute}
	cookie, csrf := setupPanel(t, p)
	in := map[string]any{"name": "cdt-confirm", "site": "china", "access_key_id": "LTAI5tTestAccount1234", "access_key_secret": "secret-for-cdt-test", "region_ids": []string{"cn-hongkong"}}
	w := panelRequest(p, "POST", "/accounts", in, cookie, csrf)
	if w.Code != 201 {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Account model.Account `json:"account"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	accID := created.Account.ID
	if created.Account.CDTConfirmed || len(created.Account.CDTSubjectOverrides) != 0 {
		t.Fatalf("unexpected default cdt fields: %+v", created.Account)
	}

	// Seed a metered CBWP subject/package/ECS and an immutable SWAS subject.
	subMetered, _ := json.Marshal(map[string]any{"id": "sub-cbwp", "name": "hk-share", "account_id": accID, "kind": "cbwp_dominant_traffic", "metering_rule": "max_in_out_per_period", "counts_toward_cdt": nil, "cdt_scope": "overseas", "public_ips": []string{"1.2.3.4"}, "instance_ids": []string{"ecs-1"}, "bandwidth_mbps": 200, "data_source": "none"})
	subSWAS, _ := json.Marshal(map[string]any{"id": "sub-swas", "name": "sha-pro", "account_id": accID, "kind": "swas_no_traffic_package", "metering_rule": "not_metered", "counts_toward_cdt": false, "cdt_scope": nil, "public_ips": []string{"5.6.7.8"}, "instance_ids": []string{"swas-1"}, "bandwidth_mbps": 200, "data_source": "none"})
	pkgMetered, _ := json.Marshal(map[string]any{"id": "sub-cbwp", "cloud_id": "cbwp-1", "name": "hk-share", "account_id": accID, "region_id": "cn-hongkong", "bandwidth_mbps": 200, "kind": "cbwp_dominant_traffic", "settled_by_cdt": nil, "members": []any{}})
	ecsSnap, _ := json.Marshal(map[string]any{"id": "ecs-1", "cloud_instance_id": "i-1", "name": "hk-1", "account_id": accID, "kind": "ecs", "region_id": "cn-hongkong", "status": "running", "billing_subject_id": "sub-cbwp", "billing_subject_kind": "cbwp_dominant_traffic", "metering_rule": "max_in_out_per_period", "counts_toward_cdt": nil, "cdt_scope": "overseas", "realtime_protection": "unprotected", "agent": map[string]any{"status": "not_installed"}})
	for _, row := range [][4]string{
		{"subject", "sub-cbwp", "cn-hongkong", string(subMetered)},
		{"subject", "sub-swas", "cn-hongkong", string(subSWAS)},
		{"package", "sub-cbwp", "cn-hongkong", string(pkgMetered)},
		{"ecs", "ecs-1", "cn-hongkong", string(ecsSnap)},
	} {
		if _, err := p.s.DB.Exec("INSERT INTO resource_snapshot(account_id,kind,id,region,payload_json,present,updated_at) VALUES(?,?,?,?,?,1,'2026-11-24T00:00:00Z')", accID, row[0], row[1], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}

	readSubjects := func() map[string]any {
		resp := panelRequest(p, "GET", "/traffic/billing-subjects?account_id="+accID, nil, cookie, "")
		if resp.Code != 200 {
			t.Fatalf("subjects failed: %d %s", resp.Code, resp.Body.String())
		}
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := map[string]any{}
		for _, it := range body.Items {
			out[it["id"].(string)] = it["counts_toward_cdt"]
		}
		return out
	}

	// Default: metered subject is nil, SWAS is false.
	got := readSubjects()
	if got["sub-cbwp"] != nil || got["sub-swas"] != false {
		t.Fatalf("unexpected initial subjects: %+v", got)
	}

	// Confirm CDT at account level -> metered subject, package, and ECS become true; SWAS remains false.
	w = panelRequest(p, "PATCH", "/accounts/"+accID, map[string]any{"cdt_confirmed": true}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cdt_confirmed":true`) {
		t.Fatalf("enable cdt_confirmed failed: %d %s", w.Code, w.Body.String())
	}
	got = readSubjects()
	if got["sub-cbwp"] != true || got["sub-swas"] != false {
		t.Fatalf("cdt_confirmed did not resolve subjects: %+v", got)
	}
	w = panelRequest(p, "GET", "/bandwidth-packages?account_id="+accID, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"settled_by_cdt":true`) {
		t.Fatalf("bandwidth-packages not resolved: %d %s", w.Code, w.Body.String())
	}
	w = panelRequest(p, "GET", "/instances?account_id="+accID, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"counts_toward_cdt":true`) {
		t.Fatalf("instances not resolved: %d %s", w.Code, w.Body.String())
	}

	// Exclude the CBWP subject via per-subject override (even trying to force SWAS true must stay false).
	w = panelRequest(p, "PATCH", "/accounts/"+accID, map[string]any{"cdt_subject_overrides": map[string]any{"sub-cbwp": false, "sub-swas": true}}, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("override failed: %d %s", w.Code, w.Body.String())
	}
	got = readSubjects()
	if got["sub-cbwp"] != false || got["sub-swas"] != false {
		t.Fatalf("override resolution mismatch: %+v", got)
	}
	w = panelRequest(p, "GET", "/instances?account_id="+accID, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"counts_toward_cdt":false`) || !strings.Contains(w.Body.String(), `"realtime_protection":"not_applicable"`) {
		t.Fatalf("excluded instance did not update protection: %d %s", w.Code, w.Body.String())
	}

	// Clear override with null -> falls back to account cdt_confirmed=true.
	w = panelRequest(p, "PATCH", "/accounts/"+accID, map[string]any{"cdt_subject_overrides": map[string]any{"sub-cbwp": nil}}, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("clear override failed: %d %s", w.Code, w.Body.String())
	}
	got = readSubjects()
	if got["sub-cbwp"] != true {
		t.Fatalf("cleared override did not inherit account setting: %+v", got)
	}
}

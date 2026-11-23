package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
	instancerelease "github.com/JasperXzy/harbormaster/internal/release"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func apiReleaseFacts() model.ReleaseInventory {
	no, yes := false, true
	return model.ReleaseInventory{Instances: []model.ReleaseInstanceFacts{{CloudID: "i-target", Name: "exact name", Status: "Stopped", ChargeType: "PostPaid", DeletionProtection: &no, Locked: &no}}, Disks: []model.ReleaseDiskFacts{{CloudID: "d-target", Category: "cloud_essd", Type: "system", InstanceIDs: []string{"i-target"}, AttachmentsKnown: true, DeleteWithInstance: &yes, DeleteAutoSnapshot: &no}}}
}

func apiReleaseFixture(t *testing.T) (*productionPanel, *http.Cookie, string, map[string]string) {
	t.Helper()
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: &apiScanner{}, Interval: time.Minute}
	a, _, err := p.engine.CreateAccount(context.Background(), model.AccountCreate{Name: "release fixture", Site: "china", AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "release-fixture-never-real", RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec("INSERT INTO instance VALUES('target',?,'i-target','ecs','exact name','cn-shanghai','stopped','stopped','now')", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec("INSERT INTO resource_snapshot VALUES(?,'ecs','target','cn-shanghai','{}',1,'now')", a.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(apiReleaseFacts())
	if _, err := p.s.DB.Exec("INSERT INTO release_inventory VALUES(?,1,'cn-shanghai',?,?,NULL,?)", a.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatal(err)
	}
	preview, err := p.s.ReleasePreview(context.Background(), "target", now)
	if err != nil || preview.Token == nil {
		t.Fatal(preview, err)
	}
	return p, cookie, csrf, map[string]string{"confirmation_name": "exact name", "preview_token": *preview.Token}
}

type panelReleaseWriter func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error)

func (f panelReleaseWriter) Execute(c context.Context, a aliyun.Credentials, r aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
	return f(c, a, r)
}

type panelReleaseObserver struct{}

func (panelReleaseObserver) Inspect(_ context.Context, _ aliyun.Credentials, region, id string) (model.ReleaseInventoryObservation, error) {
	d := apiReleaseFacts()
	return model.ReleaseInventoryObservation{Region: region, QueriedAt: time.Now().UTC(), Data: &d}, nil
}
func (panelReleaseObserver) Observe(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error) {
	return model.ReleaseObservation{ObservedAt: time.Now().UTC(), InstancePresent: false}, nil
}

func TestReleaseAPIRequiresAuthExactConfirmationAndNeverCallsCloudSynchronously(t *testing.T) {
	p, cookie, csrf, body := apiReleaseFixture(t)
	path := "/instances/target/release"
	key := model.NewID("event_")
	if w := powerRequest(p, "POST", path, body, nil, "", key); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	if w := powerRequest(p, "POST", path, body, cookie, "", key); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := powerRequest(p, "POST", path, body, cookie, csrf, key); w.Code != 503 || !strings.Contains(w.Body.String(), "instance_release_disabled") {
		t.Fatal(w.Code, w.Body)
	}
	p.releaseEnabled = true
	for _, tc := range []struct {
		method, path, key string
		body              any
		status            int
	}{
		{"GET", path, key, nil, 405},
		{"POST", path, "", body, 422},
		{"POST", path, "invalid!", body, 422},
		{"POST", path, key, map[string]string{"preview_token": body["preview_token"]}, 422},
		{"POST", path, key, map[string]string{"confirmation_name": "exact name", "preview_token": "bad"}, 422},
		{"POST", path, key, map[string]string{"confirmation_name": "EXACT NAME", "preview_token": body["preview_token"]}, 422},
		{"POST", path, key, map[string]any{"confirmation_name": "exact name", "preview_token": body["preview_token"], "force": true}, 422},
		{"POST", path, key, map[string]string{"confirmation_name": "exact name", "preview_token": strings.Repeat("0", 64)}, 409},
		{"POST", "/instances/missing/release", key, body, 404},
	} {
		if w := powerRequest(p, tc.method, tc.path, tc.body, cookie, csrf, tc.key); w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
	var count int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job WHERE type='instance_release'").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid request was enqueued", count, err)
	}
	w := panelRequest(p, "GET", "/instances/target/release-preview", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"execution_enabled":true`) {
		t.Fatal(w.Code, w.Body)
	}
	id := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, key))
	if retry := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, key)); retry != id {
		t.Fatal("retry created another release")
	}
	if same := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, model.NewID("event_"))); same != id {
		t.Fatal("active release was duplicated")
	}
	j, err := p.s.Job(context.Background(), id)
	if err != nil || j.Type != "instance_release" || j.Status != "queued" || j.Trigger != "panel" || j.InstanceID == nil || *j.InstanceID != "target" || p.engine.Scanner.(*apiScanner).calls != 0 {
		t.Fatal(j, err)
	}
	writes := 0
	worker := instancerelease.Engine{Store: p.s, Box: p.engine.Box, Enabled: true, PollInterval: time.Millisecond, ConfirmationTimeout: time.Second, Observer: panelReleaseObserver{}}
	worker.Writer = panelReleaseWriter(func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
		writes++
		return aliyun.ReleaseResult{Outcome: "accepted"}, nil
	})
	if _, err := worker.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", "/jobs/"+id, nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"succeeded"`) || !strings.Contains(w.Body.String(), `"confirmation_basis":"not_found_after_accept"`) || writes != 1 {
		t.Fatal(w.Code, w.Body, writes)
	}
	original := p.now()
	p.now = func() time.Time { return original.Add(10 * time.Minute) }
	if same := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, key)); same != id {
		t.Fatal("completed/expired confirmation was replayed")
	}
	if w := powerRequest(p, "POST", path, body, cookie, csrf, model.NewID("event_")); w.Code != 409 {
		t.Fatal("absent or expired resource accepted", w.Code, w.Body)
	}
}

func TestReleaseAPIRejectsBearerAndDuplicateActionHeaders(t *testing.T) {
	p, cookie, csrf, body := apiReleaseFixture(t)
	p.releaseEnabled = true
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest("POST", "https://panel.example/api/v1/instances/target/release", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://panel.example")
	request.Header.Set("Authorization", "Bearer not-a-cookie-session")
	request.Header.Set("Idempotency-Key", model.NewID("event_"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatal("bearer was accepted as release authentication", w.Code, w.Body)
	}
	request = httptest.NewRequest("POST", "https://panel.example/api/v1/instances/target/release", bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://panel.example")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	request.Header.Add("Idempotency-Key", model.NewID("event_"))
	request.Header.Add("Idempotency-Key", model.NewID("event_"))
	w = httptest.NewRecorder()
	p.ServeHTTP(w, request)
	if w.Code != 422 {
		t.Fatal("duplicate action headers accepted", w.Code, w.Body)
	}
}

func TestReleaseCapabilityRequiresIndependentProductionCollector(t *testing.T) {
	p := testPanel(t)
	cookie, _ := setupPanel(t, p)
	for _, tc := range []struct{ release, power, collector, dev bool }{{}, {false, true, true, false}, {true, false, false, false}, {true, false, true, false}, {true, true, true, true}} {
		var engine *collect.Engine
		if tc.collector {
			box, _ := security.NewBox(bytes.Repeat([]byte{1}, 32))
			engine = &collect.Engine{Store: p.s, Box: box}
		}
		q := NewProductionPanel(p.s, config.Config{PublicURL: "https://panel.example", Timezone: "Asia/Shanghai", SessionTTLSeconds: 43200, InstancePowerEnabled: tc.power, InstanceReleaseEnabled: tc.release, Dev: tc.dev}, engine).(*productionPanel)
		w := panelRequest(q, "GET", "/system/info", nil, cookie, "")
		var info struct {
			Release bool `json:"instance_release_enabled"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Release != (tc.release && tc.collector && !tc.dev) {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
}

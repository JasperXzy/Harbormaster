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
	"github.com/JasperXzy/harbormaster/internal/power"
	"github.com/JasperXzy/harbormaster/internal/security"
)

type panelPowerWriter func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error)

func (f panelPowerWriter) Execute(ctx context.Context, c aliyun.Credentials, r aliyun.PowerRequest) (aliyun.PowerResult, error) {
	return f(ctx, c, r)
}

type panelPowerObserver func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error)

func (f panelPowerObserver) Observe(ctx context.Context, c aliyun.Credentials, k, r, id string) (model.PowerObservation, error) {
	return f(ctx, c, k, r, id)
}

func powerRequest(p *productionPanel, method, path string, body any, cookie *http.Cookie, csrf, key string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "https://panel.example/api/v1"+path, bytes.NewReader(raw))
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("Origin", "https://panel.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}
func acceptedPowerID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var j struct {
		ID string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &j) != nil || j.ID == "" {
		t.Fatal(w.Code, w.Body)
	}
	return j.ID
}

func TestPowerAPIEnqueuesWithoutCloudIOAndWorkerConfirms(t *testing.T) {
	p := testPanel(t)
	ctx := context.Background()
	box, err := security.NewBox(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: 5 * time.Minute}
	a, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{Name: "test", Site: "china", Credential: model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "test-secret-not-real-123456"}, RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.engine.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	id := a.ID + "_instance_1"
	path := "/instances/" + id + "/power"
	cookie, csrf := setupPanel(t, p)
	body := map[string]any{"action": "stop", "stop_mode": "keep_charging"}
	key := model.NewID("event_")
	if w := powerRequest(p, "POST", path, body, nil, "", key); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	if w := powerRequest(p, "POST", path, body, cookie, "", key); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := powerRequest(p, "POST", path, body, cookie, csrf, key); w.Code != 503 || !strings.Contains(w.Body.String(), "instance_power_disabled") {
		t.Fatal("default permits writes", w.Code, w.Body)
	}
	before := panelRequest(p, "GET", "/instances", nil, cookie, "")
	if before.Code != 200 || !strings.Contains(before.Body.String(), `"desired_state":null`) {
		t.Fatal("legacy default exposed as intent", before.Code, before.Body)
	}
	p.powerEnabled = true
	for _, tc := range []struct {
		method, path, key string
		body              any
		status            int
	}{
		{"GET", path, key, nil, 405},
		{"POST", path, "", body, 422},
		{"POST", path, "bad!", body, 422},
		{"POST", path, key, map[string]any{"action": "arbitrary"}, 422},
		{"POST", path, key, map[string]any{"action": "stop"}, 422},
		{"POST", path, key, map[string]any{"action": "stop", "stop_mode": nil}, 422},
		{"POST", path, key, map[string]any{"action": "start", "stop_mode": "keep_charging"}, 422},
		{"POST", path, key, map[string]any{"action": "stop", "stop_mode": "keep_charging", "command": "anything"}, 422},
		{"POST", "/instances/missing/power", key, body, 404},
	} {
		if w := powerRequest(p, tc.method, tc.path, tc.body, cookie, csrf, tc.key); w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
	var count int
	if err = p.s.DB.QueryRow("SELECT count(*) FROM job WHERE type='instance_power'").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid request enqueued", count, err)
	}
	first := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, key))
	if retry := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, key)); retry != first {
		t.Fatal("retry duplicated")
	}
	aliasKey := model.NewID("event_")
	if alias := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, aliasKey)); alias != first {
		t.Fatal("same active action duplicated")
	}
	if w := powerRequest(p, "POST", path, map[string]string{"action": "start"}, cookie, csrf, key); w.Code != 409 || !strings.Contains(w.Body.String(), "idempotency_key_conflict") {
		t.Fatal(w.Code, w.Body)
	}
	if w := powerRequest(p, "POST", path, map[string]string{"action": "start"}, cookie, csrf, model.NewID("event_")); w.Code != 409 || !strings.Contains(w.Body.String(), "instance_power_busy") {
		t.Fatal(w.Code, w.Body)
	}
	j, err := p.s.Job(ctx, first)
	if err != nil || j.Status != "queued" || j.Trigger != "panel" || j.InstanceID == nil || *j.InstanceID != id || scanner.calls != 1 {
		t.Fatal("POST ran cloud or lost metadata", j, err, scanner.calls)
	}
	items := panelRequest(p, "GET", "/instances", nil, cookie, "")
	if items.Code != 200 || !strings.Contains(items.Body.String(), `"desired_state":"stopped"`) || !strings.Contains(items.Body.String(), `"status":"running"`) {
		t.Fatal("intent not independent of actual state", items.Code, items.Body)
	}
	writes, reads := 0, 0
	worker := power.Engine{Store: p.s, Box: box, Enabled: true, PollInterval: time.Millisecond, ConfirmationTimeout: time.Second}
	worker.Writer = panelPowerWriter(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
		writes++
		return aliyun.PowerResult{Outcome: "accepted"}, nil
	})
	worker.Observer = panelPowerObserver(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
		reads++
		status := "running"
		if reads > 1 {
			status = "stopped"
		}
		mode := "keep_charging"
		return model.PowerObservation{Status: status, StoppedMode: &mode, ObservedAt: time.Now().UTC()}, nil
	})
	if ran, err := worker.RunOne(ctx); err != nil || !ran || writes != 1 {
		t.Fatal(ran, err, writes)
	}
	detail := panelRequest(p, "GET", "/jobs/"+first, nil, cookie, "")
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"status":"succeeded"`) || !strings.Contains(detail.Body.String(), `"confirmation_basis":"target_state"`) {
		t.Fatal(detail.Code, detail.Body)
	}
	if retry := acceptedPowerID(t, powerRequest(p, "POST", path, body, cookie, csrf, aliasKey)); retry != first {
		t.Fatal("completed retry duplicated")
	}
	if ran, err := worker.RunOne(ctx); err != nil || ran || writes != 1 {
		t.Fatal("completion retry replayed", ran, err, writes)
	}
	start := acceptedPowerID(t, powerRequest(p, "POST", path, map[string]string{"action": "start"}, cookie, csrf, model.NewID("event_")))
	worker.Writer = panelPowerWriter(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
		writes++
		return aliyun.PowerResult{Outcome: "rejected"}, &aliyun.Error{Code: "permission_denied"}
	})
	if _, err = worker.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	failed, err := p.s.Job(ctx, start)
	if err != nil || failed.Status != "failed" || failed.Error.Code != "permission_denied" {
		t.Fatal(failed, err)
	}
	intents, err := p.s.InstanceIntents(ctx, []string{id})
	if err != nil || intents[id].DesiredState != "running" {
		t.Fatal("failure reverted user intent", intents, err)
	}
	if _, err = p.engine.UpdateAccount(ctx, a.ID, model.AccountUpdate{Enabled: func() *bool { v := false; return &v }()}); err != nil {
		t.Fatal(err)
	}
	if w := powerRequest(p, "POST", path, body, cookie, csrf, model.NewID("event_")); w.Code != 409 || !strings.Contains(w.Body.String(), "account_disabled") {
		t.Fatal(w.Code, w.Body)
	}
}

func TestPowerCapabilityRequiresConfiguredProductionCollector(t *testing.T) {
	p := testPanel(t)
	cookie, _ := setupPanel(t, p)
	for _, tc := range []struct{ enabled, collector bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		var engine *collect.Engine
		if tc.collector {
			box, _ := security.NewBox(bytes.Repeat([]byte{1}, 32))
			engine = &collect.Engine{Store: p.s, Box: box}
		}
		q := NewProductionPanel(p.s, config.Config{PublicURL: "https://panel.example", Timezone: "Asia/Shanghai", SessionTTLSeconds: 43200, InstancePowerEnabled: tc.enabled}, engine).(*productionPanel)
		w := panelRequest(q, "GET", "/system/info", nil, cookie, "")
		var info struct {
			Power bool `json:"instance_power_enabled"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil || info.Power != (tc.enabled && tc.collector) {
			t.Fatal(tc, w.Code, w.Body)
		}
	}
}

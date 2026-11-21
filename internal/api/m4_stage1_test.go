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

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestM4Stage1CronBrandingVersionCenterAndAlerts(t *testing.T) {
	p := testPanel(t)
	now := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)

	// 1. Pre-login public branding (F16.9)
	w := panelRequest(p, "GET", "/public/branding", nil, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"has_logo":false`) {
		t.Fatal("public branding failed", w.Code, w.Body.String())
	}
	w = panelRequest(p, "PUT", "/settings/branding", map[string]any{"revision": 0, "name": "Harbor Cloud"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal("update branding failed", w.Code, w.Body.String())
	}
	w = panelRequest(p, "GET", "/public/branding", nil, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Harbor Cloud"`) {
		t.Fatal("public branding did not reflect updated name", w.Code, w.Body.String())
	}

	// 2. Version Center & online check (F16.8)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.0","html_url":"https://github.com/JasperXzy/harbormaster/releases/tag/v0.4.0","body":"M3 and M4 release"}`))
	}))
	defer feed.Close()
	p.releaseFeedURL = feed.URL

	w = panelRequest(p, "GET", "/system/version-center", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"checked_at":null`) {
		t.Fatal("initial version center failed", w.Code, w.Body.String())
	}
	w = panelRequest(p, "POST", "/system/version-center/check", nil, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"latest_version":"0.4.0"`) {
		t.Fatal("version check failed", w.Code, w.Body.String())
	}

	// 3. URL Cron endpoint (F13.4)
	ctx := context.Background()
	box, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: &apiScanner{}, Interval: time.Minute}
	acc, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{
		Name: "cron-acc", Site: "china", RegionIDs: []string{"cn-shanghai"},
		Credential: model.Credential{AccessKeyID: "CronAccessKeyID12345", AccessKeySecret: "synthetic-secret-not-real"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.engine.RunOne(ctx); err != nil {
		t.Fatal(err)
	}

	_, cronTok, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "ext-cron", Scopes: []string{"cron:run"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, summaryTok, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "summary-only", Scopes: []string{"summary:read"}}, now)
	if err != nil {
		t.Fatal(err)
	}

	prevAccess := p.access
	p.access = newPanelAccess(config.Config{
		PublicURL:       "https://panel.example",
		TrustedProxies:  []string{"127.0.0.1/32"},
		AdminAllowCIDRs: []string{"10.0.0.0/8"},
	})
	cronReq := func(tok, sessionCookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://panel.example/api/v1/integrations/cron/run", nil)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Forwarded-For", "203.0.113.55")
		r.Header.Set("X-Forwarded-Proto", "https")
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		if sessionCookie != "" {
			r.Header.Set("Cookie", sessionCookie)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, r)
		return rec
	}

	// Session cookie alone must be rejected with 401
	if rec := cronReq("", cookie.String()); rec.Code != 401 {
		t.Fatal("cron accepted session cookie without bearer key", rec.Code)
	}
	// Wrong scope must be rejected with 403
	if rec := cronReq(summaryTok, ""); rec.Code != 403 {
		t.Fatal("cron accepted summary:read key", rec.Code)
	}
	// Valid cron:run key from external CIDR succeeds with 202
	rec := cronReq(cronTok, "")
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"scheduled":true`) {
		t.Fatal("cron run failed", rec.Code, rec.Body.String())
	}
	p.access = prevAccess

	// 4. M4 Alerts: cpu_credit_low & system_event cross-source dedup + spot urgent bypass
	obsAt := now.Add(-2 * time.Minute)
	valLow := 40.0 // 40 / 288 = 13.89% < 20%
	max24h := 288.0
	ns := "acs_ecs_dashboard"
	burstSnap := map[string]any{
		"id":                "inst_burst",
		"name":              "t6-burst",
		"account_id":        acc.ID,
		"kind":              "ecs",
		"region_id":         "cn-shanghai",
		"cloud_instance_id": "i-burst123",
		"spec":              "ecs.t6-c2m1.large",
		"status":            "running",
		"cpu": model.InstanceCPU{
			Source:    "cms",
			Namespace: &ns,
			Usage:     model.CPUMetric{MetricName: "CPUUtilization", Unit: "percent", PeriodSeconds: 60, Status: "ok", ObservedAt: &obsAt, CheckedAt: now},
			CreditBalance: &model.CPUMetric{
				MetricName: "TotalCredit", Unit: "count", PeriodSeconds: 60,
				Value: &valLow, Status: "ok", ObservedAt: &obsAt, CheckedAt: now,
			},
			CreditMax24h: &max24h,
		},
	}
	rawSnap, _ := json.Marshal(burstSnap)
	if _, err := p.s.DB.Exec(`INSERT INTO instance(id,account_id,kind,region,cloud_id,name,status,updated_at) VALUES('inst_burst',?,'ecs','cn-shanghai','i-burst123','t6-burst','running',?)`, acc.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec(`INSERT INTO resource_snapshot(account_id,kind,id,region,present,payload_json,updated_at) VALUES(?,'ecs','inst_burst','cn-shanghai',1,?,?)`, acc.ID, string(rawSnap), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	pubRecent := now.Add(-2 * time.Minute)
	maintEv, _ := json.Marshal(model.CloudSystemEvent{
		CloudEventID: "ev-maint-1", CloudInstanceID: "i-burst123", Category: "maintenance",
		EventType: "SystemMaintenance.Reboot", ProviderStatus: "Scheduled", PublishedAt: &pubRecent,
	})
	spotEv, _ := json.Marshal(model.CloudSystemEvent{
		CloudEventID: "ev-spot-1", CloudInstanceID: "i-burst123", Category: "spot_interruption",
		EventType: "Instance:PreemptibleInstanceInterruption", ProviderStatus: "Executing", PublishedAt: &pubRecent,
	})
	// Insert BOTH ecs_history and cms_spot for maintenance & spot to verify cross-source deduplication!
	for _, row := range []struct {
		id, src, eid, cat string
		payload           []byte
	}{
		{"ce_1", "ecs_history", "ev-maint-1", "maintenance", maintEv},
		{"ce_2", "cms_spot", "ev-maint-1", "maintenance", maintEv},        // ignored: maintenance only from ecs_history
		{"ce_3", "ecs_history", "ev-spot-1", "spot_interruption", spotEv}, // ignored: spot only from cms_spot
		{"ce_4", "cms_spot", "ev-spot-1", "spot_interruption", spotEv},
	} {
		if _, err := p.s.DB.Exec(`INSERT INTO system_event(id,account_id,region_id,source,cloud_event_id,cloud_instance_id,category,payload_json,first_seen_at,observed_at,seen_in_latest_scan) VALUES(?,?, 'cn-shanghai', ?, ?, 'i-burst123', ?, ?, ?, ?, 1)`,
			row.id, acc.ID, row.src, row.eid, row.cat, string(row.payload), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.s.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}

	w = panelRequest(p, "GET", "/notifications?limit=50", nil, cookie, "")
	var notePage struct {
		Items []model.Notification `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &notePage) != nil {
		t.Fatal("notifications list failed", w.Code, w.Body.String())
	}
	notes := notePage.Items
	var cpuCount, maintCount, spotCount int
	for _, n := range notes {
		if n.RuleKey == nil {
			continue
		}
		switch *n.RuleKey {
		case "cpu_credit_low":
			cpuCount++
		case "system_event":
			if strings.Contains(n.Title, "计划维护") {
				maintCount++
				if n.AlwaysUrgent() {
					t.Fatal("maintenance event should obey quiet hours (not AlwaysUrgent)")
				}
			}
			if strings.Contains(n.Title, "回收中断预警") {
				spotCount++
				if !n.AlwaysUrgent() {
					t.Fatal("spot interruption must bypass quiet hours (AlwaysUrgent)")
				}
			}
		}
	}
	if cpuCount != 1 || maintCount != 1 || spotCount != 1 {
		t.Fatalf("unexpected notification counts: cpu=%d maint=%d spot=%d (notes=%+v)", cpuCount, maintCount, spotCount, notes)
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestWidgetSummaryRequiresSummaryScopeAndBypassesAdminAllowCIDRs(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	// Restrict admin_allow_cidrs to 10.0.0.0/8 only; requests from 203.0.113.9 must be blocked on admin/control routes but allowed on /integrations/summary!
	p.access = newPanelAccess(config.Config{
		PublicURL:       "https://panel.example",
		TrustedProxies:  []string{"127.0.0.1/32"},
		AdminAllowCIDRs: []string{"10.0.0.0/8"},
	})
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: &apiScanner{}, Interval: time.Minute}
	ctx := context.Background()
	if _, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{
		Name: "widget-acc", Site: "china", RegionIDs: []string{"cn-shanghai"},
		Credential: model.Credential{AccessKeyID: "WidgetAccessKeyID123", AccessKeySecret: "synthetic-secret-not-real"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.engine.RunOne(ctx); err != nil {
		t.Fatal(err)
	}

	_, summaryTok, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "ios-widget", Scopes: []string{"summary:read"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, controlTok, err := p.s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "control-only", Scopes: []string{"instance:control"}}, now)
	if err != nil {
		t.Fatal(err)
	}

	callFromPublicIP := func(path, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://panel.example/api/v1"+path, nil)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Forwarded-Proto", "https")
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}

	// Admin route from 203.0.113.9 -> 403 forbidden
	if w := callFromPublicIP("/setup/status", ""); w.Code != http.StatusForbidden {
		t.Fatal("admin route bypassed admin_allow_cidrs", w.Code)
	}
	// Control-only token on /integrations/summary -> 403 insufficient_scope
	if w := callFromPublicIP("/integrations/summary", controlTok); w.Code != http.StatusForbidden {
		t.Fatal("expected 403 insufficient_scope", w.Code, w.Body.String())
	}
	// Summary token from 203.0.113.9 -> 200 OK!
	w := callFromPublicIP("/integrations/summary", summaryTok)
	var sum model.WidgetSummary
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &sum) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if sum.Accounts.Enabled != 1 || sum.Instances.Total == 0 || len(sum.Quotas) != 2 {
		t.Fatal("unexpected widget summary", sum)
	}
}

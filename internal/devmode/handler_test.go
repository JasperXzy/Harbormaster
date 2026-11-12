package devmode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestDevelopmentContractAndSessionBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	h := New(s, "Asia/Shanghai")
	call := func(method, path, body string, cookie *http.Cookie, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/accounts", "", nil, ""); w.Code != 401 {
		t.Fatal("anonymous data exposed")
	}
	w := call("POST", "/session", `{"password":"demo-password-only"}`, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie settings")
	}
	var session map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	for _, path := range []string{"/system/info", "/notify/quiet-hours", "/settings/heartbeat", "/passkeys", "/session/methods", "/settings", "/system-events", "/accounts", "/ram-policies", "/api-keys", "/accounts/demo-account-1/finance", "/accounts/demo-account-2/finance", "/instances?limit=1", "/traffic/quotas", "/traffic/daily?account_id=demo-account-1", "/traffic/billing-subjects", "/bandwidth-packages", "/bandwidth-packages/demo-package/hourly", "/notify/telegram", "/alert-rules", "/notifications"} {
		w = call("GET", path, "", cookie, "")
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	powerResponse := call("POST", "/instances/demo-sin/power", `{"action":"stop","stop_mode":"keep_charging"}`, cookie, session["csrf_token"])
	if powerResponse.Code != 503 || !strings.Contains(powerResponse.Body.String(), "instance_power_disabled") {
		t.Fatal("demo accepted cloud write", powerResponse.Code, powerResponse.Body)
	}
	infoResponse := call("GET", "/system/info", "", cookie, "")
	if !strings.Contains(infoResponse.Body.String(), `"instance_power_enabled":false`) {
		t.Fatal("demo advertises power enabled")
	}
	w = call("GET", "/instances?limit=1", "", cookie, "")
	jobs := call("GET", "/jobs?type=refresh&trigger=panel&account_id=demo-account-1", "", cookie, "")
	if jobs.Code != 200 || !strings.Contains(jobs.Body.String(), `"id":"demo-job-refresh"`) || strings.Contains(jobs.Body.String(), "demo-job-permissions") {
		t.Fatal("demo job filters", jobs.Code, jobs.Body)
	}
	if jobs := call("GET", "/jobs/demo-job-refresh", "", cookie, ""); jobs.Code != 200 {
		t.Fatal("demo job detail", jobs.Code)
	}
	systemEvents := call("GET", "/system-events?source=cms_spot&instance_id=demo-sin", "", cookie, "")
	if systemEvents.Code != 200 || !strings.Contains(systemEvents.Body.String(), "demo-cloud-event-2") || strings.Contains(systemEvents.Body.String(), "demo-cloud-event-1") {
		t.Fatal("demo system event filters", systemEvents.Code, systemEvents.Body)
	}
	events := call("GET", "/instance-events?instance_id=demo-sin&limit=1", "", cookie, "")
	var eventPage struct {
		Items []object `json:"items"`
		Next  string   `json:"next_cursor"`
	}
	if events.Code != 200 || json.Unmarshal(events.Body.Bytes(), &eventPage) != nil || len(eventPage.Items) != 1 || eventPage.Next == "" || eventPage.Items[0]["kind"] != "status_changed" {
		t.Fatal("demo events unavailable", events.Code, events.Body)
	}
	events = call("GET", "/instance-events?instance_id=demo-sin&limit=1&cursor="+eventPage.Next, "", cookie, "")
	if events.Code != 200 || json.Unmarshal(events.Body.Bytes(), &eventPage) != nil || len(eventPage.Items) != 1 || eventPage.Items[0]["kind"] != "discovered" {
		t.Fatal("demo event cursor repeated newest", events.Code, events.Body)
	}
	var page struct {
		Items []object `json:"items"`
		Next  string   `json:"next_cursor"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Next == "" {
		t.Fatal("pagination")
	}
	w = call("GET", "/instances?limit=1&cursor="+page.Next, "", cookie, "")
	var next struct {
		Items []object `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &next)
	if len(next.Items) != 1 || next.Items[0]["id"] == page.Items[0]["id"] {
		t.Fatal("cursor repeated item")
	}
	if call("POST", "/accounts", `{}`, cookie, "").Code != 403 {
		t.Fatal("CSRF not enforced")
	}
	if call("POST", "/accounts", `{}`, cookie, session["csrf_token"]).Code != 501 {
		t.Fatal("unsupported write appeared successful")
	}
	hash, _ := security.HashPassword([]byte("another-demo-password"))
	if err = s.ResetPassword(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if call("GET", "/session", "", cookie, "").Code != 401 {
		t.Fatal("reset did not revoke session")
	}
	if call("POST", "/session", `{"password":"demo-password-only"}`, nil, "").Code != 401 {
		t.Fatal("old password survived reset")
	}
}

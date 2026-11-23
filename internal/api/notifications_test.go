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
	"github.com/JasperXzy/harbormaster/internal/notify"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

func TestNotificationReadsRequireSessionAndDoNotRevealSecrets(t *testing.T) {
	p := testPanel(t)
	ctx := context.Background()
	if w := panelRequest(p, "GET", "/notify/telegram", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, _ := setupPanel(t, p)
	w := panelRequest(p, "GET", "/notify/telegram", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"not_configured"`) {
		t.Fatal(w.Code, w.Body)
	}
	box, _ := security.NewBox(bytes.Repeat([]byte{5}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box}
	manager := notify.Channels{Store: p.s, Box: box}
	destination := telegram.Destination{Token: "123456789:TEST_ONLY_not_a_real_bot_token", ChatID: "-100123456789", ProxyMode: "socks5", ProxyURL: "socks5://127.0.0.1:1080", ProxyUsername: "test-private-user", ProxyPassword: "test-private-password"}
	if _, err := manager.Replace(ctx, destination, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", "/notify/telegram", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"not_tested"`) || !strings.Contains(w.Body.String(), `"proxy_auth_set":true`) {
		t.Fatal(w.Code, w.Body)
	}
	for _, secret := range []string{destination.Token, destination.ProxyUsername, destination.ProxyPassword} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("channel getter exposed credential")
		}
	}
	for _, key := range []string{"one", "two", "three"} {
		if _, err := p.s.EnqueueNotification(ctx, model.Notification{EventKey: key, Kind: "alert", Severity: "warning", Title: "placeholder", Body: "placeholder"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var page struct {
		Items []model.Notification `json:"items"`
		Next  *string              `json:"next_cursor"`
	}
	w = panelRequest(p, "GET", "/notifications?limit=2", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 2 || page.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "event_key") || strings.Contains(w.Body.String(), "channel_revision") || strings.Contains(w.Body.String(), "lease_owner") {
		t.Fatal("notification internals exposed")
	}
	w = panelRequest(p, "GET", "/notifications?limit=2&cursor="+*page.Next, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next != nil {
		t.Fatal(w.Code, w.Body)
	}
	for _, path := range []string{"/notifications?limit=201", "/notifications?cursor=broken", "/notifications?cursor=MA"} {
		if w = panelRequest(p, "GET", path, nil, cookie, ""); w.Code != 422 {
			t.Fatal(path, w.Code)
		}
	}
	// Reads did not send messages or create test jobs.
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("read endpoint created work")
	}
}

func TestTelegramWritesAreProtectedWriteOnlyAndQueueTests(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{5}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box}
	const token = "123456789:TEST_ONLY_not_a_real_bot_token"
	body := map[string]any{"bot_token": token, "chat_id": "-100123456789", "proxy_mode": "reverse_proxy", "proxy_url": "https://tg.example", "proxy_username": "private-user", "proxy_password": "private-pass"}
	if w := panelRequest(p, "PUT", "/notify/telegram", body, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	if w := panelRequest(p, "POST", "/notify/telegram/test", nil, cookie, csrf); w.Code != 409 || !strings.Contains(w.Body.String(), "telegram_not_configured") {
		t.Fatal(w.Code, w.Body)
	}
	w := panelRequest(p, "PUT", "/notify/telegram", body, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"not_tested"`) || !strings.Contains(w.Body.String(), `"proxy_auth_set":true`) {
		t.Fatal(w.Code, w.Body)
	}
	for _, secret := range []string{token, "private-user", "private-pass"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("update response exposed a secret")
		}
	}
	// Omitting the token and auth keeps both while the proxy is unchanged.
	w = panelRequest(p, "PUT", "/notify/telegram", map[string]any{"chat_id": "-100777", "proxy_mode": "reverse_proxy", "proxy_url": "https://tg.example"}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"proxy_auth_set":true`) || !strings.Contains(w.Body.String(), `"chat_id":"-100777"`) {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "PUT", "/notify/telegram", map[string]any{"chat_id": "-100777", "proxy_mode": "none", "proxy_url": ""}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"proxy_auth_set":false`) {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "PUT", "/notify/telegram", map[string]any{"chat_id": "nope", "proxy_mode": "none", "proxy_url": ""}, cookie, csrf)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"chat_id":"invalid_format"`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "PUT", "/notify/telegram", map[string]any{"proxy_mode": "none", "proxy_url": ""}, cookie, csrf); w.Code != 422 {
		t.Fatal(w.Code)
	}
	w = panelRequest(p, "POST", "/notify/telegram/test", nil, cookie, csrf)
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.JobID == "" {
		t.Fatal(w.Code, w.Body)
	}
	again := panelRequest(p, "POST", "/notify/telegram/test", nil, cookie, csrf)
	if again.Code != 202 || !strings.Contains(again.Body.String(), accepted.JobID) {
		t.Fatal("duplicate unfinished test created another job", again.Body)
	}
	items, err := p.s.Notifications(context.Background(), 0, 10)
	if err != nil || len(items) != 1 || items[0].Kind != "test" || items[0].DeliveryStatus != "pending" || items[0].Title != telegramTestTitle {
		t.Fatal(items, err)
	}
}

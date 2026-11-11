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

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestQuietHoursAPIUsesBusinessTimezoneAndValidatesReplacement(t *testing.T) {
	p := testPanel(t)
	p.now = func() time.Time { return time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC) }
	cookie, csrf := setupPanel(t, p)
	if w := panelRequest(p, "GET", "/notify/quiet-hours", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "GET", "/notify/quiet-hours", nil, cookie, "")
	var initial model.QuietHours
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.Enabled || initial.Active || initial.Timezone != "Asia/Singapore" {
		t.Fatal(w.Code, w.Body)
	}
	body := map[string]any{"revision": 0, "enabled": true, "start": "23:00", "end": "08:00", "maintenance": nil}
	w = panelRequest(p, "PUT", "/notify/quiet-hours", body, cookie, "")
	if w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	w = panelRequest(p, "PUT", "/notify/quiet-hours", body, cookie, csrf)
	var current model.QuietHours
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &current) != nil || !current.Active || current.ActiveReason == nil || *current.ActiveReason != "daily" || current.Revision != 1 {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "PUT", "/notify/quiet-hours", body, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("stale policy accepted", w.Code)
	}
	for _, bad := range []map[string]any{
		{"revision": 1, "enabled": true, "start": "24:00", "end": "08:00", "maintenance": nil},
		{"revision": 1, "enabled": true, "start": "08:00", "end": "08:00", "maintenance": nil},
		{"revision": 1, "enabled": true, "start": "23:00", "end": "08:00"},
		{"revision": 1, "enabled": true, "start": "23:00", "end": "08:00", "maintenance": map[string]any{"start_at": "2026-11-12T15:00:00Z", "end_at": "2026-11-12T14:00:00Z"}},
		{"revision": 1, "enabled": true, "start": "23:00", "end": "08:00", "maintenance": map[string]any{"start_at": "2026-11-12T15:00:00Z", "end_at": "2026-11-12T16:00:00Z", "unknown": true}},
	} {
		w = panelRequest(p, "PUT", "/notify/quiet-hours", bad, cookie, csrf)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body)
		}
	}
	body["revision"] = 1
	body["enabled"] = false
	body["maintenance"] = map[string]string{"start_at": "2026-11-12T15:00:00Z", "end_at": "2026-11-12T16:00:00Z"}
	w = panelRequest(p, "PUT", "/notify/quiet-hours", body, cookie, csrf)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &current) != nil || current.Enabled || !current.Active || *current.ActiveReason != "maintenance" {
		t.Fatal(w.Code, w.Body)
	}
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("quiet settings created work", jobs, err)
	}
}

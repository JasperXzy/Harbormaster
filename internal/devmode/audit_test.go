package devmode

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestDemoAuditUsesSyntheticBoundedFilteredPages(t *testing.T) {
	now := time.Now().UTC()
	call := func(query string) (int, []model.AuditEntry, *string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/audit-log?"+query, nil)
		if !demoAuditLog(w, r, "/audit-log", now) {
			t.Fatal("demo route missing")
		}
		var out struct {
			Items []model.AuditEntry `json:"items"`
			Next  *string            `json:"next_cursor"`
		}
		if w.Code == 200 && (json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Items == nil) {
			t.Fatal(w.Body)
		}
		return w.Code, out.Items, out.Next
	}
	status, first, next := call("limit=2")
	if status != 200 || len(first) != 2 || first[0].ID != "4" || next == nil {
		t.Fatal(status, first, next)
	}
	status, second, last := call("limit=2&cursor=" + url.QueryEscape(*next))
	if status != 200 || len(second) != 2 || second[0].ID != "2" || last != nil {
		t.Fatal(status, second, last)
	}
	if status, _, _ = call("limit=2&actor=system&cursor=" + url.QueryEscape(*next)); status != 422 {
		t.Fatal("cursor accepted another filter")
	}
	for _, query := range []string{"actor=api_key", "action=instance_power_finished", "target_type=instance&target_id=demo-sin&account_id=demo-account-1"} {
		status, items, _ := call(query)
		if status != 200 || len(items) == 0 {
			t.Fatal(query, status, items)
		}
	}
	for _, query := range []string{"limit=201", "actor=unknown", "from=invalid", "from=&to=", "cursor=bad"} {
		if status, _, _ = call(query); status != 422 {
			t.Fatal(query, status)
		}
	}
	query := url.Values{"from": {now.Add(-25 * time.Minute).Format(time.RFC3339Nano)}, "to": {now.Add(-15 * time.Minute).Format(time.RFC3339Nano)}}
	status, items, _ := call(query.Encode())
	if status != 200 || len(items) != 2 {
		t.Fatal(status, items)
	}
}

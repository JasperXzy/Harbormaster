package devmode

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestDemoIdleCandidatesKeepCostsUnknownAndBindCursor(t *testing.T) {
	now := time.Now().UTC()
	f := makeFixtures(now, time.UTC)
	call := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		if !demoIdle(w, r, "/idle-resources", f, now) {
			t.Fatal("route unhandled")
		}
		return w
	}
	w := call("/idle-resources?limit=1")
	var page struct {
		Items   []model.IdleCandidate `json:"items"`
		Sources []model.IdleSource    `json:"sources"`
		Next    *string               `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Next == nil || len(page.Sources) != 24 {
		t.Fatal(w.Code, w.Body)
	}
	if !page.Items[0].Valid() || page.Items[0].MonthlyCost != nil || page.Items[0].Currency != nil {
		t.Fatal(page.Items)
	}
	first, next := page.Items[0].ID, *page.Next
	w = call("/idle-resources?limit=1&cursor=" + next)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID <= first || page.Next != nil {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("/idle-resources?kind=eip&cursor=" + next); w.Code != 422 {
		t.Fatal(w.Code, w.Body)
	}
	w = call("/idle-resources?account_id=demo-account-2")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 || len(page.Sources) != 12 {
		t.Fatal(w.Code, w.Body)
	}
}

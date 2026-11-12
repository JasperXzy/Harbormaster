package devmode

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestDemoCloudHistoryHasDistinctDirectionsAndBoundCursor(t *testing.T) {
	now := time.Now().UTC()
	f := makeFixtures(now, time.UTC)
	id := f.Instances[0]["id"].(string)
	path := "/instances/" + id + "/cloud-network-history"
	q := url.Values{"from": {now.Add(-24 * time.Hour).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}, "limit": {"1"}}
	req := httptest.NewRequest("GET", path+"?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	if !demoNetworkHistory(w, req, path, f.Instances, now) || w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var first struct {
		Items []map[string]any `json:"items"`
		Next  string           `json:"next_cursor"`
	}
	if json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Next == "" {
		t.Fatal(w.Body)
	}
	q.Set("cursor", first.Next)
	req = httptest.NewRequest("GET", path+"?"+q.Encode(), nil)
	w = httptest.NewRecorder()
	demoNetworkHistory(w, req, path, f.Instances, now)
	var second struct {
		Items []map[string]any `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Items) != 1 || first.Items[0]["direction"] == second.Items[0]["direction"] {
		t.Fatal(w.Code, w.Body)
	}
	q.Set("direction", "ingress")
	req = httptest.NewRequest("GET", path+"?"+q.Encode(), nil)
	w = httptest.NewRecorder()
	demoNetworkHistory(w, req, path, f.Instances, now)
	if w.Code != 422 {
		t.Fatal("changed filter accepted", w.Code)
	}
}

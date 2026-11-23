package devmode

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestDemoAgentHistoryProvidesIntervalsAndPagination(t *testing.T) {
	now := time.Now().UTC()
	f := makeFixtures(now, time.UTC)
	id := f.Instances[0]["id"].(string)
	path := "/instances/" + id + "/agent-network-history"
	q := url.Values{"from": {now.Add(-time.Hour).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}, "limit": {"1"}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path+"?"+q.Encode(), nil)
	if !demoAgentHistory(w, r, path, f.Instances, now) || w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var first struct {
		Items []model.AgentNetworkSample `json:"items"`
		Next  string                     `json:"next_cursor"`
	}
	if json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Items[0].Interval == nil || first.Items[0].Interval.RXRate != 100 || first.Next == "" {
		t.Fatal(w.Body)
	}
	q.Set("cursor", first.Next)
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", path+"?"+q.Encode(), nil)
	demoAgentHistory(w, r, path, f.Instances, now)
	var second struct {
		Items []model.AgentNetworkSample `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.Items[0].Sequence >= first.Items[0].Sequence {
		t.Fatal(w.Code, w.Body)
	}
	q.Set("from", now.Add(-2*time.Hour).Format(time.RFC3339Nano))
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", path+"?"+q.Encode(), nil)
	demoAgentHistory(w, r, path, f.Instances, now)
	if w.Code != 422 {
		t.Fatal("changed cursor range accepted", w.Code)
	}
}

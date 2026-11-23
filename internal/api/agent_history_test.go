package api

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func agentHistoryURL(id string, from, to time.Time) string {
	return "/instances/" + id + "/agent-network-history?" + url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}.Encode()
}
func TestAgentHistoryAPIProvidesStoredIntervalsWithoutCloudOrTokenAccess(t *testing.T) {
	p := testPanel(t)
	cookie, _ := setupPanel(t, p)
	now := p.now().UTC()
	base := now.Add(-time.Minute)
	stamp := func(v time.Time) string { return v.UTC().Format("2006-01-02T15:04:05.000000000Z") }
	for _, query := range []string{"INSERT INTO account VALUES('a','test','china','now')", "INSERT INTO instance VALUES('i-history','a','cloud-id','ecs','fixture','cn-shanghai','running','stopped','now')", "INSERT INTO agent(id,instance_id,token_hash) VALUES('history-agent','i-history',zeroblob(32))"} {
		if _, err := p.s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(1); i <= 3; i++ {
		at := base.Add(time.Duration(i-1) * 10 * time.Second)
		if _, err := p.s.DB.Exec("INSERT INTO traffic_sample VALUES('history-agent','epoch',?,?,?,?,?,?)", i, stamp(at), stamp(now), i*10000, i*1000, i*2000); err != nil {
			t.Fatal(err)
		}
		if _, err := p.s.DB.Exec("INSERT INTO traffic_sample_detail(agent_id,counter_epoch,sequence,rules_version,dropped_samples,agent_version,quality,report_interval_seconds) VALUES('history-agent','epoch',?,'r1',0,'test','stale',10)", i); err != nil {
			t.Fatal(err)
		}
	}
	path := agentHistoryURL("i-history", base.Add(5*time.Second), now) + "&limit=1"
	if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "GET", path, nil, cookie, "")
	var first struct {
		Items []model.AgentNetworkSample `json:"items"`
		Next  *string                    `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Next == nil || first.Items[0].Interval == nil || first.Items[0].Interval.RXRate != 100 {
		t.Fatal(w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "token_hash") || strings.Contains(w.Body.String(), "history-agent") {
		t.Fatal("internal authentication data exposed")
	}
	w = panelRequest(p, "GET", path+"&cursor="+url.QueryEscape(*first.Next), nil, cookie, "")
	var second struct {
		Items []model.AgentNetworkSample `json:"items"`
		Next  *string                    `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.Next != nil || second.Items[0].Sequence != 2 || !second.Items[0].Interval.StartAt.Before(base.Add(5*time.Second)) {
		t.Fatal(w.Code, w.Body)
	}
	changed := agentHistoryURL("i-history", base, now) + "&cursor=" + url.QueryEscape(*first.Next)
	if w = panelRequest(p, "GET", changed, nil, cookie, ""); w.Code != 422 {
		t.Fatal("changed cursor window accepted", w.Code)
	}
	for _, bad := range []string{"/instances/i-history/agent-network-history", agentHistoryURL("i-history", now.Add(-8*24*time.Hour), now), agentHistoryURL("i-history", now, now.Add(time.Hour)), agentHistoryURL("i-history", base, now) + "&limit=1001", path + "&cursor=invalid"} {
		if w = panelRequest(p, "GET", bad, nil, cookie, ""); w.Code != 422 {
			t.Fatal(bad, w.Code, w.Body)
		}
	}
	if w = panelRequest(p, "GET", agentHistoryURL("missing", base, now), nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("history enqueued work", jobs, err)
	}
}

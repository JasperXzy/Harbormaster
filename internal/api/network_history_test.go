package api

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func networkHistoryURL(id string, from, to time.Time) string {
	return "/instances/" + id + "/cloud-network-history?" + url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}.Encode()
}
func TestCloudNetworkHistoryAPIReadOnlyPaginationAndFilterBinding(t *testing.T) {
	p := testPanel(t)
	cookie, _ := setupPanel(t, p)
	now := p.now().UTC()
	at := now.Add(-time.Minute)
	stamp := func(v time.Time) string { return v.UTC().Format("2006-01-02T15:04:05.000000000Z") }
	if _, err := p.s.DB.Exec("INSERT INTO account VALUES('a','test','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec("INSERT INTO instance VALUES('i-history','a','cloud-id','ecs','fixture','cn-shanghai','running','stopped','now')"); err != nil {
		t.Fatal(err)
	}
	for i, d := range []string{"ingress", "egress"} {
		in, out := model.NetworkMetricNames("vpc")
		name := in
		if i == 1 {
			name = out
		}
		if _, err := p.s.DB.Exec(`INSERT INTO cloud_network_sample(instance_id,source,network_type,ip,direction,metric_name,period_seconds,sampled_at,value,checked_at) VALUES('i-history','cms','vpc','203.0.113.1',?,?,60,?,?,?)`, d, name, stamp(at), float64(i), stamp(now)); err != nil {
			t.Fatal(err)
		}
	}
	path := networkHistoryURL("i-history", now.Add(-24*time.Hour), now) + "&limit=1"
	if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
		t.Fatal("history unauthenticated", w.Code)
	}
	w := panelRequest(p, "GET", path, nil, cookie, "")
	var first struct {
		Items []model.CloudNetworkSample `json:"items"`
		Next  *string                    `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Next == nil {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", path+"&cursor="+url.QueryEscape(*first.Next), nil, cookie, "")
	var second struct {
		Items []model.CloudNetworkSample `json:"items"`
		Next  *string                    `json:"next_cursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.Next != nil || second.Items[0].Direction == first.Items[0].Direction {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", path+"&direction=ingress&cursor="+url.QueryEscape(*first.Next), nil, cookie, ""); w.Code != 422 {
		t.Fatal("cursor accepted with changed filters", w.Code)
	}
	if w = panelRequest(p, "GET", networkHistoryURL("missing", now.Add(-time.Hour), now), nil, cookie, ""); w.Code != 404 {
		t.Fatal("missing instance", w.Code)
	}
	for _, bad := range []string{"/instances/i-history/cloud-network-history", networkHistoryURL("i-history", now.Add(-8*24*time.Hour), now), networkHistoryURL("i-history", now, now.Add(time.Hour)), path + "&ip=invalid", networkHistoryURL("i-history", now.Add(-24*time.Hour), now) + "&limit=1001", path + "&cursor=bad"} {
		if w = panelRequest(p, "GET", bad, nil, cookie, ""); w.Code != 422 {
			t.Fatal(bad, w.Code, w.Body)
		}
	}
	var jobs int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("history enqueued work", jobs, err)
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestAuditAPIAuthenticatedCachedReadsAndFilterBoundPagination(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{8}, 32))
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	if w := panelRequest(p, "GET", "/audit-log", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	in := map[string]any{"name": "audit account private name", "site": "china", "access_key_id": "TestAccessKeyID123456", "access_key_secret": "synthetic-api-credential-not-real", "region_ids": []string{"cn-shanghai"}}
	w := panelRequest(p, "POST", "/accounts", in, cookie, csrf)
	var created struct {
		Account model.Account `json:"account"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := created.Account.ID
	for _, trigger := range []string{"cli", "telegram", "system"} {
		now = now.Add(time.Second)
		if _, err := p.s.UpdateAccount(model.WithJobTrigger(context.Background(), trigger), id, func(a *model.Account) ([]byte, error) { a.QuotaMainland++; return nil, nil }, now, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Second)
	if w = panelRequest(p, "PATCH", "/accounts/"+id, map[string]any{"name": "second private name"}, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	key, token, err := p.s.CreateAPIKey(context.Background(), model.APIKeyCreate{Name: "separate scope", Scopes: []string{"summary:read"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if w := integrationRequest(p, "GET", "/audit-log", token, "", nil, nil); w.Code != 401 {
		t.Fatal("bearer gained administrator audit access", w.Code)
	}
	if w := integrationRequest(p, "GET", "/integrations/audit-log", token, "", nil, nil); w.Code != 403 {
		t.Fatal("integration route exposed audit", w.Code)
	}
	readPage := func(path string) ([]model.AuditEntry, *string) {
		t.Helper()
		w := panelRequest(p, "GET", path, nil, cookie, "")
		var out struct {
			Items []model.AuditEntry `json:"items"`
			Next  *string            `json:"next_cursor"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Items == nil || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body)
		}
		for _, secret := range []string{in["access_key_id"].(string), in["access_key_secret"].(string), token, "ciphertext", "token_hash"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("audit leaked private values")
			}
		}
		return out.Items, out.Next
	}
	path := "/audit-log?account_id=" + id + "&limit=2"
	first, cursor := readPage(path)
	if len(first) != 2 || cursor == nil || first[0].Action != "account_updated" || first[0].Actor.Kind != "admin" {
		t.Fatal(first, cursor)
	}
	// An append between pages must not displace or repeat older entries.
	now = now.Add(time.Second)
	if _, err := p.s.UpdateAccount(context.Background(), id, func(a *model.Account) ([]byte, error) { a.QuotaOverseas++; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	second, next := readPage(path + "&cursor=" + url.QueryEscape(*cursor))
	if len(second) != 2 || next == nil || second[0].ID == first[0].ID || second[0].ID == first[1].ID {
		t.Fatal(second, next)
	}
	third, last := readPage(path + "&cursor=" + url.QueryEscape(*next))
	if len(third) != 1 || last != nil || third[0].Action != "account_created" {
		t.Fatal(third, last)
	}
	for _, changed := range []url.Values{{"actor": {"admin"}}, {"action": {"account_created"}}, {"target_type": {"account"}}, {"target_id": {id}}, {"account_id": {"other"}}, {"from": {now.Add(-time.Hour).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}}} {
		query := url.Values{"account_id": {id}, "limit": {"2"}, "cursor": {*cursor}}
		for k, v := range changed {
			query[k] = v
		}
		if w := panelRequest(p, "GET", "/audit-log?"+query.Encode(), nil, cookie, ""); w.Code != 422 {
			t.Fatal("cursor accepted altered filter", changed, w.Code, w.Body)
		}
	}
	for _, actor := range []string{"admin", "cli", "telegram", "system"} {
		items, _ := readPage("/audit-log?account_id=" + id + "&actor=" + actor)
		if len(items) == 0 {
			t.Fatal("missing actor", actor)
		}
		for _, e := range items {
			if e.Actor.Kind != actor {
				t.Fatal(e)
			}
		}
	}
	items, _ := readPage("/audit-log?target_type=api_key&target_id=" + key.ID + "&action=api_key_created")
	if len(items) != 1 {
		t.Fatal(items)
	}
	empty, _ := readPage("/audit-log?account_id=missing")
	if len(empty) != 0 {
		t.Fatal(empty)
	}
	for _, query := range []url.Values{
		{"limit": {"0"}}, {"limit": {"201"}}, {"cursor": {"invalid"}}, {"actor": {"agent"}}, {"action": {strings.Repeat("a", 81)}}, {"target_id": {strings.Repeat("x", 129)}},
		{"from": {now.Format(time.RFC3339Nano)}}, {"from": {""}, "to": {""}},
		{"from": {now.Add(-32 * 24 * time.Hour).Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}},
		{"from": {now.Format(time.RFC3339Nano)}, "to": {now.Add(time.Second).Format(time.RFC3339Nano)}},
		{"from": {now.Format(time.RFC3339Nano)}, "to": {now.Format(time.RFC3339Nano)}},
	} {
		if w := panelRequest(p, "GET", "/audit-log?"+query.Encode(), nil, cookie, ""); w.Code != 422 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	if w := panelRequest(p, "DELETE", "/audit-log", nil, cookie, csrf); w.Code != 405 {
		t.Fatal("audit mutation accepted", w.Code)
	}
	var before, after int
	if err := p.s.DB.QueryRow("SELECT count(*) FROM audit_log").Scan(&before); err != nil {
		t.Fatal(err)
	}
	head, err := p.s.JobChangeHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	readPage("/audit-log")
	readPage("/audit-log?from=" + url.QueryEscape(now.Add(-time.Minute).Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(now.Format(time.RFC3339Nano)))
	if err = p.s.DB.QueryRow("SELECT count(*) FROM audit_log").Scan(&after); err != nil {
		t.Fatal(err)
	}
	nextHead, err := p.s.JobChangeHead(context.Background())
	if err != nil || head != nextHead || before != after || scanner.calls != 0 {
		t.Fatal("audit GET caused work", err)
	}
}

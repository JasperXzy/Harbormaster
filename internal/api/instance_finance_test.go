package api

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestInstanceFinanceCachedMonthIdentityAndAuth(t *testing.T) {
	p := testPanel(t)
	p.now = func() time.Time { return time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC) }
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	ctx := context.Background()
	a, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{Name: "instance bill fixture", Site: "china", AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "fixture-instance-bill-never-real", RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	// UTC month end is already the next business month in this persisted zone.
	p.now = func() time.Time { return time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC) }
	if _, err = p.s.DB.Exec("UPDATE app_meta SET value='Pacific/Kiritimati' WHERE key='business_timezone'"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"target", "empty"} {
		if _, err = p.s.DB.Exec("INSERT INTO instance VALUES(?,?,?,'swas','fixture','cn-shanghai','running','stopped','now')", id, a.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	path := "/instances/target/finance"
	if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := panelRequest(p, "GET", path, nil, cookie, "")
	var out model.InstanceFinance
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Cycle != "2026-11" || out.Bill.Data != nil || out.Bill.Status != "never_synced" {
		t.Fatal(w.Code, w.Body)
	}
	data := model.InstanceBillData{Cycle: "2026-11", Items: []model.InstanceBillItem{
		{CloudID: "target", PipCode: "swas", ProductCode: "ace_eweb", Type: "SubscriptionOrder", Currency: "CNY", Pretax: "68", AfterDiscount: "68"},
		{CloudID: "target", PipCode: "swas", ProductCode: "ace_eweb", Type: "SubscriptionOrder", Currency: "CNY", Pretax: "0", AfterDiscount: "270"},
		{CloudID: "target", PipCode: "eip", Currency: "CNY", Pretax: "999"},
		{CloudID: "target-suffix", PipCode: "swas", Currency: "CNY", Pretax: "888"},
	}}
	raw, _ := json.Marshal(data)
	if _, err = p.s.DB.Exec("INSERT INTO account_finance VALUES(?,1,'instance_bill','2026-11',?,?,NULL,?)", a.ID, p.now().Format(time.RFC3339Nano), p.now().Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", path, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Bill.Data == nil || len(out.Bill.Data.Items) != 2 || out.Bill.Data.Items[1].AfterDiscount != "270" {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", "/instances/empty/finance", nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Bill.Data == nil || out.Bill.Data.Items == nil || len(out.Bill.Data.Items) != 0 {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", "/instances/missing/finance", nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = panelRequest(p, "POST", path, map[string]any{}, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if scanner.calls != 0 {
		t.Fatal("GET called cloud")
	}
	var jobs int
	if err = p.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("GET enqueued work", jobs, err)
	}
}

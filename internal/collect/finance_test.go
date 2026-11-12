package collect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type financeScanner struct {
	scanFunc
	collectFinance func(context.Context, model.Account, string) []model.FinanceObservation
}

func (s financeScanner) Finance(ctx context.Context, a model.Account, _ model.Credential, cycle string, before func() error) []model.FinanceObservation {
	if err := before(); err != nil {
		return nil
	}
	return s.collectFinance(ctx, a, cycle)
}

func TestFinanceCacheFailureRotationAndDisable(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	zone := "Pacific/Kiritimati"
	if _, err := e.Store.DB.Exec("INSERT INTO app_meta(key,value) VALUES('business_timezone',?)", zone); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation(zone)
	cycle := time.Now().In(loc).Format("2006-01")
	failed, calls := false, 0
	at := time.Now().UTC()
	e.Scanner = financeScanner{scanFunc: scanFunc(func(_ context.Context, a model.Account, _ model.Credential, _ bool, _ func() error) model.ScanResult {
		r := oneSnapshot(a)
		r.Permissions = &model.PermissionCheck{Items: []model.PermissionItem{{Key: "bss_read", Result: "not_verified"}}}
		return r
	}), collectFinance: func(_ context.Context, _ model.Account, month string) []model.FinanceObservation {
		calls++
		if month != cycle {
			t.Fatal("billing ignored persisted business timezone", month)
		}
		items := []model.FinanceObservation{{Kind: "balance", FetchedAt: at, Balance: &model.BalanceData{Currency: "USD", Available: "123.456789", Cash: "12.3"}}, {Kind: "bill", Cycle: month, FetchedAt: at, Bill: &model.BillData{Cycle: month, Items: []model.BillItem{}}}}
		items = append(items, model.FinanceObservation{Kind: "instance_bill", Cycle: month, FetchedAt: at, InstanceBill: &model.InstanceBillData{Cycle: month, Items: []model.InstanceBillItem{
			{CloudID: "i-test", PipCode: "ecs", ProductCode: "ecs", Currency: "USD", Pretax: "1.000000000001", Type: "PayAsYouGoBill"},
			{CloudID: "i-test", PipCode: "ecs", ProductCode: "ecs", Currency: "CNY", Pretax: "2", Type: "Refund"},
			{CloudID: "i-test", PipCode: "eip", ProductCode: "eip", Currency: "CNY", Pretax: "99"},
			{CloudID: "other", PipCode: "ecs", ProductCode: "ecs", Currency: "CNY", Pretax: "88"},
		}}})
		if failed {
			for i := range items {
				items[i].Error = model.Error("permission_denied")
				items[i].FetchedAt = at.Add(time.Minute)
			}
		}
		return items
	}}
	a, _, err := e.CreateAccount(ctx, testCreate("finance"))
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.Store.AccountFinance(ctx, a.ID, cycle)
	if err != nil || view.Balance.Status != "never_synced" || view.Balance.Data != nil || calls != 0 {
		t.Fatal(view, err)
	}
	if ran, err := e.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	view, err = e.Store.AccountFinance(ctx, a.ID, cycle)
	if err != nil || view.Balance.Data == nil || view.Balance.Data.Available != "123.456789" || view.Bill.Data == nil || view.Bill.Data.Items == nil {
		t.Fatal(view, err)
	}
	instance, err := e.Store.InstanceFinance(ctx, a.ID+"_instance", cycle)
	if err != nil || instance.Bill.Data == nil || len(instance.Bill.Data.Items) != 2 || instance.Bill.Data.Items[0].Pretax != "1.000000000001" || instance.Bill.Data.Items[1].Type != "Refund" {
		t.Fatal(instance, err)
	}
	empty, err := e.Store.InstanceFinance(ctx, a.ID+"_instance", "2000-01")
	if err != nil || empty.Bill.Status != "never_synced" || empty.Bill.Data != nil {
		t.Fatal(empty, err)
	}
	prior, err := e.Store.AccountFinance(ctx, a.ID, "2000-01")
	if err != nil || prior.Bill.Data != nil || prior.Bill.Status != "never_synced" {
		t.Fatal("month substituted", prior, err)
	}
	failed = true
	if _, err := e.Enqueue(ctx, "refresh", a.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	view, err = e.Store.AccountFinance(ctx, a.ID, cycle)
	if err != nil || view.Balance.Status != "failing" || view.Balance.Data.Available != "123.456789" || !view.Balance.FetchedAt.Equal(at) || !view.Balance.LastAttemptAt.Equal(at.Add(time.Minute)) {
		t.Fatal(view, err)
	}
	instance, err = e.Store.InstanceFinance(ctx, a.ID+"_instance", cycle)
	if err != nil || instance.Bill.Status != "failing" || len(instance.Bill.Data.Items) != 2 || !instance.Bill.FetchedAt.Equal(at) || !instance.Bill.LastAttemptAt.Equal(at.Add(time.Minute)) {
		t.Fatal(instance, err)
	}
	account, err := e.Store.Account(ctx, a.ID)
	if err != nil || account.PermissionCheck.Items[0].Result != "missing" {
		t.Fatal(account.PermissionCheck, err)
	}
	off := false
	if _, err := e.UpdateAccount(ctx, a.ID, model.AccountUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	view, err = e.Store.AccountFinance(ctx, a.ID, cycle)
	if err != nil || view.Enabled || view.Balance.Data == nil {
		t.Fatal("disabled history discarded", view, err)
	}
	instance, err = e.Store.InstanceFinance(ctx, a.ID+"_instance", cycle)
	if err != nil || instance.Enabled || instance.Bill.Data == nil {
		t.Fatal(instance, err)
	}
	if err := e.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.RunOne(ctx); ran || err != nil || calls != 2 {
		t.Fatal("disabled finance collected", ran, err, calls)
	}
	// Credential replacement may refer to a different cloud account. It cannot
	// inherit the preceding key's financial values before a fresh collection.
	key, secret := "TestAccessKeyID987654", "another-test-secret-never-real"
	if _, err := e.UpdateAccount(ctx, a.ID, model.AccountUpdate{AccessKeyID: &key, AccessKeySecret: &secret}); err != nil {
		t.Fatal(err)
	}
	view, err = e.Store.AccountFinance(ctx, a.ID, cycle)
	if err != nil || view.Balance.Data != nil || view.Bill.Data != nil || view.UpdatedAt != nil {
		t.Fatal("old credential data exposed", view, err)
	}
	instance, err = e.Store.InstanceFinance(ctx, a.ID+"_instance", cycle)
	if err != nil || instance.Bill.Data != nil || instance.Bill.Status != "never_synced" || instance.UpdatedAt != nil {
		t.Fatal("old instance bill exposed", instance, err)
	}
}

func TestStaleFinanceCollectionCannotPublish(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	a, _, err := e.CreateAccount(ctx, testCreate("stale finance"))
	if err != nil {
		t.Fatal(err)
	}
	e.Scanner = financeScanner{scanFunc: scanFunc(func(_ context.Context, a model.Account, _ model.Credential, _ bool, _ func() error) model.ScanResult {
		return oneSnapshot(a)
	}), collectFinance: func(ctx context.Context, a model.Account, _ string) []model.FinanceObservation {
		off := false
		if _, err := e.UpdateAccount(ctx, a.ID, model.AccountUpdate{Enabled: &off}); err != nil {
			t.Fatal(err)
		}
		return []model.FinanceObservation{{Kind: "balance", FetchedAt: time.Now().UTC(), Balance: &model.BalanceData{Currency: "CNY", Available: "999", Cash: "999"}}, {Kind: "instance_bill", Cycle: "2026-11", FetchedAt: time.Now().UTC(), InstanceBill: &model.InstanceBillData{Cycle: "2026-11", Items: []model.InstanceBillItem{}}}}
	}}
	if ran, err := e.RunOne(ctx); !ran || !errors.Is(err, store.ErrLeaseLost) {
		t.Fatal("stale job did not lose its lease", ran, err)
	}
	view, err := e.Store.AccountFinance(ctx, a.ID, time.Now().Format("2006-01"))
	if err != nil || view.Balance.Data != nil {
		t.Fatal("disabled account accepted in-flight finance", view, err)
	}
	var count int
	if err := e.Store.DB.QueryRow("SELECT count(*) FROM account_finance WHERE source='instance_bill'").Scan(&count); err != nil || count != 0 {
		t.Fatal("stale instance finance persisted", count, err)
	}
}

func TestFinancePermissionKeepsEarlierBSSFailure(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	a, _, err := e.CreateAccount(ctx, testCreate("renewal permission"))
	if err != nil {
		t.Fatal(err)
	}
	e.Scanner = financeScanner{scanFunc: scanFunc(func(_ context.Context, a model.Account, _ model.Credential, _ bool, _ func() error) model.ScanResult {
		r := oneSnapshot(a)
		r.Failure = "permission_denied"
		r.Permissions = &model.PermissionCheck{Items: []model.PermissionItem{{Key: "bss_read", Result: "missing"}}}
		return r
	}), collectFinance: func(_ context.Context, _ model.Account, cycle string) []model.FinanceObservation {
		return []model.FinanceObservation{{Kind: "balance", FetchedAt: time.Now(), Balance: &model.BalanceData{Currency: "CNY", Available: "1", Cash: "1"}}, {Kind: "bill", Cycle: cycle, FetchedAt: time.Now(), Bill: &model.BillData{Cycle: cycle, Items: []model.BillItem{}}}}
	}}
	if ran, err := e.RunOne(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	a, err = e.Store.Account(ctx, a.ID)
	if err != nil || a.PermissionCheck.Items[0].Result != "missing" || a.SyncStatus != "failing" {
		t.Fatal("finance success hid missing renewal permission", err)
	}
}

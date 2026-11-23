package collect

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type trafficFixture struct {
	observation model.CDTObservation
	reads       int
}

func (f *trafficFixture) Scan(_ context.Context, a model.Account, _ model.Credential, _ bool, _ func() error) model.ScanResult {
	return oneSnapshot(a)
}
func (f *trafficFixture) Traffic(_ context.Context, a model.Account, _ model.Credential, before func() error) model.CDTObservation {
	f.reads++
	if before() != nil {
		panic("lost account lease before query")
	}
	o := f.observation
	o.AccountID = a.ID
	return o
}
func TestOfficialObservationsStayProvisionalAcrossFailuresAndCredentialChanges(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	a, _, err := e.CreateAccount(ctx, testCreate("traffic"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 15, 10, 0, 0, 0, time.UTC)
	mainland, overseas := int64(100), int64(0)
	f := &trafficFixture{observation: model.CDTObservation{FetchedAt: now, Mainland: &mainland, Overseas: &overseas, Quality: "inferred"}}
	e.Scanner = f
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	poll := func() {
		t.Helper()
		if _, err = e.Store.EnqueueJob(ctx, "refresh", "cdt:"+a.ID, []string{a.ID}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if _, err = e.RunOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := e.Store.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	rows, err := e.Store.Quotas(ctx, accounts, loc, now)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	for _, q := range rows {
		if q.OfficialBytes == nil || q.OfficialAsOf != nil || q.OfficialQuality != "inferred" || q.OfficialStatus != "cutoff_unknown" || q.EstimatedBytes != nil || q.EffectiveBytes != nil || q.RealtimeIncrement != nil || q.WorstCaseBytes != nil || q.Forecast != nil {
			t.Fatal("unverified counter became protected estimate", q)
		}
		if q.FetchedAt == nil || !q.FetchedAt.Equal(now) {
			t.Fatal("fetch time missing")
		}
	}
	f.observation = model.CDTObservation{FetchedAt: now.Add(5 * time.Minute), Quality: "unknown", Error: model.Error("permission_denied")}
	poll()
	rows, err = e.Store.Quotas(ctx, accounts, loc, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range rows {
		if q.OfficialBytes == nil || q.OfficialStatus != "unavailable" || !q.FetchedAt.Equal(now) || !q.UpdatedAt.Equal(now.Add(5*time.Minute)) {
			t.Fatal("failed request discarded prior observation or replaced cutoff", q)
		}
	}
	items, more, err := e.Store.Observations(ctx, a.ID, 0, 1)
	if err != nil || !more || len(items) != 1 || items[0].Error == nil {
		t.Fatal("observation pagination/failed attempts missing")
	}
	rows, err = e.Store.Quotas(ctx, accounts, loc, now.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range rows {
		if q.OfficialBytes != nil {
			t.Fatal("last month's record reused for current reference cycle")
		}
	}
	id, secret := "DifferentAccessKey123456", "another-test-secret-not-real"
	if _, err = e.UpdateAccount(ctx, a.ID, model.AccountUpdate{AccessKeyID: &id, AccessKeySecret: &secret}); err != nil {
		t.Fatal(err)
	}
	accounts, err = e.Store.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = e.Store.Quotas(ctx, accounts, loc, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range rows {
		if q.OfficialBytes != nil || q.OfficialStatus != "never_fetched" {
			t.Fatal("old credential's value reused as current")
		}
	}
	items, _, err = e.Store.Observations(ctx, a.ID, 0, 100)
	if err != nil || len(items) != 2 {
		t.Fatal("rotation deleted retained history")
	}
}

func TestCDTPollScheduleIsIndependentAndDisabledAccountsDoNotPoll(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	a, _, err := e.CreateAccount(ctx, testCreate("scheduled"))
	if err != nil {
		t.Fatal(err)
	}
	e.CDTInterval = time.Minute
	e.Interval = time.Hour
	f := &trafficFixture{observation: model.CDTObservation{FetchedAt: time.Now().UTC().Add(-2 * time.Minute), Quality: "inferred"}}
	e.Scanner = f
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	f.observation.FetchedAt = time.Now().UTC()
	if err = e.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if f.reads != 2 {
		t.Fatal("CDT did not poll independently")
	}
	if err = e.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.RunOne(ctx); err != nil || ran {
		t.Fatal("poll repeated before its interval", err)
	}
	off := false
	if _, err = e.UpdateAccount(ctx, a.ID, model.AccountUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.ScheduleCDT(ctx, time.Minute, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.RunOne(ctx); err != nil || ran || f.reads != 2 {
		t.Fatal("disabled account polled")
	}
}

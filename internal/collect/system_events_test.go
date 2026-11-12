package collect

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type eventScanner struct {
	inventoryCalls, eventCalls int
	denied                     bool
}

func (s *eventScanner) Scan(ctx context.Context, a model.Account, c model.Credential, permissions bool, before func() error) model.ScanResult {
	s.inventoryCalls++
	result := oneSnapshot(a)
	result.Permissions = &model.PermissionCheck{CheckedAt: time.Now(), Items: []model.PermissionItem{{Key: "ecs_read", Result: "passed"}, {Key: "cms_read", Result: "passed"}}}
	return result
}
func (s *eventScanner) SystemEvents(ctx context.Context, a model.Account, c model.Credential, before func() error) []model.SystemEventObservation {
	s.eventCalls++
	if err := before(); err != nil {
		return nil
	}
	result := []model.SystemEventObservation{}
	for _, source := range []string{"ecs_history", "cms_spot"} {
		o := model.SystemEventObservation{AccountID: a.ID, RegionID: "cn-shanghai", Source: source, FetchedAt: time.Now().UTC(), Events: []model.CloudSystemEvent{}}
		if source == "ecs_history" {
			o.Events = append(o.Events, model.CloudSystemEvent{CloudEventID: "event-test", CloudInstanceID: "i-test", Category: "maintenance", EventType: "SystemMaintenance.Reboot", ProviderStatus: "Scheduled"})
		}
		if s.denied && source == "cms_spot" {
			o.Error = model.Error("permission_denied")
			o.Events = nil
		}
		result = append(result, o)
	}
	return result
}

func TestIndependentSystemEventJobsDoNotRescanInventory(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	scanner := &eventScanner{}
	e.Scanner = scanner
	a, _, err := e.CreateAccount(ctx, testCreate("event account"))
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := e.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if scanner.inventoryCalls != 1 || scanner.eventCalls != 1 {
		t.Fatal("initial permission checks omitted event permissions")
	}
	for range 2 {
		if err = e.Schedule(ctx); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := e.Store.Jobs(ctx, model.JobFilter{Status: "queued"}, 0, 20)
	if err != nil || len(jobs) != 1 || jobs[0].Scope != "system-events:"+a.ID || jobs[0].Trigger != "schedule" {
		t.Fatal(jobs, err)
	}
	if ran, err := e.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	if scanner.inventoryCalls != 1 || scanner.eventCalls != 2 {
		t.Fatal("event polling repeated resource/finance scan", scanner)
	}
	events, err := e.Store.SystemEvents(ctx, model.SystemEventFilter{AccountID: a.ID}, 0, 20)
	if err != nil || len(events) != 1 || events[0].InstanceID == nil {
		t.Fatal(events, err)
	}
	sources, err := e.Store.SystemEventSources(ctx, model.SystemEventFilter{AccountID: a.ID}, time.Now(), 2*time.Minute)
	if err != nil || len(sources) != 2 || sources[0].Status != "ok" || sources[1].Status != "ok" {
		t.Fatal(sources, err)
	}
	if e.SystemEventPollInterval() != time.Minute {
		t.Fatal("wrong default event interval")
	}
}

func TestEventPermissionFailureIsReportedWithoutLosingOtherSource(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	scanner := &eventScanner{denied: true}
	e.Scanner = scanner
	a, j, err := e.CreateAccount(ctx, testCreate("event denied"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := e.Store.Job(ctx, j.ID)
	if err != nil || job.Status != "failed" || job.Error.Code != "permission_denied" {
		t.Fatal(job, err)
	}
	account, err := e.Store.Account(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range account.PermissionCheck.Items {
		if item.Key == "cms_read" && item.Result != "missing" {
			t.Fatal(item)
		}
	}
	events, err := e.Store.SystemEvents(ctx, model.SystemEventFilter{}, 0, 20)
	if err != nil || len(events) != 1 {
		t.Fatal("healthy source discarded", events, err)
	}
	sources, err := e.Store.SystemEventSources(ctx, model.SystemEventFilter{}, time.Now(), 2*time.Minute)
	if err != nil || len(sources) != 2 || sources[0].Status != "error" || sources[1].Status != "ok" {
		t.Fatal(sources, err)
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func cloudEvent(id, status string) model.CloudSystemEvent {
	return model.CloudSystemEvent{CloudEventID: id, CloudInstanceID: "i-test", Category: "maintenance", EventType: "SystemMaintenance.Reboot", ProviderStatus: status}
}
func sourceObservation(now time.Time, events ...model.CloudSystemEvent) model.SystemEventObservation {
	if events == nil {
		events = []model.CloudSystemEvent{}
	}
	return model.SystemEventObservation{AccountID: "a", RegionID: "cn-shanghai", Source: "ecs_history", FetchedAt: now, Events: events}
}
func saveSources(t *testing.T, s *Store, at time.Time, observations ...model.SystemEventObservation) {
	t.Helper()
	ctx := context.Background()
	j, a := startIPv6Scan(t, s, at)
	if err := s.SaveSystemEvents(ctx, j, a, observations, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseAccountScan(ctx, a.ID, j.Owner); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishJob(ctx, j, nil, "", at); err != nil {
		t.Fatal(err)
	}
}

func TestSystemEventsRetainHistoryWithoutInventingResolution(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveSources(t, s, now, sourceObservation(now, cloudEvent("event-1", "Scheduled")))
	first, err := s.SystemEvents(ctx, model.SystemEventFilter{}, 0, 20)
	if err != nil || len(first) != 1 || first[0].InstanceID == nil || *first[0].InstanceID != "ecs" {
		t.Fatal(first, err)
	}
	id, sequence := first[0].ID, first[0].Sequence
	failed := sourceObservation(now.Add(time.Minute), cloudEvent("partial", "Executed"))
	failed.Error = model.Error("permission_denied")
	saveSources(t, s, failed.FetchedAt, failed)
	after, err := s.SystemEvents(ctx, model.SystemEventFilter{}, 0, 20)
	if err != nil || len(after) != 1 || after[0].ID != id || after[0].ProviderStatus != "Scheduled" || !after[0].ObservedAt.Equal(now) || !after[0].SeenInLatestScan {
		t.Fatal("failed scan rewrote known event", after, err)
	}
	health, err := s.SystemEventSources(ctx, model.SystemEventFilter{}, failed.FetchedAt, 2*time.Minute)
	if err != nil || health[0].Status != "error" || health[0].LastSuccessAt == nil || !health[0].LastSuccessAt.Equal(now) {
		t.Fatal(health, err)
	}
	empty := sourceObservation(now.Add(2 * time.Minute))
	saveSources(t, s, empty.FetchedAt, empty)
	after, err = s.SystemEvents(ctx, model.SystemEventFilter{}, 0, 20)
	if err != nil || after[0].SeenInLatestScan || after[0].ProviderStatus != "Scheduled" || !after[0].ObservedAt.Equal(now) {
		t.Fatal("absence fabricated resolution", after, err)
	}
	returned := sourceObservation(now.Add(3*time.Minute), cloudEvent("event-1", "Canceled"))
	saveSources(t, s, returned.FetchedAt, returned)
	after, err = s.SystemEvents(ctx, model.SystemEventFilter{}, 0, 20)
	if err != nil || after[0].ID != id || after[0].Sequence != sequence || !after[0].FirstSeenAt.Equal(now) || after[0].ProviderStatus != "Canceled" || !after[0].SeenInLatestScan {
		t.Fatal("provider change lost identity", after, err)
	}
	var path string
	if err = s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, filepath.Dir(path), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	health, err = s.SystemEventSources(ctx, model.SystemEventFilter{}, now.Add(6*time.Minute), 2*time.Minute)
	if err != nil || health[0].Status != "stale" {
		t.Fatal("restart made old source fresh", health, err)
	}
	if _, err = s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now.Add(6*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	health, err = s.SystemEventSources(ctx, model.SystemEventFilter{}, now.Add(6*time.Minute), 2*time.Minute)
	if err != nil || health[0].Status != "paused" {
		t.Fatal(health, err)
	}
	after, err = s.SystemEvents(ctx, model.SystemEventFilter{}, 0, 20)
	if err != nil || len(after) != 1 {
		t.Fatal("disabled account lost event history", after, err)
	}
}

func TestSystemEventSourcesIdentityPaginationAndLeaseFences(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cms := sourceObservation(now, cloudEvent("shared", "Normal"))
	cms.Source = "cms_spot"
	cms.Events[0].Category = "spot_interruption"
	cms.Events[0].EventType = "Instance:PreemptibleInstanceInterruption"
	saveSources(t, s, now, sourceObservation(now, cloudEvent("shared", "Scheduled"), cloudEvent("second", "Scheduled")), cms)
	first, err := s.SystemEvents(ctx, model.SystemEventFilter{}, 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatal(first, err)
	}
	cursor := first[1].Sequence
	saveSources(t, s, now.Add(time.Minute), sourceObservation(now.Add(time.Minute), cloudEvent("shared", "Executed"), cloudEvent("second", "Scheduled"), cloudEvent("newest", "Scheduled")))
	next, err := s.SystemEvents(ctx, model.SystemEventFilter{}, cursor, 20)
	if err != nil || len(next) != 1 || next[0].CloudEventID != "shared" || next[0].ProviderStatus != "Executed" {
		t.Fatal("update shifted first-seen cursor", next, err)
	}
	for _, tc := range []struct {
		f     model.SystemEventFilter
		count int
	}{{model.SystemEventFilter{Source: "cms_spot"}, 1}, {model.SystemEventFilter{Category: "maintenance"}, 3}, {model.SystemEventFilter{InstanceID: "ecs"}, 4}, {model.SystemEventFilter{InstanceID: "swas"}, 0}, {model.SystemEventFilter{AccountID: "missing"}, 0}} {
		items, err := s.SystemEvents(ctx, tc.f, 0, 20)
		if err != nil || len(items) != tc.count {
			t.Fatal(tc.f, items, err)
		}
	}
	j, a := startIPv6Scan(t, s, now.Add(2*time.Minute))
	bad := cloudEvent("shared", "Executed")
	bad.CloudInstanceID = "i-wrong"
	if err := s.SaveSystemEvents(ctx, j, a, []model.SystemEventObservation{sourceObservation(now.Add(2*time.Minute), bad)}, now.Add(2*time.Minute)); err == nil {
		t.Fatal("event identity changed")
	}
	if _, err = s.DB.Exec("UPDATE account_settings SET revision=revision+1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSystemEvents(ctx, j, a, []model.SystemEventObservation{sourceObservation(now.Add(2 * time.Minute))}, now.Add(2*time.Minute)); !errors.Is(err, ErrAccountChanged) {
		t.Fatal("stale worker published", err)
	}
	sources, err := s.SystemEventSources(ctx, model.SystemEventFilter{}, now.Add(2*time.Minute), 2*time.Minute)
	if err != nil || len(sources) != 2 || sources[0].Status != "stale" {
		t.Fatal(sources, err)
	}
}

func TestSystemEventScheduleDeduplicatesAndStopsDisabledAccounts(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now()
	for range 2 {
		if err := s.ScheduleSystemEvents(ctx, time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.Jobs(ctx, model.JobFilter{}, 0, 20)
	if err != nil || len(jobs) != 1 || jobs[0].Scope != "system-events:a" || jobs[0].Trigger != "schedule" {
		t.Fatal(jobs, err)
	}
	if _, err = s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.ScheduleSystemEvents(ctx, time.Minute, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.Jobs(ctx, model.JobFilter{}, 0, 20)
	if err != nil || len(jobs) != 1 || jobs[0].Status != "failed" || jobs[0].Error.Code != "account_changed" {
		t.Fatal(jobs, err)
	}
}

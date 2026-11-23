package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestJobHistoryStableCursorAndRecordedOrigins(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := context.Background()
	now := time.Now().UTC()
	panel, err := s.EnqueueJob(model.WithJobTrigger(ctx, "panel"), "refresh", "panel", []string{"a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.EnqueueJob(model.WithJobTrigger(ctx, "cli"), "refresh", "panel", []string{"a"}, now)
	if err != nil || duplicate.ID != panel.ID || duplicate.Trigger != "panel" || duplicate.Sequence != panel.Sequence {
		t.Fatal("dedupe changed source", duplicate, err)
	}
	cli, err := s.EnqueueJob(model.WithJobTrigger(ctx, "cli"), "permission_check", "cli", []string{"a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	system, err := s.EnqueueJob(ctx, "refresh", "system", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if system.Trigger != "system" || system.AccountIDs == nil || cli.Trigger != "cli" || panel.InstanceID != nil {
		t.Fatal("incorrect enqueue metadata", panel, cli, system)
	}
	first, err := s.Jobs(ctx, model.JobFilter{}, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != system.ID || first[1].ID != cli.ID {
		t.Fatal(first, err)
	}
	if _, err := s.EnqueueJob(ctx, "refresh", "newest", nil, now); err != nil {
		t.Fatal(err)
	}
	next, err := s.Jobs(ctx, model.JobFilter{}, first[1].Sequence, 2)
	if err != nil || len(next) != 1 || next[0].ID != panel.ID {
		t.Fatal("cursor shifted after insertion", next, err)
	}
	for _, test := range []struct {
		filter model.JobFilter
		count  int
	}{
		{model.JobFilter{AccountID: "a"}, 2}, {model.JobFilter{Trigger: "panel"}, 1}, {model.JobFilter{Type: "permission_check", Trigger: "cli", Status: "queued"}, 1},
		{model.JobFilter{AccountID: "missing"}, 0}, {model.JobFilter{InstanceID: "ecs"}, 0},
	} {
		items, err := s.Jobs(ctx, test.filter, 0, 20)
		if err != nil || len(items) != test.count {
			t.Fatal(test.filter, items, err)
		}
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	items, err := s.Jobs(ctx, model.JobFilter{AccountID: "a"}, 0, 20)
	if err != nil || len(items) != 2 {
		t.Fatal("disabled account lost history", items, err)
	}
}

func TestScheduledJobOriginsOverrideCLIContext(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	ctx := model.WithJobTrigger(context.Background(), "cli")
	now := time.Now().UTC()
	if err := s.ScheduleScans(ctx, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleCDT(ctx, time.Minute, now); err != nil {
		t.Fatal(err)
	}
	items, err := s.Jobs(ctx, model.JobFilter{Trigger: "schedule"}, 0, 20)
	if err != nil || len(items) != 2 {
		t.Fatal("scheduled work attributed to invoking CLI", items, err)
	}
}

func TestJobHistoryMigrationPreservesLegacyTasks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "harbormaster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE schema_migration(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() >= "016_job_history.sql" {
			break
		}
		raw, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatal(file.Name(), err)
		}
		if _, err := db.Exec("INSERT INTO schema_migration VALUES(?,'now')", file.Name()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO app_meta VALUES('mode','production')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO job(id,type,scope,account_ids_json,status,created_at) VALUES('legacy','refresh','all','null','succeeded','2026-11-12T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	legacy, err := s.Job(ctx, "legacy")
	if err != nil || legacy.Trigger != "unknown" || legacy.InstanceID != nil || legacy.Sequence <= 0 || legacy.AccountIDs == nil || legacy.Status != "succeeded" {
		t.Fatal("legacy task rewritten or source invented", legacy, err)
	}
	newJob, err := s.EnqueueJob(model.WithJobTrigger(ctx, "panel"), "refresh", "new", nil, time.Now())
	if err != nil || newJob.Sequence <= legacy.Sequence {
		t.Fatal("new order overlaps backfill", newJob, err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := s.Jobs(ctx, model.JobFilter{}, 0, 10)
	if err != nil || len(items) != 2 {
		t.Fatal("migration rerun duplicated records", items, err)
	}
}

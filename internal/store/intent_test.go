package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestInstanceIntentIsSeparateFromLegacyAndObservedState(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	views, err := s.InstanceIntents(ctx, []string{"ecs", "swas"})
	if err != nil || len(views) != 0 {
		t.Fatal("legacy defaults promoted to intent", views, err)
	}
	first := queuePower(t, s, "start", now)
	views, err = s.InstanceIntents(ctx, []string{"ecs", "swas"})
	if err != nil || len(views) != 1 || views["ecs"].DesiredState != "running" || !views["ecs"].UpdatedAt.Equal(now) {
		t.Fatal(views, err)
	}
	if _, err = s.DB.Exec("UPDATE instance SET status='stopped' WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	views, err = s.InstanceIntents(ctx, []string{"ecs"})
	if err != nil || views["ecs"].DesiredState != "running" {
		t.Fatal("observation changed intent", views, err)
	}
	p := claimPower(t, s, now)
	if _, err = s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPowerDispatch(ctx, p.Job, "rejected", "permission_denied", now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	queuePower(t, s, "stop", later)
	var oldKey string
	if err = s.DB.QueryRow("SELECT event_key FROM instance_power_request WHERE job_id=? LIMIT 1", first.ID).Scan(&oldKey); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueuePowerJob(ctx, "ecs", "start", "", oldKey, later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	views, err = s.InstanceIntents(ctx, []string{"ecs"})
	if err != nil || views["ecs"].DesiredState != "stopped" || !views["ecs"].UpdatedAt.Equal(later) {
		t.Fatal("old event replay overwrote newer intent", views, err)
	}
}

func TestIntentMigrationBackfillsOnlyProvenPowerIntent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "harbormaster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA foreign_keys=ON; CREATE TABLE schema_migration(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() >= "019_instance_intent.sql" {
			break
		}
		raw, e := migrations.ReadFile("migrations/" + file.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(file.Name(), e)
		}
		if _, e = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", file.Name()); e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{
		`INSERT INTO app_meta VALUES('mode','production')`,
		`INSERT INTO account VALUES('a','account','china','now')`,
		`INSERT INTO instance VALUES('manual','a','i-1','ecs','manual','cn-shanghai','running','stopped','now')`,
		`INSERT INTO instance VALUES('legacy','a','i-2','ecs','legacy','cn-shanghai','running','stopped','now')`,
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,instance_id) VALUES('first','instance_power','one','["a"]','failed','2026-11-12T02:00:00Z','manual')`,
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,instance_id) VALUES('second','instance_power','two','["a"]','failed','2026-11-12T01:00:00Z','manual')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	views, err := s.InstanceIntents(ctx, []string{"manual", "legacy"})
	if err != nil || len(views) != 1 || views["manual"].DesiredState != "stopped" || views["manual"].UpdatedAt.Format(time.RFC3339) != "2026-11-12T01:00:00Z" {
		t.Fatal(views, err)
	}
	var job string
	if err = s.DB.QueryRow("SELECT job_id FROM instance_intent WHERE instance_id='manual'").Scan(&job); err != nil || job != "second" {
		t.Fatal("backfill used wall clock rather than enqueue order", job, err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

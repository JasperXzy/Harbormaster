package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestHeartbeatMigrationPreservesPowerRelationsAndHistory(t *testing.T) {
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
		if file.Name() >= "025_heartbeat.sql" {
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
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,trigger_source,lease_owner,lease_until,attempts) VALUES('legacy','refresh','all','["a"]','running','2026-11-12T00:00:00Z','cli','owner',9999999999,2)`,
		`UPDATE job SET result_json='{"preserved":true}' WHERE id='legacy'`,
		`INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,details_json) VALUES('a',1,'legacy',1,'now','{}')`,
		`INSERT INTO notification(id,event_key,job_id,created_at,kind,severity,title,body,delivery_status) VALUES('notice','event','legacy','now','test','info','title','body','pending')`,
		`INSERT INTO instance VALUES('i','a','i-test','ecs','fixture','cn-shanghai','running','running','now')`,
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,trigger_source,instance_id) VALUES('power','instance_power','i','["a"]','queued','2026-11-12T00:00:00Z','panel','i')`,
		`INSERT INTO instance_power_job(job_id,account_revision,action) VALUES('power',1,'start')`,
		`INSERT INTO instance_power_request(principal_id,event_key,job_id) VALUES('panel','test-event','power')`,
		`INSERT INTO instance_intent VALUES('i','power','now')`,
		`INSERT INTO api_key(id,name,scopes_json,token_hash,created_at) VALUES('key','fixture','["instance:control"]',zeroblob(32),'now')`,
		`INSERT INTO api_key_power_job VALUES('power','key')`,
		`UPDATE sqlite_sequence SET seq=100 WHERE name='job_order'`,
		`UPDATE sqlite_sequence SET seq=200 WHERE name='job_change'`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(q, err)
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
	legacy, err := s.Job(ctx, "legacy")
	if err != nil || legacy.Status != "running" || legacy.Owner != "owner" || legacy.Attempts != 2 || legacy.Trigger != "cli" || legacy.Sequence != 1 {
		t.Fatal(legacy, err)
	}
	for _, tc := range []struct {
		query string
		want  int
	}{
		{`SELECT count(*) FROM cdt_observation WHERE job_id='legacy' AND attempt=1`, 1},
		{`SELECT count(*) FROM notification WHERE job_id='legacy' AND delivery_status='pending'`, 1},
		{`SELECT count(*) FROM notification_change`, 1},
		{`SELECT count(*) FROM job_change WHERE job_id='legacy'`, 2},
		{`SELECT count(*) FROM job_order WHERE job_id='legacy' AND seq=1`, 1},
		{`SELECT count(*) FROM instance_power_job WHERE job_id='power' AND action='start'`, 1},
		{`SELECT count(*) FROM instance_power_request WHERE job_id='power' AND event_key='test-event'`, 1},
		{`SELECT count(*) FROM instance_intent WHERE job_id='power' AND instance_id='i'`, 1},
		{`SELECT count(*) FROM api_key_power_job WHERE job_id='power' AND key_id='key'`, 1},
		{`SELECT count(*) FROM pragma_foreign_key_check`, 0},
		{`SELECT foreign_keys FROM pragma_foreign_keys`, 1},
	} {
		var got int
		if err := s.DB.QueryRow(tc.query).Scan(&got); err != nil || got != tc.want {
			t.Fatal(tc.query, got, err)
		}
	}
	fresh, err := s.EnqueueJob(ctx, "refresh", "new", nil, time.Now())
	if err != nil || fresh.Sequence != 101 {
		t.Fatal("cursor high water lost", fresh, err)
	}
	head, err := s.JobChangeHead(ctx)
	if err != nil || head != 201 {
		t.Fatal("SSE high water lost", head, err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not repeatable", err)
	}
}

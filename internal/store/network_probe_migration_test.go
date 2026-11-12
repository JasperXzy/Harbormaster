package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProbeMigrationPreservesHeartbeatAndFinance(t *testing.T) {
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
		if file.Name() >= "028_network_probe.sql" {
			break
		}
		raw, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(file.Name(), err)
		}
		if _, err = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", file.Name()); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"INSERT INTO app_meta VALUES('mode','production')",
		"INSERT INTO account VALUES('a','test','china','now')",
		"INSERT INTO job(id,type,scope,account_ids_json,status,created_at,lease_owner,lease_until) VALUES('heartbeat-existing','heartbeat_test','heartbeat','[]','running','2026-11-12T00:00:00Z','existing-owner',9999999999)",
		"INSERT INTO heartbeat_test VALUES('heartbeat-existing',7)",
		`INSERT INTO account_finance VALUES('a',1,'balance','','first','second','permission_denied','{"currency":"CNY","available_amount":"0.123456789012"}')`,
		`INSERT INTO account_finance VALUES('a',2,'bill','2026-11','third','third',NULL,'{"billing_cycle":"2026-11","items":[]}')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var fetched, attempt, code, raw string
	err = s.DB.QueryRow("SELECT fetched_at,last_attempt_at,error_code,payload_json FROM account_finance WHERE source='balance'").Scan(&fetched, &attempt, &code, &raw)
	if err != nil || fetched != "first" || attempt != "second" || code != "permission_denied" || raw != `{"currency":"CNY","available_amount":"0.123456789012"}` {
		t.Fatal(fetched, attempt, code, raw, err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM account_finance").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}

	var revision int
	if err = s.DB.QueryRow("SELECT revision FROM heartbeat_test WHERE job_id='heartbeat-existing'").Scan(&revision); err != nil || revision != 7 {
		t.Fatal(revision, err)
	}
	job, err := s.Job(context.Background(), "heartbeat-existing")
	if err != nil || job.Status != "running" || job.Owner != "existing-owner" {
		t.Fatal(job, err)
	}
	var changes int
	if err = s.DB.QueryRow("SELECT count(*) FROM job_change WHERE job_id='heartbeat-existing'").Scan(&changes); err != nil || changes != 1 {
		t.Fatal("migration fabricated job events", changes, err)
	}
	rows, err := s.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestInstanceFinanceMigrationPreservesExistingSources(t *testing.T) {
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
		if file.Name() >= "027_instance_finance.sql" {
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
	if _, err = s.DB.Exec("INSERT INTO account_finance VALUES('a',2,'instance_bill','2026-11',NULL,'now','permission_denied',NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"INSERT INTO account_finance VALUES('missing',1,'instance_bill','2026-11',NULL,'now',NULL,NULL)", "INSERT INTO account_finance VALUES('a',1,'other','2026-11',NULL,'now',NULL,NULL)", "INSERT INTO account_finance VALUES('a',1,'instance_bill','',NULL,'now',NULL,NULL)"} {
		if _, err = s.DB.Exec(q); err == nil {
			t.Fatal("constraint lost", q)
		}
	}
}

package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestServerProbeMigrationPreservesAgentTasks(t *testing.T) {
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
		if file.Name() >= "029_server_probe.sql" {
			break
		}
		raw, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", file.Name()); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"INSERT INTO app_meta VALUES('mode','production')",
		"INSERT INTO account VALUES('a','test','china','now')",
		"INSERT INTO instance VALUES('i','a','i-cloud','ecs','fixture','cn-shanghai','running','stopped','now')",
		"INSERT INTO agent(id,instance_id,token_hash) VALUES('agent','i',zeroblob(32))",
		"INSERT INTO job(id,type,scope,account_ids_json,status,created_at,lease_owner,lease_until) VALUES('probe-existing','network_probe','probe:old','[\"a\"]','running','2026-11-12T00:00:00Z','old-owner',9999999999)",
		`INSERT INTO network_probe_job VALUES('probe-existing','old','i','agent',zeroblob(32),7,'{"host":"1.1.1.1"}',9999999999,'old-owner','{"sample":"retained"}')`,
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
	var kind, instance, agent, request, owner, result string
	var token []byte
	var version, until int64
	err = s.DB.QueryRow("SELECT point_kind,instance_id,agent_id,token_hash,credential_version,request_json,queued_until,claimed_by,result_json FROM network_probe_job WHERE job_id='probe-existing'").Scan(&kind, &instance, &agent, &token, &version, &request, &until, &owner, &result)
	if err != nil || kind != "agent" || instance != "i" || agent != "agent" || !bytes.Equal(token, make([]byte, 32)) || version != 7 || request != `{"host":"1.1.1.1"}` || until != 9999999999 || owner != "old-owner" || result != `{"sample":"retained"}` {
		t.Fatal("agent task changed", err)
	}
	var changes int
	var action string
	if err = s.DB.QueryRow("SELECT action FROM network_probe_job WHERE job_id='probe-existing'").Scan(&action); err != nil || action != "probe" {
		t.Fatal("old task action changed", action, err)
	}
	if _, err = s.DB.Exec("UPDATE network_probe_job SET action='shell' WHERE job_id='probe-existing'"); err == nil {
		t.Fatal("arbitrary action accepted")
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM job_change WHERE job_id='probe-existing'").Scan(&changes); err != nil || changes != 1 {
		t.Fatal(changes, err)
	}
	if _, err = s.DB.Exec("UPDATE network_probe_job SET point_kind='server' WHERE job_id='probe-existing'"); err == nil {
		t.Fatal("cross-point binding accepted")
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

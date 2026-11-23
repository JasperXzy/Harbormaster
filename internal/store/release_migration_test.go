package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func releaseMigrationRows(t *testing.T, db *sql.DB, table string) [][]any {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := [][]any{}
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = append([]byte{}, b...)
			}
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// SQLite may choose a different row traversal after rebuilding an index.
	// Compare content as a multiset, not by physical rowid or query plan.
	return result
}

func TestReleaseMigrationPreservesJobsRelationsAuditAndSequence(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	j := queuePower(t, s, "start", now)
	p, err := s.ClaimPowerJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	var database string
	if err := s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&database); err != nil {
		t.Fatal(err)
	}
	tables := []string{"job", "job_change", "job_order", "instance_power_job", "instance_power_request", "instance_intent", "notification", "cdt_observation", "audit_log", "release_inventory"}
	before := map[string]map[string]int{}
	for _, table := range tables {
		before[table] = map[string]int{}
		for _, row := range releaseMigrationRows(t, s.DB, table) {
			before[table][fmt.Sprint(row)]++
		}
	}
	for _, statement := range []string{
		"DROP TRIGGER audit_release_started", "DROP TRIGGER audit_release_finished",
		"DROP TABLE instance_release_request", "DROP TABLE instance_release_job",
		"DELETE FROM schema_migration WHERE name='043_instance_release.sql'",
	} {
		if _, err := s.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(ctx, filepath.Dir(database), false)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	for _, table := range tables {
		after := map[string]int{}
		for _, row := range releaseMigrationRows(t, next.DB, table) {
			after[fmt.Sprint(row)]++
		}
		if !reflect.DeepEqual(before[table], after) {
			t.Fatalf("migration changed %s content", table)
		}
	}
	var fk, deferred, broken, triggers int
	if err := next.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := next.DB.QueryRow("PRAGMA defer_foreign_keys").Scan(&deferred); err != nil {
		t.Fatal(err)
	}
	if err := next.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil {
		t.Fatal(err)
	}
	if err := next.DB.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name='job'").Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if fk != 1 || deferred != 0 || broken != 0 || triggers != 8 {
		t.Fatal(fk, deferred, broken, triggers)
	}
	if _, err := next.DB.Exec("UPDATE job SET status='failed' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	var finished int
	if err := next.DB.QueryRow("SELECT count(*) FROM audit_log WHERE action='instance_power_finished' AND job_id=?", j.ID).Scan(&finished); err != nil || finished != 1 {
		t.Fatal("power audit trigger was lost", finished, err)
	}
	// The new kind is admitted and gets a strictly later public sequence.
	created := queueRelease(t, next, "release_migration_test_key", now)
	if created.Sequence <= j.Sequence {
		t.Fatal("job sequence regressed", created.Sequence, j.Sequence)
	}
}

func TestReleaseMigrationRejectsBrokenForeignKeysAtomically(t *testing.T) {
	s := powerStore(t)
	s.DB.SetMaxOpenConns(1)
	var database string
	if err := s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&database); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DROP TRIGGER audit_release_started", "DROP TRIGGER audit_release_finished",
		"DROP TABLE instance_release_request", "DROP TABLE instance_release_job",
		"DELETE FROM schema_migration WHERE name='043_instance_release.sql'",
		"PRAGMA foreign_keys=OFF",
		"INSERT INTO job_change(job_id) VALUES('missing-parent-fixture')",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := s.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if opened, err := Open(context.Background(), filepath.Dir(database), false); err == nil {
		opened.Close()
		t.Fatal("migration accepted a pre-existing broken relation")
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied, newTable, originalBadRow int
	if err := db.QueryRow("SELECT count(*) FROM schema_migration WHERE name='043_instance_release.sql'").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='instance_release_job'").Scan(&newTable); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM job_change WHERE job_id='missing-parent-fixture'").Scan(&originalBadRow); err != nil {
		t.Fatal(err)
	}
	if applied != 0 || newTable != 0 || originalBadRow != 1 {
		t.Fatal(applied, newTable, originalBadRow)
	}
}

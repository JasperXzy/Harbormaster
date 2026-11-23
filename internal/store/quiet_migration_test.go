package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestQuietMigrationBackfillsUrgencyWithoutChangingDeliveryHistory(t *testing.T) {
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
		if file.Name() >= "026_quiet_hours.sql" {
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
	for _, query := range []string{
		"INSERT INTO app_meta VALUES('mode','production')",
		"INSERT INTO notification(id,event_key,created_at,kind,severity,rule_key,title,body,delivery_status) VALUES('offline','offline','2026-11-12T00:00:00Z','recovery','info','agent_offline','placeholder','placeholder','pending')",
		"INSERT INTO notification(id,event_key,created_at,kind,severity,title,body,delivery_status) VALUES('critical','critical','2026-11-12T00:00:00Z','alert','critical','placeholder','placeholder','sent')",
		"INSERT INTO notification(id,event_key,created_at,kind,severity,title,body,delivery_status) VALUES('normal','normal','2026-11-12T00:00:00Z','alert','warning','placeholder','placeholder','failed')",
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		id, status string
		urgent     bool
	}{{"offline", "pending", true}, {"critical", "sent", true}, {"normal", "failed", false}} {
		n, err := s.Notification(context.Background(), tc.id)
		if err != nil || n.DeliveryStatus != tc.status || n.Urgent != tc.urgent || n.SuppressionReason != nil {
			t.Fatal(n, err)
		}
	}
	var changes int
	if err = s.DB.QueryRow("SELECT count(*) FROM notification_change").Scan(&changes); err != nil || changes != 3 {
		t.Fatal("synthetic migration delivery events", changes, err)
	}
}

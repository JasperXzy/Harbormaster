package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestRestoreWithNewMasterCancelsOldWorkAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	newKey, _ := security.NewBox(bytes.Repeat([]byte{5}, 32))
	if err := s.BindMasterKey(ctx, old); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := model.Account{ID: "a", Name: "fixture", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}, UpdatedAt: now}
	cipher, _ := old.Encrypt([]byte("restore-account-fixture"), model.CredentialOwner(a.ID))
	if _, err := s.CreateAccount(ctx, a, cipher, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueJob(ctx, "refresh", "a", []string{"a"}, now); err != nil {
		t.Fatal(err)
	}
	running, err := s.ClaimJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAccountScan(ctx, "a", running.Owner, now); err != nil {
		t.Fatal(err)
	}
	completed, err := s.EnqueueJob(ctx, "refresh", "completed", []string{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE job SET status='succeeded',finished_at=? WHERE id=?", now.Format(time.RFC3339Nano), completed.ID); err != nil {
		t.Fatal(err)
	}
	channel, _ := old.Encrypt([]byte("restore-channel-fixture"), model.TelegramCredentialOwner)
	if _, err := s.SaveTelegramChannel(ctx, channel, 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueTelegramTest(ctx, "test placeholder", "test placeholder", now); err != nil {
		t.Fatal(err)
	}
	sent, err := s.EnqueueNotification(ctx, model.Notification{EventKey: "sent-history", Kind: "alert", Severity: "info", Title: "placeholder", Body: "placeholder"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE notification SET delivery_status='sent',message_id=123,delivered_at=? WHERE id=?", now.Format(time.RFC3339Nano), sent.ID); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO session VALUES(x'01','2099-01-01T00:00:00Z')",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','test','cn-shanghai','running','stopped','now')",
		"INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')",
	} {
		if _, err := s.DB.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AgentGrant(ctx, "ecs", now); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareSetup(ctx, []byte("restore-setup-fixture-token"), now); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "fixture", Scopes: []string{"summary:read"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := Export(ctx, s, old, testPassword, &archive); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "restored")
	summary, err := Restore(ctx, bytes.NewReader(archive.Bytes()), testPassword, newKey, target, now.Add(time.Hour))
	if err != nil || summary.CancelledJobs != 3 || summary.CancelledNotifications != 1 {
		t.Fatal(summary, err)
	}
	restored, err := store.Open(ctx, target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.BindMasterKey(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	if err := restored.BindMasterKey(ctx, old); !errors.Is(err, store.ErrMasterKey) {
		t.Fatal("old master accepted", err)
	}
	var sealed []byte
	if err := restored.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", model.CredentialOwner("a")).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if plain, err := newKey.Decrypt(sealed, model.CredentialOwner("a")); err != nil || string(plain) != "restore-account-fixture" {
		t.Fatal("credential lost", err)
	}
	for _, query := range []string{
		"SELECT count(*) FROM session", "SELECT count(*) FROM agent_registration", "SELECT count(*) FROM setup_credential",
		"SELECT count(*) FROM job WHERE status IN ('queued','running') OR lease_owner IS NOT NULL OR lease_until<>0",
		"SELECT count(*) FROM notification WHERE delivery_status='pending' OR lease_owner IS NOT NULL OR lease_until<>0",
		"SELECT count(*) FROM account_settings WHERE lease_owner IS NOT NULL OR lease_until<>0 OR next_scan_at<>0 OR next_cdt_at<>0",
	} {
		var n int
		if err := restored.DB.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatal(query, n, err)
		}
	}
	job, err := restored.Job(ctx, completed.ID)
	if err != nil || job.Status != "succeeded" {
		t.Fatal("completed history changed", job, err)
	}
	job, err = restored.Job(ctx, running.ID)
	if err != nil || job.Status != "failed" || job.Error == nil || job.Error.Code != "backup_restored" {
		t.Fatal(job, err)
	}
	notices, err := restored.Notifications(ctx, 0, 10)
	if err != nil || len(notices) != 2 {
		t.Fatal(notices, err)
	}
	for _, n := range notices {
		if n.ID == sent.ID {
			if n.DeliveryStatus != "sent" {
				t.Fatal("sent history changed")
			}
			continue
		}
		if n.DeliveryStatus != "failed" || !n.Uncertain || n.LastError == nil || n.LastError.Code != "backup_restored" {
			t.Fatal("pending delivery replayable", n)
		}
	}
	if _, err := restored.AuthenticateAPIKey(ctx, token, "summary:read", now.Add(time.Hour)); err != nil {
		t.Fatal("persistent API key lost", err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM job WHERE status IN ('queued','running')").Scan(&n); err != nil || n != 3 {
		t.Fatal("source work modified", n, err)
	}
	if err := s.DB.QueryRow("SELECT count(*) FROM session").Scan(&n); err != nil || n != 1 {
		t.Fatal("source sessions modified", n, err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 1 {
		t.Fatal("restore staging leaked", entries, err)
	}
	if _, err := Restore(ctx, bytes.NewReader(archive.Bytes()), testPassword, newKey, target, now); !errors.Is(err, ErrRestoreTargetExists) {
		t.Fatal("existing target accepted", err)
	}
}

func TestRestoreRejectsInvalidArchiveWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	key, _ := security.NewBox(bytes.Repeat([]byte{6}, 32))
	e, err := newEnvelope(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"empty_authenticated", "non_sqlite_authenticated", "truncated", "wrong_password"} {
		t.Run(mode, func(t *testing.T) {
			var data bytes.Buffer
			plain := []byte{}
			if mode == "non_sqlite_authenticated" {
				plain = []byte("not a database")
			}
			if err := e.seal(ctx, &data, bytes.NewReader(plain)); err != nil {
				t.Fatal(err)
			}
			b := data.Bytes()
			password := testPassword
			if mode == "truncated" {
				b = b[:len(b)-1]
			}
			if mode == "wrong_password" {
				password = []byte("different-fixture-password")
			}
			parent := t.TempDir()
			if _, err := Restore(ctx, bytes.NewReader(b), password, key, filepath.Join(parent, "target"), time.Now()); err == nil {
				t.Fatal("invalid archive accepted")
			}
			if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
				t.Fatal("failed restore published files", entries, err)
			}
		})
	}
}

func TestRestoreRejectsIncompatibleOrInvalidDatabase(t *testing.T) {
	ctx := context.Background()
	e, err := newEnvelope(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	master, _ := security.NewBox(bytes.Repeat([]byte{11}, 32))
	for _, mode := range []string{"future_schema", "missing_lineage", "foreign_key", "bad_credential"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s, err := store.Open(ctx, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.BindMasterKey(ctx, e.credentials); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "future_schema":
				_, err = s.DB.Exec("INSERT INTO schema_migration VALUES('999_future.sql','now')")
			case "missing_lineage":
				_, err = s.DB.Exec("DELETE FROM schema_migration")
			case "foreign_key":
				if _, err = s.DB.Exec("PRAGMA foreign_keys=OFF"); err == nil {
					_, err = s.DB.Exec("INSERT INTO instance VALUES('orphan','missing','i-test','ecs','orphan','cn-shanghai','running','stopped','now')")
				}
			case "bad_credential":
				sealed, _ := master.Encrypt([]byte("wrong-key-fixture"), "test-owner")
				_, err = s.DB.Exec("INSERT INTO credential VALUES('test-owner',?,'now')", sealed)
			}
			if err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(dir, "payload.db")
			if err := s.SnapshotTo(ctx, payload); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(payload)
			if err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			if err := e.seal(ctx, &archive, bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			if _, err := Restore(ctx, bytes.NewReader(archive.Bytes()), testPassword, master, filepath.Join(parent, "restored"), time.Now()); err == nil {
				t.Fatal("invalid database restored", mode)
			}
			if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
				t.Fatal("invalid database left target", entries, err)
			}
		})
	}
}

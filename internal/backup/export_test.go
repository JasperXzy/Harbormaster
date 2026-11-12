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

func TestExportRekeysWALCredentialsAndClearsOnlyCopySessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	master, _ := security.NewBox(bytes.Repeat([]byte{7}, 32))
	if err := s.BindMasterKey(ctx, master); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	owner := "account/fixture/access-key"
	secret := []byte("private-fixture-secret-not-real")
	sealed, _ := master.Encrypt(secret, owner)
	if _, err := s.DB.Exec("INSERT INTO credential VALUES(?,?,'now')", owner, sealed); err != nil {
		t.Fatal(err)
	}
	telegram, _ := master.Encrypt([]byte("private-test-telegram-not-real"), model.TelegramCredentialOwner)
	if _, err := s.SaveTelegramChannel(ctx, telegram, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO session VALUES(x'01','2099-01-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "harbormaster.db-wal")); err != nil || info.Size() == 0 {
		t.Fatal("fixture must exercise committed WAL data", err)
	}
	var archive bytes.Buffer
	if err := Export(ctx, s, master, testPassword, &archive); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Bytes(), secret) || bytes.Contains(archive.Bytes(), []byte("SQLite format 3")) || bytes.Contains(archive.Bytes(), testPassword) {
		t.Fatal("plaintext in encrypted archive")
	}
	restoredDir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(restoredDir, "harbormaster.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	backupKey, err := Unseal(ctx, f, bytes.NewReader(archive.Bytes()), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := os.ReadFile(f.Name())
	if err != nil || bytes.Contains(decoded, secret) || bytes.Contains(decoded, sealed) {
		t.Fatal("credential was not reencrypted/compacted", err)
	}
	restored, err := store.Open(ctx, restoredDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var integrity string
	if err := restored.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	if err := restored.BindMasterKey(ctx, backupKey); err != nil {
		t.Fatal("backup needs old master key", err)
	}
	if err := restored.BindMasterKey(ctx, master); !errors.Is(err, store.ErrMasterKey) {
		t.Fatal("source key still opens backup copy", err)
	}
	var cipher []byte
	if err := restored.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", owner).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if plain, err := backupKey.Decrypt(cipher, owner); err != nil || !bytes.Equal(plain, secret) {
		t.Fatal("credential lost", err)
	}
	channel, _, err := restored.TelegramChannel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := backupKey.Decrypt(channel, model.TelegramCredentialOwner); err != nil || string(plain) != "private-test-telegram-not-real" {
		t.Fatal("channel credential lost", err)
	}
	var sessions int
	if err := restored.DB.QueryRow("SELECT count(*) FROM session").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("sessions included in backup copy", err)
	}
	if err := s.DB.QueryRow("SELECT count(*) FROM session").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("export revoked live sessions", err)
	}
	if err := s.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", owner).Scan(&cipher); err != nil || !bytes.Equal(cipher, sealed) {
		t.Fatal("export changed source credentials", err)
	}
	// Exercise cross-master recovery in a private test copy. Publication and
	// pending task handling remain separate from the credential transform.
	newMaster, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	if err := restored.ReencryptCopy(ctx, backupKey, newMaster); err != nil {
		t.Fatal(err)
	}
	if err := restored.BindMasterKey(ctx, newMaster); err != nil {
		t.Fatal(err)
	}
	if err := restored.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", owner).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if plain, err := newMaster.Decrypt(cipher, owner); err != nil || !bytes.Equal(plain, secret) {
		t.Fatal("new master cannot read restored credential", err)
	}
}

func TestExportFileDoesNotOverwriteOrPublishFailures(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	master, _ := security.NewBox(bytes.Repeat([]byte{1}, 32))
	if err := s.BindMasterKey(ctx, master); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.hmbak")
	wrong, _ := security.NewBox(bytes.Repeat([]byte{2}, 32))
	if err := ExportFile(ctx, s, wrong, testPassword, path); !errors.Is(err, store.ErrMasterKey) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("partial file left behind", entries, err)
	}
	if err := ExportFile(ctx, s, master, testPassword, path); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ExportFile(ctx, s, master, testPassword, path); !errors.Is(err, ErrOutputExists) {
		t.Fatal("existing backup overwritten", err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("existing file changed", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatal("temporary output leaked", entries, err)
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestProductionBootstrapAndResetDoNotLogCredentials(t *testing.T) {
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
	t.Setenv("HM_MASTER_KEY_FILE", "")
	t.Setenv("HM_SETUP_TOKEN", "cli-test-bootstrap-not-a-real-credential")
	t.Setenv("HM_SETUP_TOKEN_FILE", "")
	t.Setenv("HM_PUBLIC_URL", "https://panel.example")
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	// Cancel only after setup/server startup, preserving a live migration context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	logs := &stopOnStart{cancel: cancel}
	if code := Server(ctx, []string{"serve", "--data-dir", dir, "--listen", addr}, strings.NewReader(""), &out, logs); code != 0 {
		t.Fatal("serve failed", code, logs.String())
	}
	if !strings.Contains(logs.String(), "setup_required") || strings.Contains(logs.String(), "cli-test-bootstrap-not-a-real-credential") || strings.Contains(logs.String(), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))) || out.Len() != 0 {
		t.Fatal("bootstrap logging boundary failed")
	}
	s, err := store.Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec("INSERT INTO administrator VALUES(1,'$argon2id$test-placeholder','now')")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec("INSERT INTO session VALUES(x'01','2099-01-01T00:00:00Z')")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"UPDATE passkey_state SET password_enabled=0 WHERE id=1",
		"INSERT INTO passkey(id,credential_id,name,credential_json,created_at) VALUES('retained-key',x'01','fixture','{}','now')",
		"INSERT INTO passkey_ceremony(id,kind,binding_hash,epoch,name,session_json,expires_at) SELECT 'pending','login',zeroblob(32),epoch,'','{}','2099-01-01T00:00:00Z' FROM passkey_state WHERE id=1",
	} {
		if _, err = s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	password := "cli-reset-password-not-a-real-secret"
	var resetLogs bytes.Buffer
	if code := Server(context.Background(), []string{"reset-password", "--data-dir", dir}, strings.NewReader(password+"\n"), &out, &resetLogs); code != 0 {
		t.Fatal("reset failed", code)
	}
	if strings.Contains(resetLogs.String(), password) || !strings.Contains(resetLogs.String(), "sessions_revoked") {
		t.Fatal("reset logs contain secret or omit revocation")
	}
	s, err = store.Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err = s.DB.QueryRow("SELECT count(*) FROM session").Scan(&n); err != nil || n != 0 {
		t.Fatal("reset retained sessions", err)
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM passkey_ceremony").Scan(&n); err != nil || n != 0 {
		t.Fatal("reset retained challenges", err)
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM passkey WHERE id='retained-key'").Scan(&n); err != nil || n != 1 {
		t.Fatal("reset removed passkey", err)
	}
	var enabled bool
	if err = s.DB.QueryRow("SELECT password_enabled FROM passkey_state WHERE id=1").Scan(&enabled); err != nil || !enabled {
		t.Fatal("reset left password disabled", err)
	}
	var hash string
	if err = s.DB.QueryRow("SELECT password_hash FROM administrator WHERE id=1").Scan(&hash); err != nil || !security.VerifyPassword(hash, []byte(password)) {
		t.Fatal("reset password not persisted", err)
	}
}

type stopOnStart struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *stopOnStart) Write(b []byte) (int, error) {
	n, err := w.Buffer.Write(b)
	if bytes.Contains(b, []byte("server_started")) {
		w.cancel()
	}
	return n, err
}

func TestProductionCommandRejectsWrongMasterKeyBeforeWriting(t *testing.T) {
	t.Setenv("HM_MASTER_KEY_FILE", "")
	t.Setenv("HM_CONFIG", "")
	t.Setenv("HM_DEV", "false")
	firstKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{11}, 32))
	wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{12}, 32))
	t.Setenv("HM_MASTER_KEY", firstKey)
	dir := t.TempDir()
	var out, logs bytes.Buffer
	if code := Server(context.Background(), []string{"migrate", "--data-dir", dir}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatal(code)
	}
	s, err := store.Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := security.NewBox(bytes.Repeat([]byte{11}, 32))
	owner := "account/test-fixture/access-key"
	ciphertext, err := box.Encrypt([]byte("fixture-only-not-a-real-credential"), owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO credential VALUES(?,?,'2026-11-12T00:00:00Z')", owner, ciphertext); err != nil {
		t.Fatal(err)
	}
	s.Close()
	t.Setenv("HM_MASTER_KEY", wrongKey)
	logs.Reset()
	if code := Server(context.Background(), []string{"migrate", "--data-dir", dir}, strings.NewReader(""), &out, &logs); code == 0 {
		t.Fatal("production command accepted a key that cannot decrypt existing credentials")
	}
	if !strings.Contains(logs.String(), "master_key_verification_failed") || strings.Contains(logs.String(), wrongKey) || strings.Contains(logs.String(), firstKey) {
		t.Fatal("key verification did not fail safely")
	}
	t.Setenv("HM_MASTER_KEY", firstKey)
	logs.Reset()
	if code := Server(context.Background(), []string{"migrate", "--data-dir", dir}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatal("correct key failed after rejected attempt", code)
	}
}

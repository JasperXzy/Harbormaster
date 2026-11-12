package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/backup"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestBackupDownloadAuthorizationAndRestore(t *testing.T) {
	p := testPanel(t)
	body := map[string]string{"backup_password": "api-backup-password-fixture"}
	if w := panelRequest(p, "POST", "/backups", body, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := setupPanel(t, p)
	if w := panelRequest(p, "POST", "/backups", body, cookie, ""); w.Code != 403 {
		t.Fatal("CSRF missing", w.Code)
	}
	if w := panelRequest(p, "POST", "/backups", body, cookie, csrf); w.Code != 503 {
		t.Fatal("no encryption key accepted", w.Code)
	}
	box, _ := security.NewBox(bytes.Repeat([]byte{7}, 32))
	if err := p.s.BindMasterKey(context.Background(), box); err != nil {
		t.Fatal(err)
	}
	p.engine = &collect.Engine{Store: p.s, Box: box}
	owner := "account/api-backup/access-key"
	cipher, _ := box.Encrypt([]byte("private-api-backup-fixture"), owner)
	if _, err := p.s.DB.Exec("INSERT INTO credential VALUES(?,?,'now')", owner, cipher); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]string{{"backup_password": "short"}, {"backup_password": strings.Repeat("x", 1025)}, {"backup_password": body["backup_password"], "unexpected": "secret"}} {
		if w := panelRequest(p, "POST", "/backups", invalid, cookie, csrf); w.Code != 422 {
			t.Fatal(w.Code)
		}
	}
	p.backups.Lock()
	w := panelRequest(p, "POST", "/backups", body, cookie, csrf)
	p.backups.Unlock()
	if w.Code != 429 {
		t.Fatal("concurrent download accepted", w.Code)
	}
	p.hashing.Lock()
	w = panelRequest(p, "POST", "/backups", body, cookie, csrf)
	p.hashing.Unlock()
	if w.Code != 429 {
		t.Fatal("concurrent expensive KDF accepted", w.Code)
	}
	w = panelRequest(p, "POST", "/backups", body, cookie, csrf)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatal("download framing", w.Code, w.Header())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(body["backup_password"])) || bytes.Contains(w.Body.Bytes(), []byte("private-api-backup-fixture")) {
		t.Fatal("plaintext in attachment")
	}
	newKey, _ := security.NewBox(bytes.Repeat([]byte{8}, 32))
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := backup.Restore(context.Background(), bytes.NewReader(w.Body.Bytes()), []byte(body["backup_password"]), newKey, target, time.Now()); err != nil {
		t.Fatal("download cannot restore", err)
	}
	s, err := store.Open(context.Background(), target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BindMasterKey(context.Background(), newKey); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := s.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", owner).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if plain, err := newKey.Decrypt(sealed, owner); err != nil || string(plain) != "private-api-backup-fixture" {
		t.Fatal("download lost credentials", err)
	}
	if w := panelRequest(p, "GET", "/session", nil, cookie, ""); w.Code != 200 {
		t.Fatal("download revoked live session", w.Code)
	}
	if w := panelRequest(p, "GET", "/backups", nil, cookie, ""); w.Code != 405 {
		t.Fatal("GET download accepted", w.Code)
	}
}

type onRead struct {
	io.Reader
	first func()
}

func (r *onRead) Read(p []byte) (int, error) {
	if r.first != nil {
		f := r.first
		r.first = nil
		f()
	}
	return r.Reader.Read(p)
}

func TestBackupDownloadRevalidatesSessionAndFailsBeforeAttachment(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	box, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	if err := p.s.BindMasterKey(context.Background(), box); err != nil {
		t.Fatal(err)
	}
	p.engine = &collect.Engine{Store: p.s, Box: box}
	input, _ := json.Marshal(map[string]string{"backup_password": "session-revocation-backup-fixture"})
	body := &onRead{Reader: bytes.NewReader(input), first: func() {
		if _, err := p.s.DB.Exec("DELETE FROM session"); err != nil {
			t.Fatal(err)
		}
	}}
	r := httptest.NewRequest("POST", "https://panel.example/api/v1/backups", body)
	r.RemoteAddr = "203.0.113.10:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://panel.example")
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 401 || w.Header().Get("Content-Disposition") != "" || !json.Valid(w.Body.Bytes()) {
		t.Fatal("revoked session got attachment", w.Code, w.Header())
	}
	// Obtain a valid session again, then make snapshot-key verification fail.
	w = panelRequest(p, "POST", "/session", map[string]string{"password": testPassword}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookie = w.Result().Cookies()[0]
	p.engine.Box, _ = security.NewBox(bytes.Repeat([]byte{10}, 32))
	w = panelRequest(p, "POST", "/backups", map[string]string{"backup_password": "snapshot-failure-backup-fixture"}, cookie, session.CSRF)
	if w.Code != 503 || w.Header().Get("Content-Disposition") != "" || !json.Valid(w.Body.Bytes()) {
		t.Fatal("failure returned a partial attachment", w.Code, w.Header())
	}
}

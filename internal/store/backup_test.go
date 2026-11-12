package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func TestCopyReencryptionRollsBackOnInvalidChannelCiphertext(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := security.NewBox(bytes.Repeat([]byte{2}, 32))
	newKey, _ := security.NewBox(bytes.Repeat([]byte{3}, 32))
	if err := s.BindMasterKey(ctx, old); err != nil {
		t.Fatal(err)
	}
	owner := "account/test/access-key"
	cipher, _ := old.Encrypt([]byte("test-only-secret"), owner)
	if _, err := s.DB.Exec("INSERT INTO credential VALUES(?,?,'now')", owner, cipher); err != nil {
		t.Fatal(err)
	}
	badChannel, _ := old.Encrypt([]byte("test-channel"), "wrong-owner")
	if _, err := s.SaveTelegramChannel(ctx, badChannel, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO session VALUES(x'01','2099-01-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReencryptCopy(ctx, old, newKey); !errors.Is(err, ErrMasterKey) {
		t.Fatal(err)
	}
	if err := s.BindMasterKey(ctx, old); err != nil {
		t.Fatal("verifier changed despite rollback", err)
	}
	var retained []byte
	if err := s.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", owner).Scan(&retained); err != nil || !bytes.Equal(retained, cipher) {
		t.Fatal("credential partially rekeyed", err)
	}
	var sessions int
	if err := s.DB.QueryRow("SELECT count(*) FROM session").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatal("session deletion escaped rollback", err)
	}
	goodChannel, _ := old.Encrypt([]byte("test-channel"), model.TelegramCredentialOwner)
	if _, err := s.SaveTelegramChannel(ctx, goodChannel, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReencryptCopy(ctx, old, newKey); err != nil {
		t.Fatal(err)
	}
	if err := s.BindMasterKey(ctx, newKey); err != nil {
		t.Fatal(err)
	}
}

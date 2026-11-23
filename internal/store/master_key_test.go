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

func TestMasterKeyBindingRejectsAnotherKeyAndTamperedVerifier(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	correct, _ := security.NewBox(bytes.Repeat([]byte{1}, 32))
	wrong, _ := security.NewBox(bytes.Repeat([]byte{2}, 32))
	if err := s.BindMasterKey(ctx, correct); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := s.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", masterVerifierOwner).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := s.BindMasterKey(ctx, wrong); !errors.Is(err, ErrMasterKey) {
		t.Fatal("wrong key accepted", err)
	}
	if err := s.BindMasterKey(ctx, correct); err != nil {
		t.Fatal(err)
	}
	var unchanged []byte
	if err := s.DB.QueryRow("SELECT ciphertext FROM credential WHERE owner_field=?", masterVerifierOwner).Scan(&unchanged); err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("verification rewrote verifier")
	}
	tampered := bytes.Clone(original)
	tampered[len(tampered)-1] ^= 1
	if _, err := s.DB.Exec("UPDATE credential SET ciphertext=? WHERE owner_field=?", tampered, masterVerifierOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.BindMasterKey(ctx, correct); !errors.Is(err, ErrMasterKey) {
		t.Fatal("tampered verifier was replaced", err)
	}
}
func TestLegacyBindingValidatesAccountAndTelegramCiphertextsBeforeAdoption(t *testing.T) {
	for _, withTelegram := range []bool{false, true} {
		t.Run(map[bool]string{false: "account", true: "telegram"}[withTelegram], func(t *testing.T) {
			s := notificationStore(t)
			ctx := context.Background()
			correct, _ := security.NewBox(bytes.Repeat([]byte{3}, 32))
			wrong, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
			owner := model.CredentialOwner("fixture")
			if withTelegram {
				owner = model.TelegramCredentialOwner
			}
			ciphertext, err := correct.Encrypt([]byte("test-only-not-a-real-credential"), owner)
			if err != nil {
				t.Fatal(err)
			}
			if withTelegram {
				_, err = s.SaveTelegramChannel(ctx, ciphertext, 0, model.AuditDetails{}, time.Now())
			} else {
				_, err = s.DB.Exec("INSERT INTO credential VALUES(?,?,'now')", owner, ciphertext)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.BindMasterKey(ctx, wrong); !errors.Is(err, ErrMasterKey) {
				t.Fatal("legacy credentials accepted a different key", err)
			}
			var count int
			if err = s.DB.QueryRow("SELECT count(*) FROM credential WHERE owner_field=?", masterVerifierOwner).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed adoption wrote verifier")
			}
			if err = s.BindMasterKey(ctx, correct); err != nil {
				t.Fatal(err)
			}
			if err = s.DB.QueryRow("SELECT count(*) FROM credential WHERE owner_field=?", masterVerifierOwner).Scan(&count); err != nil || count != 1 {
				t.Fatal("legacy database not bound")
			}
		})
	}
}
func TestLegacyMixedKeysCannotBeAdopted(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	first, _ := security.NewBox(bytes.Repeat([]byte{5}, 32))
	second, _ := security.NewBox(bytes.Repeat([]byte{6}, 32))
	for i, box := range []*security.Box{first, second} {
		owner := model.CredentialOwner([]string{"one", "two"}[i])
		sealed, err := box.Encrypt([]byte("fixture"), owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec("INSERT INTO credential VALUES(?,?,'now')", owner, sealed); err != nil {
			t.Fatal(err)
		}
	}
	for _, box := range []*security.Box{first, second} {
		if err := s.BindMasterKey(ctx, box); !errors.Is(err, ErrMasterKey) {
			t.Fatal("mixed credentials were adopted", err)
		}
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM credential WHERE owner_field=?", masterVerifierOwner).Scan(&count); err != nil || count != 0 {
		t.Fatal("mixed-key failure changed binding")
	}
}

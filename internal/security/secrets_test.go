package security

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryption(t *testing.T) {
	b, err := NewBox(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("synthetic-secret")
	a, _ := b.Encrypt(plain, "account/a/key")
	c, _ := b.Encrypt(plain, "account/a/key")
	if bytes.Equal(a, c) || bytes.Contains(a, plain) {
		t.Fatal("ciphertext must be randomized")
	}
	p, err := b.Decrypt(a, "account/a/key")
	if err != nil || !bytes.Equal(p, plain) {
		t.Fatal("round trip failed")
	}
	if _, err = b.Decrypt(a, "account/b/key"); err == nil {
		t.Fatal("row swap accepted")
	}
	a[len(a)-1] ^= 1
	if _, err = b.Decrypt(a, "account/a/key"); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestKeyLocation(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	inside := filepath.Join(data, "key")
	_ = os.WriteFile(inside, []byte(key), 0600)
	t.Setenv("HM_MASTER_KEY", "")
	t.Setenv("HM_MASTER_KEY_FILE", inside)
	if _, err := LoadBox(data); err == nil {
		t.Fatal("accepted key inside data")
	}
	link := filepath.Join(root, "key-link")
	if err := os.Symlink(inside, link); err != nil {
		t.Skip(err)
	}
	t.Setenv("HM_MASTER_KEY_FILE", link)
	if _, err := LoadBox(data); err == nil {
		t.Fatal("accepted symlink into data")
	}
	out := filepath.Join(root, "external-key")
	_ = os.WriteFile(out, []byte(key), 0600)
	t.Setenv("HM_MASTER_KEY_FILE", out)
	if _, err := LoadBox(data); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HM_MASTER_KEY", key)
	if _, err := LoadBox(data); err == nil {
		t.Fatal("ambiguous sources accepted")
	}
}

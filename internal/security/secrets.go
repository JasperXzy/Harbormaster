package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Box holds only an AES-256 key schedule; never log it or serialized credentials.
type Box struct{ aead cipher.AEAD }

func NewBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must decode to 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("invalid master key")
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Box{a}, nil
}

// Encrypt binds ciphertext to a stable owner/field identifier, preventing row swaps.
func (b *Box) Encrypt(plaintext []byte, ownerField string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{1}, nonce...)
	return b.aead.Seal(out, nonce, plaintext, []byte(ownerField)), nil
}

func (b *Box) Decrypt(data []byte, ownerField string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(data) < 1+n+b.aead.Overhead() || data[0] != 1 {
		return nil, errors.New("invalid encrypted credential")
	}
	p, err := b.aead.Open(nil, data[1:1+n], data[1+n:], []byte(ownerField))
	if err != nil {
		return nil, errors.New("credential authentication failed")
	}
	return p, nil
}

// LoadBox accepts exactly one source. File paths are resolved before checking the
// data-directory boundary, including symbolic links and not-yet-created directories.
func LoadBox(dataDir string) (*Box, error) {
	value, file := os.Getenv("HM_MASTER_KEY"), os.Getenv("HM_MASTER_KEY_FILE")
	if (value == "") == (file == "") {
		return nil, errors.New("provide exactly one of HM_MASTER_KEY or HM_MASTER_KEY_FILE")
	}
	if file != "" {
		f, err := canonical(file)
		if err != nil {
			return nil, errors.New("cannot resolve master key file")
		}
		d, err := canonical(dataDir)
		if err != nil {
			return nil, errors.New("cannot resolve data directory")
		}
		rel, err := filepath.Rel(d, f)
		if err != nil {
			return nil, errors.New("cannot validate key location")
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, errors.New("master key file must be outside data directory")
		}
		info, err := os.Stat(f)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
			return nil, errors.New("invalid master key file")
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, errors.New("cannot read master key file")
		}
		value = strings.TrimSpace(string(b))
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("master key must use standard base64")
	}
	defer clear(key)
	return NewBox(key)
}

func canonical(path string) (string, error) {
	a, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(a)
	if err == nil {
		return r, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(a)
	if parent == a {
		return "", err
	}
	r, err = canonical(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(r, filepath.Base(a)), nil
}

func MaskAccessKeyID(id string) string {
	if len(id) < 9 {
		return "***"
	}
	return id[:4] + "***" + id[len(id)-4:]
}

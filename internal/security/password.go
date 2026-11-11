package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

func HashPassword(password []byte) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must be 12 to 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey(password, salt, 3, 64*1024, 1, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(h)), nil
}

func VerifyPassword(encoded string, password []byte) bool {
	const prefix = "$argon2id$v=19$m=65536,t=3,p=1$"
	if !strings.HasPrefix(encoded, prefix) || len(password) > 1024 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(encoded, prefix), "$")
	if len(parts) != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey(password, salt, 3, 64*1024, 1, 32)
	defer clear(got)
	return subtle.ConstantTimeCompare(got, want) == 1
}

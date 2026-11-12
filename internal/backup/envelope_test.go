package backup

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

var testPassword = []byte("backup-test-password-not-real")

func TestEnvelopeBoundariesAndIndependentKeys(t *testing.T) {
	ctx := context.Background()
	e, err := newEnvelope(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	other, err := newEnvelope(testPassword)
	if err != nil || e.header == other.header {
		t.Fatal("backup salts reused", err)
	}
	for _, size := range []int{0, 1, chunkSize, chunkSize + 1, 2*chunkSize + 17} {
		plain := bytes.Repeat([]byte{0xa7}, size)
		var archive, decoded bytes.Buffer
		if err := e.seal(ctx, &archive, bytes.NewReader(plain)); err != nil {
			t.Fatal(err)
		}
		box, err := Unseal(ctx, &decoded, bytes.NewReader(archive.Bytes()), testPassword)
		if err != nil || !bytes.Equal(plain, decoded.Bytes()) {
			t.Fatal("round trip", size, err)
		}
		credential, err := e.credentials.Encrypt([]byte("fixture credential"), "test-owner")
		if err != nil {
			t.Fatal(err)
		}
		if result, err := box.Decrypt(credential, "test-owner"); err != nil || string(result) != "fixture credential" {
			t.Fatal("wrong backup credential key", err)
		}
		if _, err := e.outer.Decrypt(credential, "test-owner"); err == nil {
			t.Fatal("file and credential keys not separated")
		}
		if _, err := other.credentials.Decrypt(credential, "test-owner"); err == nil {
			t.Fatal("independent backup has same key")
		}
	}
}

func TestEnvelopeRejectsTamperTruncationReorderAndWrongPassword(t *testing.T) {
	ctx := context.Background()
	e, err := newEnvelope(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := e.seal(ctx, &archive, bytes.NewReader(bytes.Repeat([]byte("sensitive-fixture"), 20000))); err != nil {
		t.Fatal(err)
	}
	original := archive.Bytes()
	frameLength := 4 + int(binary.BigEndian.Uint32(original[24:28]))
	for _, mode := range []string{"magic", "salt", "ciphertext", "truncate", "missing_end", "trailing", "size", "swap", "duplicate", "password"} {
		t.Run(mode, func(t *testing.T) {
			data := append([]byte{}, original...)
			password := testPassword
			switch mode {
			case "magic":
				data[0] ^= 1
			case "salt":
				data[8] ^= 1
			case "ciphertext":
				data[40] ^= 1
			case "truncate":
				data = data[:len(data)-1]
			case "missing_end":
				data = data[:len(data)-4-boxOverhead]
			case "trailing":
				data = append(data, 1)
			case "size":
				binary.BigEndian.PutUint32(data[24:28], 0xffffffff)
			case "swap":
				copy(data[24:24+frameLength], original[24+frameLength:24+2*frameLength])
				copy(data[24+frameLength:24+2*frameLength], original[24:24+frameLength])
			case "duplicate":
				copy(data[24+frameLength:24+2*frameLength], original[24:24+frameLength])
			case "password":
				password = []byte("a-different-test-password")
			}
			if _, err := Unseal(ctx, io.Discard, bytes.NewReader(data), password); !errors.Is(err, ErrFormat) {
				t.Fatal("invalid archive accepted", mode, err)
			}
		})
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestEnvelopeRejectsInvalidPasswordCancellationAndShortWrites(t *testing.T) {
	for _, password := range [][]byte{[]byte("short"), bytes.Repeat([]byte{'x'}, 1025)} {
		if _, err := newEnvelope(password); !errors.Is(err, ErrPassword) {
			t.Fatal(err)
		}
	}
	e, err := newEnvelope(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.seal(context.Background(), shortWriter{}, bytes.NewReader(nil)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.seal(ctx, io.Discard, bytes.NewReader(nil)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Unseal(ctx, io.Discard, bytes.NewReader(nil), testPassword); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

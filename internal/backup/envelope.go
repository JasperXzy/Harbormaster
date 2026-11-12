// Package backup creates password-encrypted database snapshots. Decrypted
// output is a private staging artifact, never a live database until validated.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strconv"

	"github.com/JasperXzy/harbormaster/internal/security"
	"golang.org/x/crypto/argon2"
)

const magic = "HMBK0001"
const chunkSize = 64 << 10
const boxOverhead = 29    // security.Box: version + 12-byte nonce + 16-byte GCM tag.
const maxChunks = 1 << 20 // Format v1 supports up to 64 GiB with bounded nonce use.

var ErrFormat = errors.New("invalid_encrypted_backup")
var ErrPassword = errors.New("backup_password_must_be_12_to_1024_bytes")

type envelope struct {
	header             [24]byte // magic and independent 128-bit password salt.
	outer, credentials *security.Box
}

func derive(password []byte, header [24]byte) (*envelope, error) {
	if len(password) < 12 || len(password) > 1024 {
		return nil, ErrPassword
	}
	// Fixed parameters prevent a modified header from requesting unbounded KDF
	// work. Separate halves are used for file and credential encryption.
	key := argon2.IDKey(password, header[8:], 3, 64*1024, 1, 64)
	defer clear(key)
	outer, err := security.NewBox(key[:32])
	if err != nil {
		return nil, err
	}
	credentials, err := security.NewBox(key[32:])
	if err != nil {
		return nil, err
	}
	return &envelope{header: header, outer: outer, credentials: credentials}, nil
}

func newEnvelope(password []byte) (*envelope, error) {
	var header [24]byte
	copy(header[:], magic)
	if _, err := rand.Read(header[8:]); err != nil {
		return nil, err
	}
	return derive(password, header)
}
func (e *envelope) owner(sequence uint64, final bool) string {
	kind := "/chunk/"
	if final {
		kind = "/end/"
	}
	return "harbormaster/backup/" + hex.EncodeToString(e.header[:]) + kind + strconv.FormatUint(sequence, 10)
}
func writeAll(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
func (e *envelope) frame(out io.Writer, plain []byte, sequence uint64, final bool) error {
	sealed, err := e.outer.Encrypt(plain, e.owner(sequence, final))
	if err != nil {
		return err
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
	if err := writeAll(out, length[:]); err != nil {
		return err
	}
	return writeAll(out, sealed)
}
func (e *envelope) seal(ctx context.Context, out io.Writer, in io.Reader) error {
	if err := writeAll(out, e.header[:]); err != nil {
		return err
	}
	buf := make([]byte, chunkSize)
	defer clear(buf)
	var sequence uint64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(in, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n > 0 {
			if sequence >= maxChunks {
				return ErrFormat
			}
			if err := e.frame(out, buf[:n], sequence, false); err != nil {
				return err
			}
			sequence++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return e.frame(out, nil, sequence, true)
		}
	}
}

// Unseal authenticates each bounded record plus a mandatory final record. The
// caller must discard staged output on any error, including trailing data, and
// must not open it as SQLite until this function returns successfully. Returned
// credentials decrypt only this backup's credential rows, not the source DB.
func Unseal(ctx context.Context, out io.Writer, in io.Reader, password []byte) (*security.Box, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var header [24]byte
	if _, err := io.ReadFull(in, header[:]); err != nil || string(header[:8]) != magic {
		return nil, ErrFormat
	}
	e, err := derive(password, header)
	if err != nil {
		return nil, err
	}
	for sequence := uint64(0); sequence <= maxChunks; sequence++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var length [4]byte
		if _, err := io.ReadFull(in, length[:]); err != nil {
			return nil, ErrFormat
		}
		size := binary.BigEndian.Uint32(length[:])
		if size < boxOverhead || size > chunkSize+boxOverhead {
			return nil, ErrFormat
		}
		sealed := make([]byte, int(size))
		if _, err := io.ReadFull(in, sealed); err != nil {
			return nil, ErrFormat
		}
		final := size == boxOverhead
		plain, err := e.outer.Decrypt(sealed, e.owner(sequence, final))
		if err != nil {
			return nil, ErrFormat
		}
		if final {
			clear(plain)
			var trailing [1]byte
			if _, err := io.ReadFull(in, trailing[:]); err != io.EOF {
				return nil, ErrFormat
			}
			return e.credentials, nil
		}
		if sequence == maxChunks {
			clear(plain)
			return nil, ErrFormat
		}
		err = writeAll(out, plain)
		clear(plain)
		if err != nil {
			return nil, err
		}
	}
	return nil, ErrFormat
}

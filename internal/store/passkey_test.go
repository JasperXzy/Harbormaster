package store

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func passkeyStoreFixture(t *testing.T) (*Store, PasskeyCeremony, Passkey) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	binding := bytes.Repeat([]byte{1}, 32)
	if _, err = s.DB.Exec("INSERT INTO administrator VALUES(1,'$argon2id$fixture',?)", timestamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO session VALUES(?,?)", binding, timestamp(now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	state, err := s.Passkeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := PasskeyCeremony{ID: "test-challenge", Kind: "registration", Name: "fixture", SessionJSON: "{}", BindingHash: binding, Epoch: state.Epoch, ExpiresAt: now.Add(time.Minute)}
	k := Passkey{ID: "fixture-key", CredentialID: []byte("public-credential-id"), CredentialJSON: `{"publicKey":"fixture"}`}
	if err = s.RegisterPasskey(ctx, c, k, now); err != nil {
		t.Fatal(err)
	}
	return s, c, k
}
func TestPasskeyChallengeConsumedExactlyOnce(t *testing.T) {
	s, c, _ := passkeyStoreFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SavePasskeyCeremony(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakePasskeyCeremony(ctx, c.ID, c.Kind, bytes.Repeat([]byte{2}, 32), now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("wrong browser accepted", err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := s.TakePasskeyCeremony(ctx, c.ID, c.Kind, c.BindingHash, now)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrUnauthorized) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("non-atomic consume", successes.Load())
	}
}
func TestPasskeyCommitRechecksRevocationAndSnapshot(t *testing.T) {
	for _, change := range []string{"delete", "counter", "reset", "restore", "expire"} {
		t.Run(change, func(t *testing.T) {
			s, c, k := passkeyStoreFixture(t)
			ctx := context.Background()
			now := time.Now()
			c.Kind = "login"
			if err := s.SavePasskeyCeremony(ctx, c, now); err != nil {
				t.Fatal(err)
			}
			consumed, err := s.TakePasskeyCeremony(ctx, c.ID, c.Kind, c.BindingHash, now)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "delete":
				err = s.DeletePasskey(ctx, k.ID, c.BindingHash, now)
			case "counter":
				err = s.CompletePasskeyLogin(ctx, consumed, k, `{"publicKey":"fixture","counter":2}`, bytes.Repeat([]byte{3}, 32), now.Add(time.Hour), now)
			case "reset":
				err = s.ResetPassword(ctx, "$argon2id$new-fixture")
			case "restore":
				_, err = s.PrepareRestoredCopy(ctx, now)
			case "expire":
				now = c.ExpiresAt
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.CompletePasskeyLogin(ctx, consumed, k, `{"counter":1}`, bytes.Repeat([]byte{4}, 32), now.Add(time.Hour), now); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("stale verification committed", err)
			}
			var n int
			if err = s.DB.QueryRow("SELECT count(*) FROM session WHERE token_hash=?", bytes.Repeat([]byte{4}, 32)).Scan(&n); err != nil || n != 0 {
				t.Fatal("session escaped failed transaction", n, err)
			}
		})
	}
}
func TestPasskeyRegistrationRechecksSessionAndEpoch(t *testing.T) {
	for _, change := range []string{"logout", "reset", "expire"} {
		t.Run(change, func(t *testing.T) {
			s, c, _ := passkeyStoreFixture(t)
			ctx := context.Background()
			now := time.Now()
			var err error
			switch change {
			case "logout":
				_, err = s.DB.Exec("DELETE FROM session")
			case "reset":
				err = s.ResetPassword(ctx, "$argon2id$new-fixture")
			case "expire":
				now = c.ExpiresAt
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.RegisterPasskey(ctx, c, Passkey{ID: "new", CredentialID: []byte("new"), CredentialJSON: "{}"}, now)
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatal("stale registration committed", err)
			}
		})
	}
}
func TestPasskeyRestoreClearsCeremoniesPreservesPublicKeys(t *testing.T) {
	s, c, _ := passkeyStoreFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SavePasskeyCeremony(ctx, c, now); err != nil {
		t.Fatal(err)
	}
	before, err := s.Passkeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareRestoredCopy(ctx, now); err != nil {
		t.Fatal(err)
	}
	after, err := s.Passkeys(ctx)
	if err != nil || len(after.Keys) != 1 || !bytes.Equal(before.UserHandle, after.UserHandle) || after.Epoch <= before.Epoch {
		t.Fatal("restore lost key or failed epoch fence", err)
	}
	if _, err = s.TakePasskeyCeremony(ctx, c.ID, c.Kind, c.BindingHash, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("challenge survived restore", err)
	}
}

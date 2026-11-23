package store

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPasswordPolicyFencesVerifiedPasswordAndConcurrentLastKeyDeletion(t *testing.T) {
	s, c, k := passkeyStoreFixture(t)
	ctx := context.Background()
	now := time.Now()
	eligible := map[string]string{k.ID: k.CredentialJSON}
	if err := s.SetPasswordLogin(ctx, false, c.BindingHash, eligible, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "$argon2id$fixture", bytes.Repeat([]byte{9}, 32), now.Add(time.Hour), now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("previously verified password created a session", err)
	}
	second := Passkey{ID: "second-key", CredentialID: []byte("second-public-id"), CredentialJSON: `{"publicKey":"second"}`}
	if err := s.RegisterPasskey(ctx, c, second, now); err != nil {
		t.Fatal(err)
	}
	eligible[second.ID] = second.CredentialJSON
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{k.ID, second.ID} {
		wg.Go(func() { outcomes <- s.DeletePasskey(ctx, id, c.BindingHash, eligible, now) })
	}
	wg.Wait()
	close(outcomes)
	success, refused := 0, 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if errors.Is(err, ErrLastPasskey) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	state, err := s.Passkeys(ctx)
	if err != nil || state.PasswordEnabled || len(state.Keys) != 1 || success != 1 || refused != 1 {
		t.Fatal("last-key invariant broken", state, success, refused, err)
	}
}
func TestPasswordDisableAndDeletionCannotLeaveNoLoginMethod(t *testing.T) {
	s, c, k := passkeyStoreFixture(t)
	ctx := context.Background()
	now := time.Now()
	eligible := map[string]string{k.ID: k.CredentialJSON}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	wg.Go(func() { outcomes <- s.SetPasswordLogin(ctx, false, c.BindingHash, eligible, now) })
	wg.Go(func() { outcomes <- s.DeletePasskey(ctx, k.ID, c.BindingHash, eligible, now) })
	wg.Wait()
	close(outcomes)
	success := 0
	for err := range outcomes {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrLastPasskey) && !errors.Is(err, ErrPasskeyRequired) {
			t.Fatal(err)
		}
	}
	state, err := s.Passkeys(ctx)
	if err != nil || success != 1 || (!state.PasswordEnabled && len(state.Keys) == 0) {
		t.Fatal(state, success, err)
	}
}
func TestPasswordPolicyRechecksSessionAndEligibleSnapshot(t *testing.T) {
	s, c, k := passkeyStoreFixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.SetPasswordLogin(ctx, false, c.BindingHash, map[string]string{k.ID: "stale-public-record"}, now); !errors.Is(err, ErrPasskeyRequired) {
		t.Fatal("stale candidate accepted", err)
	}
	if _, err := s.DB.Exec("DELETE FROM session"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasswordLogin(ctx, false, c.BindingHash, map[string]string{k.ID: k.CredentialJSON}, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked session changed policy", err)
	}
}

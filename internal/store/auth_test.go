package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSetupRotationExpiryAndConcurrency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	now := time.Now().UTC()
	old := []byte(strings.Repeat("a", 32))
	replacement := []byte(strings.Repeat("b", 32))
	if err = a.PrepareSetup(ctx, old, now); err != nil {
		t.Fatal(err)
	}
	if err = a.PrepareSetup(ctx, old, now.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = a.CheckSetup(ctx, old, now.Add(24*time.Hour)); !errors.Is(err, ErrSetupToken) {
		t.Fatal("restart extended token expiry", err)
	}
	now = now.Add(25 * time.Hour)
	if err = a.PrepareSetup(ctx, replacement, now); err != nil {
		t.Fatal(err)
	}
	if err = a.CheckSetup(ctx, old, now); !errors.Is(err, ErrSetupToken) {
		t.Fatal("old bootstrap still works")
	}
	if err = a.CheckSetup(ctx, replacement, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			h := sha256.Sum256([]byte(fmt.Sprint("session", i)))
			results <- s.CompleteSetup(ctx, replacement, "$argon2id$test-placeholder", "Asia/Shanghai", h[:], now.Add(time.Hour), now)
		}(i, s)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrInitialized) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("setup was not single-use", success, conflict)
	}
	if err = a.PrepareSetup(ctx, old, now); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"administrator": 1, "session": 1, "setup_credential": 0} {
		var n int
		if err = a.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatal(table, n, err)
		}
	}
}

func TestAuthRateLimitAcrossDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Now()
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		wait, err := s.TakeAuthAttempt(ctx, "203.0.113.1", now)
		if err != nil || wait != 0 {
			t.Fatal(wait, err)
		}
	}
	s.Close()
	s, err = Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if wait, err := s.TakeAuthAttempt(ctx, "203.0.113.1", now); err != nil || wait != 300 {
		t.Fatal("rate limit did not survive restart", wait, err)
	}
	for i := 0; i < 25; i++ {
		if wait, err := s.TakeAuthAttempt(ctx, fmt.Sprintf("198.51.100.%d", i), now); err != nil || wait != 0 {
			t.Fatal(wait, err)
		}
	}
	if wait, err := s.TakeAuthAttempt(ctx, "192.0.2.1", now); err != nil || wait != 300 {
		t.Fatal("global limit bypassed", wait, err)
	}
	if wait, err := s.TakeAuthAttempt(ctx, "203.0.113.1", now.Add(5*time.Minute)); err != nil || wait != 0 {
		t.Fatal("window did not expire", wait, err)
	}
}

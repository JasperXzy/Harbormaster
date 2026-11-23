package store

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func heartbeatStore(t *testing.T) (*Store, *security.Box, model.HeartbeatSettings) {
	t.Helper()
	s := openSeededTestStore(t)
	box, _ := security.NewBox(bytes.Repeat([]byte{5}, 32))
	cipher, err := box.Encrypt([]byte("https://monitor.example/private-test-token"), model.HeartbeatCredentialOwner)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.SaveHeartbeat(context.Background(), 0, true, 60, &cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, box, state
}
func TestHeartbeatLeaseTestDedupAndRevisionFencing(t *testing.T) {
	s, _, state := heartbeatStore(t)
	ctx := context.Background()
	now := time.Now()
	j, err := s.EnqueueHeartbeatTest(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	dup, err := s.EnqueueHeartbeatTest(ctx, now)
	if err != nil || dup.ID != j.ID {
		t.Fatal("test not deduplicated", dup, err)
	}
	if _, err = s.ClaimCollectionJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("collection consumed heartbeat", err)
	}
	claim, err := s.ClaimHeartbeat(ctx, now)
	if err != nil || claim.Job == nil || claim.Job.ID != j.ID {
		t.Fatal(claim, err)
	}
	if _, err = s.ClaimHeartbeat(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("concurrent ping claimed", err)
	}
	if err = s.FinishJob(ctx, *claim.Job, nil, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("generic completion bypassed policy", err)
	}
	if _, err = s.SaveHeartbeat(ctx, state.Revision, false, 60, nil, now); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthorizeHeartbeat(ctx, claim, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	if err = s.FinishHeartbeat(ctx, claim, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("old worker replaced new status", err)
	}
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "failed" || job.Error.Code != "heartbeat_configuration_changed" {
		t.Fatal(job, err)
	}
}
func TestHeartbeatInterruptedTestIsNotReplayed(t *testing.T) {
	s, _, state := heartbeatStore(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := s.SaveHeartbeat(ctx, state.Revision, false, 60, nil, now); err != nil {
		t.Fatal(err)
	}
	j, err := s.EnqueueHeartbeatTest(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimHeartbeat(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimHeartbeat(ctx, claim.ExpiresAt.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired test replayed", err)
	}
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "failed" || job.Error.Code != "heartbeat_test_interrupted" || job.Attempts != 1 {
		t.Fatal(job, err)
	}
}
func TestHeartbeatCredentialsRekeyAndRestoreLease(t *testing.T) {
	s, old, _ := heartbeatStore(t)
	ctx := context.Background()
	now := time.Now()
	j, err := s.EnqueueHeartbeatTest(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimHeartbeat(ctx, now); err != nil {
		t.Fatal(err)
	}
	next, _ := security.NewBox(bytes.Repeat([]byte{6}, 32))
	if err = s.ReencryptCopy(ctx, old, next); err != nil {
		t.Fatal(err)
	}
	cipher, state, err := s.HeartbeatSettings(ctx)
	if err != nil || !state.Enabled {
		t.Fatal(state, err)
	}
	raw, err := next.Decrypt(cipher, model.HeartbeatCredentialOwner)
	if err != nil || string(raw) != "https://monitor.example/private-test-token" {
		t.Fatal("heartbeat credential lost on rekey", err)
	}
	clear(raw)
	if bytes.Contains(cipher, []byte("private-test-token")) {
		t.Fatal("URL stored plaintext")
	}
	if _, err = s.PrepareRestoredCopy(ctx, now); err != nil {
		t.Fatal(err)
	}
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "failed" || job.Error.Code != "backup_restored" {
		t.Fatal(job, err)
	}
	var leases int
	if err = s.DB.QueryRow("SELECT lease_until FROM heartbeat_settings").Scan(&leases); err != nil || leases != 0 {
		t.Fatal("lease survived restore", leases, err)
	}
}
func TestHeartbeatOnlyOneConcurrentClaimReceivesDuePing(t *testing.T) {
	s, _, _ := heartbeatStore(t)
	ctx := context.Background()
	now := time.Now()
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Go(func() { _, err := s.ClaimHeartbeat(ctx, now); results <- err })
	}
	wg.Wait()
	close(results)
	claimed := 0
	for err := range results {
		if err == nil {
			claimed++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if claimed != 1 {
		t.Fatal(claimed)
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func queueRelease(t *testing.T, s *Store, key string, now time.Time) model.Job {
	t.Helper()
	p := readReleasePreview(t, s, now)
	if p.Token == nil || p.Impact == nil {
		t.Fatal(p)
	}
	j, err := s.EnqueueReleaseJob(context.Background(), "ecs", p.Impact.Name, *p.Token, key, now)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func releaseResult(t *testing.T, j model.Job) model.ReleaseJobResult {
	t.Helper()
	raw, _ := json.Marshal(j.Result)
	var result model.ReleaseJobResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReleaseAdmissionConfirmsNameDeduplicatesAndExcludesPower(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	if _, err := s.DB.Exec("UPDATE instance SET desired_state='running' WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	preview := readReleasePreview(t, s, now)
	if _, err := s.EnqueueReleaseJob(model.WithAPIKeyPrincipal(ctx, "any-key"), "ecs", preview.Impact.Name, *preview.Token, model.NewID("event_"), now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("API key granted release scope", err)
	}
	if _, err := s.EnqueueReleaseJob(ctx, "ecs", "EXACT PROVIDER NAME", *preview.Token, model.NewID("event_"), now); !errors.Is(err, ErrReleaseConfirmation) {
		t.Fatal("name confirmation was normalized or skipped", err)
	}
	key := model.NewID("event_")
	j := queueRelease(t, s, key, now)
	if j.Type != "instance_release" || j.Status != "queued" || desired(t, s, "ecs") != "running" || releaseResult(t, j).DispatchOutcome != "not_sent" {
		t.Fatal(j)
	}
	for range 2 {
		got, err := s.EnqueueReleaseJob(ctx, "ecs", preview.Impact.Name, *preview.Token, key, now.Add(time.Hour))
		if err != nil || got.ID != j.ID {
			t.Fatal("same event lost its durable identity after preview expiry", got, err)
		}
	}
	if _, err := s.EnqueueReleaseJob(ctx, "ecs", "different", *preview.Token, key, now); !errors.Is(err, ErrPowerEventConflict) {
		t.Fatal(err)
	}
	if got := queueRelease(t, s, model.NewID("event_"), now); got.ID != j.ID {
		t.Fatal("identical active release was duplicated", got.ID, j.ID)
	}
	if _, err := s.EnqueuePowerJob(ctx, "ecs", "start", "", model.NewID("event_"), now); !errors.Is(err, ErrPowerBusy) {
		t.Fatal("power was admitted during release", err)
	}
	if _, err := s.ClaimCollectionJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("collection worker consumed a release", err)
	}
	if _, err := s.ClaimPowerJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("power worker consumed a release", err)
	}
}

func TestReleaseAdmissionRejectsPowerAndConcurrentDuplicates(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	p := readReleasePreview(t, s, now)
	power := queuePower(t, s, "start", now)
	if _, err := s.EnqueueReleaseJob(ctx, "ecs", p.Impact.Name, *p.Token, model.NewID("event_"), now); !errors.Is(err, ErrReleaseBusy) {
		t.Fatal("release was admitted during power", err)
	}
	if _, err := s.DB.Exec("UPDATE job SET status='failed' WHERE id=?", power.ID); err != nil {
		t.Fatal(err)
	}
	key := model.NewID("event_")
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			j, err := s.EnqueueReleaseJob(ctx, "ecs", p.Impact.Name, *p.Token, key, now)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- j.ID
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent duplicate release", id, first)
		}
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM job WHERE type='instance_release'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestReleaseDispatchRequiresUnchangedFreshImpact(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		edit       func(*model.ReleaseInventoryObservation)
	}{
		{"name", "release_preview_changed", func(o *model.ReleaseInventoryObservation) { o.Data.Instances[0].Name = "renamed" }},
		{"state", "release_preview_changed", func(o *model.ReleaseInventoryObservation) { o.Data.Instances[0].Status = "Running" }},
		{"charge", "release_preview_changed", func(o *model.ReleaseInventoryObservation) { o.Data.Instances[0].ChargeType = "PrePaid" }},
		{"protection", "release_preview_changed", func(o *model.ReleaseInventoryObservation) {
			o.Data.Instances[0].DeletionProtection = releaseTestBool(true)
		}},
		{"disk attribute", "release_preview_changed", func(o *model.ReleaseInventoryObservation) {
			o.Data.Disks[0].DeleteWithInstance = releaseTestBool(false)
		}},
		{"missing disk", "release_preview_changed", func(o *model.ReleaseInventoryObservation) { o.Data.Disks = []model.ReleaseDiskFacts{} }},
		{"stale", "release_preflight_unavailable", func(o *model.ReleaseInventoryObservation) { o.QueriedAt = o.QueriedAt.Add(-31 * time.Second) }},
		{"future", "release_preflight_unavailable", func(o *model.ReleaseInventoryObservation) { o.QueriedAt = o.QueriedAt.Add(time.Second) }},
		{"read failure", "release_preflight_unavailable", func(o *model.ReleaseInventoryObservation) { o.Data = nil; o.Error = model.Error("permission_denied") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := powerStore(t)
			now := time.Now().UTC()
			saveEventScan(t, s, releaseTestScan(now), now)
			j := queueRelease(t, s, model.NewID("event_"), now)
			p, err := s.ClaimReleaseJob(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			fresh := releaseTestScan(now).ReleaseInventory[0]
			tc.edit(&fresh)
			if _, err := s.BeginReleaseDispatch(context.Background(), p.Job, fresh, now); err == nil {
				t.Fatal("changed impact was dispatched")
			}
			p, err = s.ReleaseOperation(context.Background(), j.ID)
			if err != nil || p.DispatchStartedAt != nil || p.DispatchOutcome != "not_sent" || p.Job.Status != "failed" || p.Job.Error.Code != tc.code {
				t.Fatal(p, err)
			}
		})
	}
}

func TestReleaseLeaseRecoveryStopsAtDispatchBoundary(t *testing.T) {
	for _, outcome := range []string{"dispatching", "accepted", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s := powerStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			saveEventScan(t, s, releaseTestScan(now), now)
			j := queueRelease(t, s, model.NewID("event_"), now)
			old, err := s.ClaimReleaseJob(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			p, err := s.ClaimReleaseJob(ctx, now.Add(61*time.Second))
			if err != nil || p.Job.Owner == old.Job.Owner {
				t.Fatal(p, err)
			}
			fresh := releaseTestScan(now.Add(61 * time.Second)).ReleaseInventory[0]
			if _, err := s.BeginReleaseDispatch(ctx, old.Job, fresh, now.Add(61*time.Second)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal(err)
			}
			if _, err := s.BeginReleaseDispatch(ctx, p.Job, fresh, now.Add(61*time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BeginReleaseDispatch(ctx, p.Job, fresh, now.Add(61*time.Second)); !errors.Is(err, ErrReleaseDispatched) {
				t.Fatal(err)
			}
			if err := s.RetryJob(ctx, p.Job, 0, false, now.Add(61*time.Second)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("generic retry accepted release", err)
			}
			if err := s.FinishJob(ctx, p.Job, nil, "", now.Add(61*time.Second)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("generic finish accepted release", err)
			}
			if outcome != "dispatching" {
				code := ""
				if outcome == "unknown" {
					code = "release_outcome_unknown"
				}
				if err := s.RecordReleaseDispatch(ctx, p.Job, outcome, code, now.Add(62*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ClaimReleaseJob(ctx, now.Add(122*time.Second)); !errors.Is(err, ErrNotFound) {
				t.Fatal("sent release was reclaimed", err)
			}
			got, err := s.ReleaseOperation(ctx, j.ID)
			wantOutcome, wantCode := "unknown", "release_outcome_unknown"
			if outcome == "accepted" {
				wantOutcome, wantCode = "accepted", "release_confirmation_interrupted"
			}
			if err != nil || got.DispatchOutcome != wantOutcome || got.Job.Status != "failed" || got.Job.Error.Code != wantCode {
				t.Fatal(got, err)
			}
		})
	}
}

func TestRestoredReleaseJobsStayCancelledAndDeduplicated(t *testing.T) {
	for _, state := range []string{"queued", "dispatching", "accepted"} {
		t.Run(state, func(t *testing.T) {
			s := powerStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			saveEventScan(t, s, releaseTestScan(now), now)
			key := model.NewID("event_")
			j := queueRelease(t, s, key, now)
			original, err := s.ReleaseOperation(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state != "queued" {
				p, err := s.ClaimReleaseJob(ctx, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.BeginReleaseDispatch(ctx, p.Job, releaseTestScan(now).ReleaseInventory[0], now); err != nil {
					t.Fatal(err)
				}
				if state == "accepted" {
					if err := s.RecordReleaseDispatch(ctx, p.Job, "accepted", "", now); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.PrepareRestoredCopy(ctx, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			got, err := s.EnqueueReleaseJob(ctx, "ecs", original.ConfirmedName, original.PreviewToken, key, now.Add(time.Hour))
			if err != nil || got.ID != j.ID || got.Status != "failed" || got.Error.Code != "backup_restored" {
				t.Fatal(got, err)
			}
			want := map[string]string{"queued": "not_sent", "dispatching": "unknown", "accepted": "accepted"}[state]
			if releaseResult(t, got).DispatchOutcome != want {
				t.Fatal(releaseResult(t, got))
			}
			if _, err := s.ClaimReleaseJob(ctx, now.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseDispatchFencesAccountChangesAndRawErrors(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "credential changed", true: "disabled"}[disabled], func(t *testing.T) {
			s := powerStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			saveEventScan(t, s, releaseTestScan(now), now)
			j := queueRelease(t, s, model.NewID("event_"), now)
			p, err := s.ClaimReleaseJob(ctx, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) {
				if disabled {
					a.Enabled = false
					return nil, nil
				}
				return make([]byte, 32), nil
			}, now, time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BeginReleaseDispatch(ctx, p.Job, releaseTestScan(now).ReleaseInventory[0], now); err == nil {
				t.Fatal("changed account reached dispatch")
			}
			got, err := s.ReleaseOperation(ctx, j.ID)
			if err != nil || got.DispatchStartedAt != nil || got.DispatchOutcome != "not_sent" || got.Job.Status != "failed" {
				t.Fatal(got, err)
			}
		})
	}
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	j := queueRelease(t, s, model.NewID("event_"), now)
	p, err := s.ClaimReleaseJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginReleaseDispatch(ctx, p.Job, releaseTestScan(now).ReleaseInventory[0], now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReleaseDispatch(ctx, p.Job, "rejected", "raw-provider-secret-not-for-history", now); !errors.Is(err, ErrReleaseParameters) {
		t.Fatal(err)
	}
	if err := s.RecordReleaseDispatch(ctx, p.Job, "rejected", "instance_deletion_protected", now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "failed" || got.Error.Code != "instance_deletion_protected" || releaseResult(t, got).DispatchOutcome != "rejected" {
		t.Fatal(got, err)
	}
}

func TestReleaseBeforeSendLeaseAttemptsAreBounded(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	j := queueRelease(t, s, model.NewID("event_"), now)
	for i := range 3 {
		p, err := s.ClaimReleaseJob(ctx, now.Add(time.Duration(i)*61*time.Second))
		if err != nil || p.Job.Attempts != i+1 || p.DispatchStartedAt != nil {
			t.Fatal(p, err)
		}
	}
	if _, err := s.ClaimReleaseJob(ctx, now.Add(183*time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err := s.ReleaseOperation(ctx, j.ID)
	if err != nil || got.DispatchOutcome != "not_sent" || got.DispatchStartedAt != nil || got.Job.Status != "failed" || got.Job.Error.Code != "retry_exhausted" {
		t.Fatal(got, err)
	}
}

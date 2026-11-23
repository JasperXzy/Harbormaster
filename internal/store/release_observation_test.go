package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestReleaseObservationRequiresAcceptedFreshEvidence(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	j := queueRelease(t, s, model.NewID("event_"), now)
	p, err := s.ClaimReleaseJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	o := model.ReleaseObservation{ObservedAt: now, InstancePresent: false}
	if _, err := s.ObserveRelease(ctx, p.Job, o, now); !errors.Is(err, ErrReleaseDispatched) {
		t.Fatal(err)
	}
	if _, err := s.BeginReleaseDispatch(ctx, p.Job, releaseTestScan(now).ReleaseInventory[0], now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveRelease(ctx, p.Job, o, now); !errors.Is(err, ErrReleaseDispatched) {
		t.Fatal("dispatching was treated as accepted", err)
	}
	if err := s.RecordReleaseDispatch(ctx, p.Job, "accepted", "", now); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(-time.Second), now.Add(2 * time.Second)} {
		if _, err := s.ObserveRelease(ctx, p.Job, model.ReleaseObservation{ObservedAt: at}, now.Add(time.Second)); !errors.Is(err, ErrReleaseParameters) {
			t.Fatal(err)
		}
	}
	if done, err := s.ObserveRelease(ctx, p.Job, model.ReleaseObservation{InstancePresent: true, ObservedAt: now.Add(time.Second)}, now.Add(time.Second)); err != nil || done {
		t.Fatal(done, err)
	}
	if _, err := s.ObserveRelease(ctx, p.Job, model.ReleaseObservation{InstancePresent: false, ObservedAt: now}, now.Add(time.Second)); !errors.Is(err, ErrReleaseParameters) {
		t.Fatal("out of order observation accepted", err)
	}
	if done, err := s.ObserveRelease(ctx, p.Job, model.ReleaseObservation{InstancePresent: false, ObservedAt: now.Add(2 * time.Second)}, now.Add(2*time.Second)); err != nil || !done {
		t.Fatal(done, err)
	}
	got, err := s.Job(ctx, j.ID)
	result := releaseResult(t, got)
	if err != nil || got.Status != "succeeded" || result.DispatchOutcome != "accepted" || result.Observation == nil || result.Observation.InstancePresent || result.ConfirmationBasis == nil || *result.ConfirmationBasis != "not_found_after_accept" {
		t.Fatal(got, err)
	}
	if v := readReleasePreview(t, s, now.Add(2*time.Second)); v.Status != "blocked" || v.Token != nil {
		t.Fatal("confirmed absent target remained eligible", v)
	}
}

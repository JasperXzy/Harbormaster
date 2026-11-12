package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestPowerObservationRequiresFreshEvidenceAndCurrentOwner(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	j := queuePower(t, s, "reboot", now)
	p := claimPower(t, s, now)
	oldStart := now.Add(-time.Hour)
	baseline := model.PowerObservation{Status: "running", StartedAt: &oldStart, ObservedAt: now}
	if err := s.SavePowerBaseline(ctx, p.Job, baseline, now); err != nil {
		t.Fatal(err)
	}
	if done, err := s.ObservePower(ctx, p.Job, baseline, now); done || !errors.Is(err, ErrPowerDispatched) {
		t.Fatal("pre-dispatch observation completed job", done, err)
	}
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePowerBaseline(ctx, p.Job, baseline, now.Add(time.Second)); !errors.Is(err, ErrPowerDispatched) {
		t.Fatal("baseline changed after dispatch", err)
	}
	if err := s.RecordPowerDispatch(ctx, p.Job, "accepted", "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if done, err := s.ObservePower(ctx, p.Job, baseline, now.Add(time.Second)); done || !errors.Is(err, ErrPowerParameters) {
		t.Fatal("stale baseline used as completion", done, err)
	}
	stillRunning := baseline
	stillRunning.ObservedAt = now.Add(2 * time.Second)
	if done, err := s.ObservePower(ctx, p.Job, stillRunning, stillRunning.ObservedAt); done || err != nil {
		t.Fatal("same Running falsely confirms reboot", done, err)
	}
	transition := model.PowerObservation{Status: "starting", ObservedAt: now.Add(3 * time.Second)}
	if done, err := s.ObservePower(ctx, p.Job, transition, transition.ObservedAt); done || err != nil {
		t.Fatal(done, err)
	}
	if done, err := s.ObservePower(ctx, p.Job, stillRunning, transition.ObservedAt); done || !errors.Is(err, ErrPowerParameters) {
		t.Fatal("out-of-order running accepted", done, err)
	}
	forgedOwner := p.Job
	forgedOwner.Owner = "other"
	final := model.PowerObservation{Status: "running", ObservedAt: now.Add(4 * time.Second)}
	if _, err := s.ObservePower(ctx, forgedOwner, final, final.ObservedAt); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("wrong owner published", err)
	}
	if done, err := s.ObservePower(ctx, p.Job, final, final.ObservedAt); !done || err != nil {
		t.Fatal(done, err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "succeeded" {
		t.Fatal(got, err)
	}
	raw, _ := json.Marshal(got.Result)
	var result model.PowerJobResult
	if err = json.Unmarshal(raw, &result); err != nil || result.ConfirmationBasis == nil || *result.ConfirmationBasis != "state_transition" || result.Observation == nil || result.Baseline == nil {
		t.Fatal(result, err)
	}
	if _, err := s.ObservePower(ctx, p.Job, final, final.ObservedAt); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("terminal result overwritten", err)
	}
}

func TestPowerObservationDoesNotUseStaleChangedStartTime(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	queuePower(t, s, "reboot", now)
	p := claimPower(t, s, now)
	old := now.Add(-time.Hour)
	baseline := model.PowerObservation{Status: "running", StartedAt: &old, ObservedAt: now}
	if err := s.SavePowerBaseline(ctx, p.Job, baseline, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPowerDispatch(ctx, p.Job, "accepted", "", now); err != nil {
		t.Fatal(err)
	}
	newerButStale := now.Add(-10 * time.Minute)
	stale := model.PowerObservation{Status: "running", StartedAt: &newerButStale, ObservedAt: now.Add(time.Second)}
	if done, err := s.ObservePower(ctx, p.Job, stale, stale.ObservedAt); done || err != nil {
		t.Fatal("old provider start time proved this reboot", done, err)
	}
	future := stale
	future.ObservedAt = now.Add(time.Hour)
	if _, err := s.ObservePower(ctx, p.Job, future, now); !errors.Is(err, ErrPowerParameters) {
		t.Fatal("future observation accepted", err)
	}
}

func TestPowerConfirmationCannotInventUnknownMode(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	queuePower(t, s, "stop", now)
	p := claimPower(t, s, now)
	baseline := model.PowerObservation{Status: "running", ObservedAt: now}
	if err := s.SavePowerBaseline(ctx, p.Job, baseline, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPowerDispatch(ctx, p.Job, "accepted", "", now); err != nil {
		t.Fatal(err)
	}
	o := model.PowerObservation{Status: "stopped", ObservedAt: now.Add(time.Second)}
	if done, err := s.ObservePower(ctx, p.Job, o, o.ObservedAt); done || err != nil {
		t.Fatal("unknown mode inferred from request", done, err)
	}
	if err := s.FailPowerJob(ctx, p.Job, "arbitrary-provider-message", now); !errors.Is(err, ErrPowerParameters) {
		t.Fatal(err)
	}
	if err := s.FailPowerJob(ctx, p.Job, "power_confirmation_timeout", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, p.Job.ID)
	if err != nil || got.Status != "failed" || got.Error.Code != "power_confirmation_timeout" {
		t.Fatal(got, err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestM2AutomationScheduleBoundaryPriorityExternalStopAndMonthlyReset(t *testing.T) {
	s := powerStore(t)
	ctx := model.WithJobTrigger(context.Background(), "panel")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	policy := model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, Timezone: "Asia/Shanghai"}

	// Start at 2026-11-15 10:05 Asia/Shanghai
	t0 := time.Date(2026, 11, 15, 10, 5, 0, 0, loc).UTC()

	// Create schedule for 10:00 (5 mins ago): must NOT fire today because activated_at = 10:05!
	sched, err := s.SavePowerSchedule(ctx, "", model.PowerScheduleInput{
		Name: "daily-stop", Enabled: true, AccountID: "a", Action: "stop", StopMode: "keep_charging",
		TimeOfDay: "10:00", DaysOfWeek: []int{0, 1, 2, 3, 4, 5, 6}, CatchUpMinutes: 15, Targets: []string{"ecs"},
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateAutomation(ctx, policy, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPowerJob(ctx, t0.Add(time.Minute)); err == nil {
		t.Fatal("schedule fired retroactively before activated_at")
	}

	// Next day at 10:10 (within 15m catch-up window of 10:00) -> MUST fire once!
	tNext := time.Date(2026, 11, 16, 10, 10, 0, 0, loc).UTC()
	if err := s.EvaluateAutomation(ctx, policy, tNext); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimPowerJob(ctx, tNext)
	if err != nil || op.Action != "stop" {
		t.Fatal("expected catch-up scheduled stop", err, op)
	}
	if err := s.SavePowerBaseline(ctx, op.Job, model.PowerObservation{Status: "running", ObservedAt: tNext}, tNext); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPowerDispatchWithPolicy(ctx, op.Job, policy, tNext); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPowerDispatch(ctx, op.Job, "accepted", "", tNext); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObservePower(ctx, op.Job, model.PowerObservation{Status: "stopped", StoppedMode: new("keep_charging"), ObservedAt: tNext.Add(time.Second)}, tNext.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = s.DB.Exec("UPDATE instance SET status='stopped' WHERE id='ecs'")

	// Enable keepalive on 'ecs': while in scheduled stop window (desired_state='stopped'), keepalive MUST NOT start it!
	_, err = s.SaveKeepaliveSettings(ctx, model.KeepaliveSettings{
		Revision: 0, Enabled: true, SpotAutoRecover: true, Targets: []string{"ecs"},
	}, tNext.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateAutomation(ctx, policy, tNext.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPowerJob(ctx, tNext.Add(5*time.Second)); err == nil {
		t.Fatal("keepalive violated scheduled stop / stopped intent")
	}

	// Disable stop schedule, manually start 'ecs' so desired_state='running'
	sched.Enabled = false
	if _, err := s.SavePowerSchedule(ctx, sched.ID, sched.PowerScheduleInput, tNext.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	jStart, err := s.EnqueuePowerJob(ctx, "ecs", "start", "", model.NewID("ev_"), tNext.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.DB.Exec("UPDATE job SET status='succeeded',finished_at=? WHERE id=?", timestamp(tNext.Add(12*time.Second)), jStart.ID)
	_, _ = s.DB.Exec("UPDATE instance SET status='running' WHERE id='ecs'")

	// Simulate external console shutdown 20 minutes later (running -> stopped without HM stop job)
	tExt := tNext.Add(20 * time.Minute)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkExternalShutdownTx(ctx, tx, "a", "ecs", "ecs-name", map[string]any{"charge_type": "pay_as_you_go"}, tExt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, _ = s.DB.Exec("UPDATE instance SET status='stopped' WHERE id='ecs'")

	// Pending confirmation MUST block keepalive!
	if err := s.EvaluateAutomation(ctx, policy, tExt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPowerJob(ctx, tExt.Add(time.Second)); err == nil {
		t.Fatal("keepalive started instance while external shutdown confirmation was pending")
	}

	// Confirm "restore_running" -> enqueues start job!
	desired, restoreJobID, err := s.ConfirmInstanceIntent(ctx, "ecs", "restore_running", true, tExt.Add(2*time.Second))
	if err != nil || desired != "running" || restoreJobID == nil {
		t.Fatal("expected restore_running to queue start", err, desired, restoreJobID)
	}
	_, _ = s.DB.Exec("UPDATE job SET status='succeeded',finished_at=? WHERE id=?", timestamp(tExt.Add(3*time.Second)), *restoreJobID)

	// Test Monthly 1st Reset: mark instance quota_stopped + stopped, advance to Dec 1st 00:05 Asia/Shanghai
	_, _ = s.DB.Exec("UPDATE instance SET status='stopped',desired_state='quota_stopped' WHERE id='ecs'")
	if _, err := s.SaveMonthlyResetSettings(ctx, model.MonthlyResetSettings{
		Revision: 0, Enabled: true, Targets: []string{"ecs"},
	}, tExt.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	tDec1 := time.Date(2026, 12, 1, 0, 5, 0, 0, loc).UTC()
	if err := s.EvaluateAutomation(ctx, policy, tDec1); err != nil {
		t.Fatal(err)
	}
	resetOp, err := s.ClaimPowerJob(ctx, tDec1)
	if err != nil || resetOp.Action != "start" {
		t.Fatal("expected monthly reset auto-start on 1st of month", err, resetOp)
	}
	if _, err := s.BeginPowerDispatchWithPolicy(ctx, resetOp.Job, policy, tDec1); err != nil {
		t.Fatal("expected monthly reset dispatch authorization to pass", err)
	}
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func powerStore(t *testing.T) *Store {
	t.Helper()
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	return s
}
func desired(t *testing.T, s *Store, id string) string {
	t.Helper()
	var v string
	if err := s.DB.QueryRow("SELECT desired_state FROM instance WHERE id=?", id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
func queuePower(t *testing.T, s *Store, action string, now time.Time) model.Job {
	t.Helper()
	mode := ""
	if action == "stop" {
		mode = "keep_charging"
	}
	j, err := s.EnqueuePowerJob(model.WithJobTrigger(context.Background(), "panel"), "ecs", action, mode, model.NewID("event_"), now)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func claimPower(t *testing.T, s *Store, now time.Time) PowerOperation {
	t.Helper()
	p, err := s.ClaimPowerJob(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPowerEnqueueAtomicallyKeepsIntentAndDeduplicates(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	j := queuePower(t, s, "start", now)
	if desired(t, s, "ecs") != "running" || j.Trigger != "panel" || j.InstanceID == nil || *j.InstanceID != "ecs" || len(j.AccountIDs) != 1 || j.AccountIDs[0] != "a" {
		t.Fatal(j)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dup, err := s.EnqueuePowerJob(model.WithJobTrigger(ctx, "cli"), "ecs", "start", "", model.NewID("event_"), now)
			if err != nil || dup.ID != j.ID || dup.Trigger != "panel" {
				t.Error("duplicate request", dup, err)
			}
		}()
	}
	wg.Wait()
	if _, err := s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", model.NewID("event_"), now); !errors.Is(err, ErrPowerBusy) {
		t.Fatal(err)
	}
	if desired(t, s, "ecs") != "running" {
		t.Fatal("rejected conflict changed intent")
	}
	if _, err := s.ClaimCollectionJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("read worker consumed power job", err)
	}
	p := claimPower(t, s, now)
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPowerDispatch(ctx, p.Job, "rejected", "permission_denied", now); err != nil {
		t.Fatal(err)
	}
	failed, err := s.Job(ctx, j.ID)
	if err != nil || failed.Status != "failed" || failed.Error.Code != "permission_denied" || desired(t, s, "ecs") != "running" {
		t.Fatal(failed, err)
	}
	stop := queuePower(t, s, "stop", now)
	if stop.ID == j.ID || desired(t, s, "ecs") != "stopped" {
		t.Fatal("new stop intent not saved")
	}
	items, err := s.Jobs(ctx, model.JobFilter{Type: "instance_power", InstanceID: "ecs"}, 0, 20)
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
}

func TestPowerEnqueueRollbackAndValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, tc := range []struct{ id, action, mode string }{{"missing", "start", ""}, {"ecs", "stop", ""}, {"ecs", "RunCommand", ""}, {"swas", "stop", "stop_charging"}, {"swas", "start", "keep_charging"}} {
		s := powerStore(t)
		if _, err := s.EnqueuePowerJob(ctx, tc.id, tc.action, tc.mode, model.NewID("event_"), now); err == nil {
			t.Fatal("invalid power input accepted", tc)
		}
		var count int
		s.DB.QueryRow("SELECT count(*) FROM job").Scan(&count)
		if count != 0 || desired(t, s, "ecs") != "stopped" {
			t.Fatal("invalid request mutated storage")
		}
	}
	s := powerStore(t)
	if _, err := s.DB.Exec("CREATE TRIGGER fail_power_insert BEFORE INSERT ON instance_power_job BEGIN SELECT RAISE(ABORT,'test'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueuePowerJob(ctx, "ecs", "start", "", model.NewID("event_"), now); err == nil {
		t.Fatal("forced failure did not occur")
	}
	var count int
	for _, table := range []string{"job", "job_order", "job_change", "instance_power_job"} {
		if err := s.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	if desired(t, s, "ecs") != "stopped" {
		t.Fatal("failed enqueue changed desired state")
	}
}

func TestPowerLeaseRecoveryNeverReplaysDispatch(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	for _, outcome := range []string{"dispatching", "accepted"} {
		t.Run(outcome, func(t *testing.T) {
			s := powerStore(t)
			j := queuePower(t, s, "reboot", now)
			first := claimPower(t, s, now)
			second := claimPower(t, s, now.Add(time.Minute))
			if first.Job.Owner == second.Job.Owner || second.Job.Attempts != 2 {
				t.Fatal("pre-dispatch lease was not safely recovered")
			}
			if _, err := s.BeginPowerDispatch(ctx, first.Job, now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("old owner dispatched", err)
			}
			dispatched, err := s.BeginPowerDispatch(ctx, second.Job, now.Add(time.Minute))
			if err != nil || dispatched.DispatchStartedAt == nil || dispatched.DispatchOutcome != "dispatching" {
				t.Fatal(dispatched, err)
			}
			if _, err := s.BeginPowerDispatch(ctx, second.Job, now.Add(time.Minute)); !errors.Is(err, ErrPowerDispatched) {
				t.Fatal("second dispatch allowed", err)
			}
			if err := s.RetryJob(ctx, second.Job, time.Second, false, now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("generic retry accepted power job", err)
			}
			if err := s.FinishJob(ctx, second.Job, nil, "", now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("generic finish fabricated power success", err)
			}
			if outcome == "accepted" {
				if err := s.RecordPowerDispatch(ctx, second.Job, "accepted", "", now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				accepted, _ := s.Job(ctx, j.ID)
				if accepted.Status != "running" {
					t.Fatal("API acceptance marked observed success")
				}
			}
			if _, err := s.ClaimPowerJob(ctx, now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
				t.Fatal("dispatched request replayed", err)
			}
			final, err := s.PowerOperation(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantCode, wantOutcome := "power_outcome_unknown", "unknown"
			if outcome == "accepted" {
				wantCode, wantOutcome = "power_confirmation_interrupted", "accepted"
			}
			if final.Job.Status != "failed" || final.Job.Error.Code != wantCode || final.DispatchOutcome != wantOutcome || desired(t, s, "ecs") != "running" {
				t.Fatal(final)
			}
			if err := s.RecordPowerDispatch(ctx, second.Job, "accepted", "", now.Add(2*time.Minute)); !errors.Is(err, ErrLeaseLost) {
				t.Fatal("late worker overwrote terminal uncertainty", err)
			}
		})
	}
}

func TestPowerDispatchRechecksAccountAndInventory(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, tc := range []struct{ name, query string }{{"disabled", `UPDATE account_settings SET enabled=0,config_json=json_set(config_json,'$.enabled',json('false'))`}, {"changed", `UPDATE account_settings SET revision=revision+1`}, {"disappeared", `UPDATE resource_snapshot SET present=0 WHERE id='ecs'`}} {
		t.Run(tc.name, func(t *testing.T) {
			s := powerStore(t)
			j := queuePower(t, s, "start", now)
			p := claimPower(t, s, now)
			if _, err := s.DB.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err == nil {
				t.Fatal("invalidated operation dispatched")
			}
			got, err := s.PowerOperation(ctx, j.ID)
			if err != nil || got.Job.Status != "failed" || got.DispatchStartedAt != nil || got.DispatchOutcome != "not_sent" || desired(t, s, "ecs") != "running" {
				t.Fatal(got, err)
			}
		})
	}
}

func TestPowerRecordFailureAndCodeBoundary(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now()
	queuePower(t, s, "stop", now)
	p := claimPower(t, s, now)
	if err := s.RecordPowerDispatch(ctx, p.Job, "accepted", "", now); !errors.Is(err, ErrPowerDispatched) {
		t.Fatal("response before dispatch", err)
	}
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ outcome, code string }{{"accepted", "provider-secret"}, {"unknown", "raw-sdk-message-with-secret"}, {"fake", "power_outcome_unknown"}} {
		if err := s.RecordPowerDispatch(ctx, p.Job, tc.outcome, tc.code, now); !errors.Is(err, ErrPowerParameters) {
			t.Fatal(err)
		}
	}
	if err := s.RecordPowerDispatch(ctx, p.Job, "unknown", "power_outcome_unknown", now); err != nil {
		t.Fatal(err)
	}
	if desired(t, s, "ecs") != "stopped" {
		t.Fatal("unknown call reverted stop intent")
	}
	if _, err := s.ClaimPowerJob(ctx, now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown outcome replayed", err)
	}
}

func TestPowerMigrationPreservesAllJobRelationsAndSequences(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "harbormaster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA foreign_keys=ON; CREATE TABLE schema_migration(name TEXT PRIMARY KEY,applied_at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() >= "017_instance_power.sql" {
			break
		}
		raw, e := migrations.ReadFile("migrations/" + file.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(file.Name(), e)
		}
		if _, e = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", file.Name()); e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{
		`INSERT INTO app_meta VALUES('mode','production')`,
		`INSERT INTO account VALUES('a','account','china','now')`,
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,trigger_source,lease_owner,lease_until,attempts) VALUES('legacy','refresh','all','["a"]','running','2026-11-12T00:00:00Z','cli','owner',9999999999,2)`,
		`UPDATE job SET result_json='{"preserved":true}' WHERE id='legacy'`,
		`INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,details_json) VALUES('a',1,'legacy',1,'now','{}')`,
		`INSERT INTO notification(id,event_key,job_id,created_at,kind,severity,title,body,delivery_status) VALUES('notice','event','legacy','now','test','info','title','body','pending')`,
		`UPDATE sqlite_sequence SET seq=100 WHERE name='job_order'`,
		`UPDATE sqlite_sequence SET seq=200 WHERE name='job_change'`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	legacy, err := s.Job(ctx, "legacy")
	if err != nil || legacy.Status != "running" || legacy.Owner != "owner" || legacy.Attempts != 2 || legacy.Trigger != "cli" || legacy.Sequence != 1 {
		t.Fatal(legacy, err)
	}
	for _, tc := range []struct {
		query string
		want  int
	}{
		{`SELECT count(*) FROM cdt_observation WHERE job_id='legacy' AND attempt=1`, 1},
		{`SELECT count(*) FROM notification WHERE job_id='legacy' AND delivery_status='pending'`, 1},
		{`SELECT count(*) FROM notification_change`, 1},
		{`SELECT count(*) FROM job_change WHERE job_id='legacy'`, 2},
		{`SELECT count(*) FROM job_order WHERE job_id='legacy' AND seq=1`, 1},
		{`SELECT count(*) FROM pragma_foreign_key_check`, 0},
		{`SELECT foreign_keys FROM pragma_foreign_keys`, 1},
	} {
		var got int
		if err := s.DB.QueryRow(tc.query).Scan(&got); err != nil || got != tc.want {
			t.Fatal(tc.query, got, err)
		}
	}
	fresh, err := s.EnqueueJob(ctx, "refresh", "new", nil, time.Now())
	if err != nil || fresh.Sequence != 101 {
		t.Fatal("cursor high water lost", fresh, err)
	}
	head, err := s.JobChangeHead(ctx)
	if err != nil || head != 201 {
		t.Fatal("SSE high water lost", head, err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal("migration not repeatable", err)
	}
}

func TestPowerRestoreCancelsWorkAndRetainsDispatchEvidence(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for _, phase := range []string{"queued", "claimed", "dispatching", "accepted", "rejected"} {
		t.Run(phase, func(t *testing.T) {
			s := powerStore(t)
			j := queuePower(t, s, "start", now)
			if phase != "queued" {
				p := claimPower(t, s, now)
				if phase != "claimed" {
					if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
						t.Fatal(err)
					}
					if phase == "accepted" {
						if err := s.RecordPowerDispatch(ctx, p.Job, "accepted", "", now); err != nil {
							t.Fatal(err)
						}
					}
					if phase == "rejected" {
						if err := s.RecordPowerDispatch(ctx, p.Job, "rejected", "permission_denied", now); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			restored, err := s.PrepareRestoredCopy(ctx, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			wantCount := int64(1)
			if phase == "rejected" {
				wantCount = 0
			}
			if restored.CancelledJobs != wantCount {
				t.Fatal(restored)
			}
			got, err := s.PowerOperation(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantOutcome := "not_sent"
			switch phase {
			case "dispatching":
				wantOutcome = "unknown"
			case "accepted", "rejected":
				wantOutcome = phase
			}
			wantCode := "backup_restored"
			if phase == "rejected" {
				wantCode = "permission_denied"
			}
			if got.Job.Status != "failed" || got.Job.Error.Code != wantCode || got.DispatchOutcome != wantOutcome || got.Job.Owner != "" || desired(t, s, "ecs") != "running" {
				t.Fatal(got)
			}
			if _, err := s.ClaimPowerJob(ctx, now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
				t.Fatal("restored operation replayed", err)
			}
		})
	}
}

func TestPowerClaimAttemptsBoundedBeforeDispatch(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now()
	j := queuePower(t, s, "start", now)
	for attempt := 1; attempt <= 3; attempt++ {
		p := claimPower(t, s, now.Add(time.Duration(attempt-1)*time.Minute))
		if p.Job.Attempts != attempt {
			t.Fatal(p)
		}
	}
	if _, err := s.ClaimPowerJob(ctx, now.Add(3*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "failed" || got.Error.Code != "retry_exhausted" {
		t.Fatal(got, err)
	}
}

func TestPowerEventKeysDeduplicateAfterCompletionAndRestore(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now()
	firstKey, aliasKey := model.NewID("event_"), model.NewID("event_")
	first, err := s.EnqueuePowerJob(ctx, "ecs", "start", "", firstKey, now)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := s.EnqueuePowerJob(ctx, "ecs", "start", "", aliasKey, now)
	if err != nil || alias.ID != first.ID {
		t.Fatal(alias, err)
	}
	p := claimPower(t, s, now)
	if _, err = s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPowerDispatch(ctx, p.Job, "unknown", "power_outcome_unknown", now); err != nil {
		t.Fatal(err)
	}
	next := queuePower(t, s, "stop", now)
	for _, key := range []string{firstKey, aliasKey} {
		dup, e := s.EnqueuePowerJob(ctx, "ecs", "start", "", key, now)
		if e != nil || dup.ID != first.ID || dup.Status != "failed" || desired(t, s, "ecs") != "stopped" {
			t.Fatal("completed retry changed intent or replayed", dup, e)
		}
	}
	if _, err = s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", firstKey, now); !errors.Is(err, ErrPowerEventConflict) {
		t.Fatal("event key reused for different command", err)
	}
	if _, err = s.EnqueuePowerJob(ctx, "swas", "start", "", firstKey, now); !errors.Is(err, ErrPowerEventConflict) {
		t.Fatal("event key reused for different target", err)
	}
	if _, err = s.EnqueuePowerJob(ctx, "ecs", "start", "", "", now); !errors.Is(err, ErrPowerParameters) {
		t.Fatal("missing key accepted", err)
	}
	var nextKey string
	if err = s.DB.QueryRow("SELECT event_key FROM instance_power_request WHERE job_id=?", next.ID).Scan(&nextKey); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareRestoredCopy(ctx, now); err != nil {
		t.Fatal(err)
	}
	dup, err := s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", nextKey, now)
	if err != nil || dup.ID != next.ID || dup.Error.Code != "backup_restored" {
		t.Fatal("restore lost durable deduplication", dup, err)
	}
}

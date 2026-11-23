package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func powerKey(t *testing.T, s *Store, scope string) (model.APIKey, string) {
	t.Helper()
	k, token, err := s.CreateAPIKey(context.Background(), model.APIKeyCreate{Name: "test", Scopes: []string{scope}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return k, token
}

func TestAPIKeyPowerOwnershipRevocationAndEventNamespaces(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a, _ := powerKey(t, s, "instance:control")
	b, _ := powerKey(t, s, "instance:control")
	limited, _ := powerKey(t, s, "summary:read")
	ca, cb := model.WithAPIKeyPrincipal(ctx, a.ID), model.WithAPIKeyPrincipal(ctx, b.ID)
	event := model.NewID("event_")
	j, err := s.EnqueuePowerJob(ca, "ecs", "stop", "keep_charging", event, now)
	if err != nil || j.Trigger != "api" {
		t.Fatal(j, err)
	}
	if own, err := s.APIKeyPowerJob(ctx, a.ID, j.ID); err != nil || own.ID != j.ID {
		t.Fatal(own, err)
	}
	if _, err := s.APIKeyPowerJob(ctx, b.ID, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("other key can see job", err)
	}
	for _, other := range []context.Context{ctx, cb} {
		if _, err := s.EnqueuePowerJob(other, "ecs", "stop", "keep_charging", event, now); !errors.Is(err, ErrPowerBusy) {
			t.Fatal("active work merged across principals", err)
		}
	}
	if _, err := s.EnqueuePowerJob(model.WithAPIKeyPrincipal(ctx, limited.ID), "swas", "start", "", event, now); !errors.Is(err, ErrAPIKeyScope) {
		t.Fatal("store trusted stale scope", err)
	}
	if err := s.RevokeAPIKey(ctx, a.ID, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "failed" || got.Error.Code != "api_key_revoked" || desired(t, s, "ecs") != "stopped" {
		t.Fatal(got, err)
	}
	if _, err := s.EnqueuePowerJob(ca, "ecs", "stop", "keep_charging", event, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked retry allowed", err)
	}
	if _, err := s.APIKeyPowerJob(ctx, a.ID, j.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked key history access", err)
	}
	second, err := s.EnqueuePowerJob(cb, "ecs", "stop", "keep_charging", event, now)
	if err != nil || second.ID == j.ID {
		t.Fatal("event not namespaced", second, err)
	}
	if err := s.RevokeAPIKey(ctx, a.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	secondAgain, err := s.Job(ctx, second.ID)
	if err != nil || secondAgain.Status != "queued" {
		t.Fatal("revocation cancelled other key", secondAgain, err)
	}
}

func TestAPIKeyRevocationBoundaryPreservesDispatchedEvidence(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		t.Run(map[bool]string{false: "preflight", true: "dispatched"}[dispatched], func(t *testing.T) {
			s := powerStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			key, _ := powerKey(t, s, "instance:control")
			j, err := s.EnqueuePowerJob(model.WithAPIKeyPrincipal(ctx, key.ID), "ecs", "stop", "keep_charging", model.NewID("event_"), now)
			if err != nil {
				t.Fatal(err)
			}
			p := claimPower(t, s, now)
			if err := s.SavePowerBaseline(ctx, p.Job, model.PowerObservation{Status: "running", ObservedAt: now}, now); err != nil {
				t.Fatal(err)
			}
			if dispatched {
				if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RevokeAPIKey(ctx, key.ID, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if !dispatched {
				if _, err := s.BeginPowerDispatch(ctx, p.Job, now.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
					t.Fatal("revoked preflight dispatched", err)
				}
				got, _ := s.PowerOperation(ctx, j.ID)
				if got.Job.Status != "failed" || got.DispatchStartedAt != nil || got.DispatchOutcome != "not_sent" {
					t.Fatal(got)
				}
			} else {
				if err := s.RecordPowerDispatch(ctx, p.Job, "accepted", "", now.Add(time.Second)); err != nil {
					t.Fatal("revocation destroyed response evidence", err)
				}
				mode := "keep_charging"
				if done, err := s.ObservePower(ctx, p.Job, model.PowerObservation{Status: "stopped", StoppedMode: &mode, ObservedAt: now.Add(2 * time.Second)}, now.Add(2*time.Second)); err != nil || !done {
					t.Fatal(done, err)
				}
				got, _ := s.Job(ctx, j.ID)
				if got.Status != "succeeded" {
					t.Fatal(got)
				}
			}
			if desired(t, s, "ecs") != "stopped" {
				t.Fatal("revocation reverted intent")
			}
		})
	}
}

func TestAPIKeyRevocationAndCancellationAreAtomic(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now()
	key, token := powerKey(t, s, "instance:control")
	j, err := s.EnqueuePowerJob(model.WithAPIKeyPrincipal(ctx, key.ID), "ecs", "start", "", model.NewID("event_"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER fail_cancel BEFORE UPDATE OF status ON job WHEN new.error_code='api_key_revoked' BEGIN SELECT RAISE(ABORT,'test'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAPIKey(ctx, key.ID, now); err == nil {
		t.Fatal("forced failure missing")
	}
	if _, err = s.AuthenticateAPIKey(ctx, token, "instance:control", now); err != nil {
		t.Fatal("revocation partially committed", err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "queued" {
		t.Fatal(got, err)
	}
}

func TestAPIKeyPowerMigrationPreservesPanelEventKeys(t *testing.T) {
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
	for _, f := range files {
		if f.Name() >= "020_api_key_power.sql" {
			break
		}
		raw, e := migrations.ReadFile("migrations/" + f.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(f.Name(), e)
		}
		if _, e = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", f.Name()); e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{
		`INSERT INTO app_meta VALUES('mode','production')`,
		`INSERT INTO account VALUES('a','account','china','now')`,
		`INSERT INTO instance VALUES('ecs','a','i-test','ecs','ecs','cn-shanghai','running','stopped','now')`,
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,instance_id,trigger_source) VALUES('legacy','instance_power','power:ecs','["a"]','failed','2026-11-12T00:00:00Z','ecs','panel')`,
		`INSERT INTO instance_power_job(job_id,account_revision,action,stop_mode) VALUES('legacy',1,'stop','keep_charging')`,
		`INSERT INTO instance_power_request(event_key,job_id) VALUES('event_1234567890123456','legacy')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
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
	j, err := s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", "event_1234567890123456", time.Now())
	if err != nil || j.ID != "legacy" || j.Status != "failed" {
		t.Fatal("old panel retry lost", j, err)
	}
	var principal string
	if err = s.DB.QueryRow("SELECT principal_id FROM instance_power_request WHERE job_id='legacy'").Scan(&principal); err != nil || principal != "panel" {
		t.Fatal(principal, err)
	}
	if err = s.CheckBackupIntegrity(ctx); err != nil {
		t.Fatal(err)
	}
}

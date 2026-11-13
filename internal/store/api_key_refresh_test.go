package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestAPIKeyRefreshSharesOnlyAcceptedMemberships(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a, _ := powerKey(t, s, "instance:control")
	b, _ := powerKey(t, s, "instance:control")
	c, _ := powerKey(t, s, "instance:control")
	limited, _ := powerKey(t, s, "summary:read")
	original, err := s.EnqueueJob(model.WithJobTrigger(ctx, "panel"), "refresh", "a", []string{"a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.APIKeyControlJob(ctx, a.ID, original.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("read granted membership", err)
	}
	for _, key := range []model.APIKey{a, b, a} {
		j, err := s.EnqueueAPIKeyRefresh(ctx, key.ID, "a", now)
		if err != nil || j.ID != original.ID || j.Trigger != "panel" {
			t.Fatal("coalesced read lost original creator", j, err)
		}
		if got, err := s.APIKeyControlJob(ctx, key.ID, j.ID); err != nil || got.ID != j.ID {
			t.Fatal(got, err)
		}
	}
	if _, err = s.APIKeyControlJob(ctx, c.ID, original.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("unjoined key could read task", err)
	}
	if _, err = s.APIKeyPowerJob(ctx, a.ID, original.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("legacy power-only reader accepted refresh", err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, limited.ID, "a", now); !errors.Is(err, ErrAPIKeyScope) {
		t.Fatal("missing control scope", err)
	}
	var members int
	if err = s.DB.QueryRow("SELECT count(*) FROM api_key_refresh_job").Scan(&members); err != nil || members != 2 {
		t.Fatal(members, err)
	}
	permission, err := s.EnqueueJob(ctx, "permission_check", "a", []string{"a"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.APIKeyControlJob(ctx, a.ID, permission.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("unrelated account job became visible", err)
	}
	if err = s.RevokeAPIKey(ctx, a.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.APIKeyControlJob(ctx, a.ID, original.ID); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked key kept history access", err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, a.ID, "a", now.Add(time.Second)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stale authentication created membership", err)
	}
	got, err := s.APIKeyControlJob(ctx, b.ID, original.ID)
	if err != nil || got.Status != "queued" || desired(t, s, "ecs") != "stopped" {
		t.Fatal("revocation cancelled independent collection or changed intent", got, err)
	}
	if _, err = s.DB.Exec("UPDATE api_key SET scopes_json='[\"summary:read\"]' WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, b.ID, "a", now); !errors.Is(err, ErrAPIKeyScope) {
		t.Fatal("store trusted stale scope", err)
	}
}

func TestAPIKeyRefreshCapturesAccountMembershipAndRejectsChangedScope(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a, _ := powerKey(t, s, "instance:control")
	b, _ := powerKey(t, s, "instance:control")
	original, err := s.EnqueueAPIKeyRefresh(model.WithJobTrigger(ctx, "cli"), a.ID, "", now)
	if err != nil || original.Trigger != "api" || !reflect.DeepEqual(original.AccountIDs, []string{"a"}) {
		t.Fatal(original, err)
	}
	second := model.Account{ID: "b", Name: "second", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}, ResetDay: 1, Thresholds: []int{80}, UpdatedAt: now}
	if _, err = s.CreateAccount(ctx, second, []byte("synthetic-credential-ciphertext-not-real"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, b.ID, "", now); !errors.Is(err, ErrRefreshScopeChanged) {
		t.Fatal("old task silently omitted new account", err)
	}
	if _, err = s.APIKeyControlJob(ctx, b.ID, original.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected request granted membership", err)
	}
	got, err := s.Job(ctx, original.ID)
	if err != nil || !reflect.DeepEqual(got.AccountIDs, []string{"a"}) {
		t.Fatal("queued account snapshot was rewritten", got, err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, b.ID, "b", now); err != nil {
		t.Fatal("explicit account cannot refresh after all-scope conflict", err)
	}
	if _, err = s.UpdateAccount(ctx, "b", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, a.ID, "b", now); !errors.Is(err, ErrAccountDisabled) {
		t.Fatal(err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, a.ID, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// The snapshot now matches the earlier job again; coalescing is valid.
	if got, err = s.EnqueueAPIKeyRefresh(ctx, b.ID, "", now); err != nil || got.ID != original.ID {
		t.Fatal(got, err)
	}
}

func TestAPIKeyRefreshConcurrentJoinsAndAtomicRollback(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a, _ := powerKey(t, s, "instance:control")
	b, _ := powerKey(t, s, "instance:control")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := a
			if i%2 != 0 {
				key = b
			}
			if _, err := s.EnqueueAPIKeyRefresh(ctx, key.ID, "a", now); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var jobs, members int
	if err := s.DB.QueryRow("SELECT count(*) FROM job WHERE type='refresh'").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT count(*) FROM api_key_refresh_job").Scan(&members); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || members != 2 {
		t.Fatal(jobs, members)
	}
	claimed, err := s.ClaimCollectionJob(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(ctx, claimed, nil, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER refresh_join_abort BEFORE INSERT ON api_key_refresh_job BEGIN SELECT RAISE(ABORT,'test join failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, a.ID, "a", now); err == nil {
		t.Fatal("orphaned task accepted")
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM job WHERE type='refresh'").Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("enqueue escaped failed membership transaction", jobs, err)
	}
	if _, err = s.DB.Exec("DROP TRIGGER refresh_join_abort"); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.EnqueueAPIKeyRefresh(ctx, a.ID, "a", now)
	if err != nil || fresh.ID == claimed.ID || fresh.Trigger != "api" {
		t.Fatal("completed reads prevented a new refresh", fresh, err)
	}
	if _, err = s.PrepareRestoredCopy(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := s.APIKeyControlJob(ctx, a.ID, fresh.ID)
	if err != nil || got.Status != "failed" || got.Error == nil || got.Error.Code != "backup_restored" {
		t.Fatal("restore lost membership or replayed refresh", got, err)
	}
	var violations int
	if err = s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Fatal(violations, err)
	}
}

func TestAPIKeyRefreshMigrationPreservesPowerOwnership(t *testing.T) {
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
		if f.Name() >= "038_api_key_refresh.sql" {
			break
		}
		raw, err := migrations.ReadFile("migrations/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(f.Name(), err)
		}
		if _, err = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", f.Name()); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO app_meta VALUES('mode','production')",
		"INSERT INTO account VALUES('a','account','china','now')",
		`INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{"id":"a","enabled":true}',1)`,
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','ecs','cn-shanghai','running','running','now')",
		`INSERT INTO api_key(id,name,scopes_json,token_hash,created_at) VALUES('old-key','test','["instance:control"]',zeroblob(32),'2026-11-12T00:00:00Z')`,
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,instance_id) VALUES('old-power','instance_power','power:ecs','["a"]','queued','2026-11-12T00:00:00Z','ecs')`,
		"INSERT INTO instance_power_job(job_id,account_revision,action) VALUES('old-power',1,'start')",
		"INSERT INTO api_key_power_job(job_id,key_id) VALUES('old-power','old-key')",
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.APIKeyControlJob(ctx, "old-key", "old-power")
	if err != nil || old.Type != "instance_power" || old.Status != "queued" || old.Sequence != 1 {
		t.Fatal(old, err)
	}
	var members int
	if err = s.DB.QueryRow("SELECT count(*) FROM api_key_refresh_job").Scan(&members); err != nil || members != 0 {
		t.Fatal("migration fabricated authorization", members, err)
	}
	if _, err = s.EnqueueAPIKeyRefresh(ctx, "old-key", "a", time.Now()); err != nil {
		t.Fatal(err)
	}
}

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func auditHistory(t *testing.T, s *Store) []model.AuditEntry {
	t.Helper()
	items, err := s.AuditLog(context.Background(), model.AuditFilter{Limit: 201})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestAuditAccountChangesAreAtomicAndExcludeCredentialValues(t *testing.T) {
	s := powerStore(t)
	ctx := model.WithJobTrigger(context.Background(), "cli")
	now := time.Now().UTC()
	// Pre-existing discovered state is not fabricated as a historical user action.
	if len(auditHistory(t, s)) != 0 {
		t.Fatal("invented audit backfill")
	}
	a := model.Account{ID: "audit-account", Name: "private account name", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}, QuotaMainland: 20 << 30, QuotaOverseas: 200 << 30, ResetDay: 1, Thresholds: []int{80, 95}, UpdatedAt: now}
	firstCipher := []byte("synthetic-encrypted-credential-one")
	if _, err := s.CreateAccount(ctx, a, firstCipher, time.Minute); err != nil {
		t.Fatal(err)
	}
	secondCipher := []byte("synthetic-encrypted-credential-two")
	updated, err := s.UpdateAccount(ctx, a.ID, func(a *model.Account) ([]byte, error) {
		a.Name = "replacement private name"
		a.Thresholds[0] = 75
		return secondCipher, nil
	}, now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	admin := model.WithJobTrigger(ctx, "panel")
	if _, err = s.UpdateAccount(admin, a.ID, func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now.Add(2*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	items := auditHistory(t, s)
	if len(items) != 3 || items[0].Action != "account_disabled" || items[0].Actor.Kind != "admin" || items[0].Details.CredentialsChanged || !reflect.DeepEqual(items[0].Details.ChangedFields, []string{"enabled"}) || items[1].Action != "account_updated" || items[1].Actor.Kind != "cli" || !items[1].Details.CredentialsChanged || !reflect.DeepEqual(items[1].Details.ChangedFields, []string{"name", "alert_thresholds_percent"}) || items[2].Action != "account_created" || !items[2].Details.CredentialsChanged {
		t.Fatal(items)
	}
	for i, e := range items {
		if e.AccountID == nil || *e.AccountID != a.ID || e.Details.Revision == nil || *e.Details.Revision != int64(3-i) {
			t.Fatal(e)
		}
	}
	raw, _ := json.Marshal(items)
	for _, secret := range []string{string(firstCipher), string(secondCipher), a.Name, updated.Name, "access_key_secret", "ciphertext", "token_hash"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("audit contains private configuration or credential material")
		}
	}
	before, _ := s.Account(ctx, a.ID)
	if _, err = s.DB.Exec("CREATE TRIGGER audit_reject BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'audit unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateAccount(admin, a.ID, func(a *model.Account) ([]byte, error) {
		a.Enabled = true
		a.Name = "must roll back"
		return []byte("must-roll-back-cipher"), nil
	}, now.Add(3*time.Second), time.Minute); err == nil {
		t.Fatal("mutation committed without audit")
	}
	after, err := s.Account(ctx, a.ID)
	cipher, e := s.AccountCipher(ctx, a.ID)
	if err != nil || e != nil || !reflect.DeepEqual(before, after) || !bytes.Equal(cipher, secondCipher) || len(auditHistory(t, s)) != 3 {
		t.Fatal("account/audit transaction was not atomic", err, e)
	}
	a.ID = "rollback-create"
	a.Name = "rollback-create"
	if _, err = s.CreateAccount(ctx, a, firstCipher, time.Minute); err == nil {
		t.Fatal("create committed without audit")
	}
	if _, err = s.Account(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial account create", err)
	}
}

func TestAuditCoversImplementedRuleAndProbeConfigurationWrites(t *testing.T) {
	s := agentStore(t)
	ctx := model.WithJobTrigger(context.Background(), "panel")
	now := time.Now().UTC()
	host, port := "1.1.1.1", 443
	target, err := s.SaveNetworkTarget(ctx, "", model.NetworkTarget{Name: "private target name", Kind: "custom", Host: &host, TCPPort: &port, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := model.DefaultProbePlan()
	plan.Name = "private plan name"
	plan.PointKind = "server"
	plan.TargetID = target.ID
	plan.Family = "ipv4"
	plan.Method = "tcp"
	plan.Enabled = true
	plan, err = s.SaveProbePlan(ctx, "", plan, now)
	if err != nil {
		t.Fatal(err)
	}
	plan.FrequencySeconds = 600
	plan, err = s.SaveProbePlan(ctx, plan.ID, plan, now)
	if err != nil {
		t.Fatal(err)
	}
	stale := target
	target.Host = new("8.8.8.8")
	target, err = s.SaveNetworkTarget(ctx, target.ID, target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveNetworkTarget(ctx, target.ID, stale, now); !errors.Is(err, ErrNetworkChanged) {
		t.Fatal(err)
	}
	spec := model.DefaultTraceSchedule()
	spec.Enabled = true
	spec.NotifyChanges = true
	if _, err = s.SaveTraceSchedule(ctx, plan.ID, spec, "UTC", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateAlertRule(ctx, "agent_offline", model.AlertRuleUpdate{Enabled: new(false)}, now); err != nil {
		t.Fatal(err)
	}
	quiet, err := s.QuietHours(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	quiet.Enabled = true
	if _, err = s.SaveQuietHours(ctx, quiet, now); err != nil {
		t.Fatal(err)
	}
	count := len(auditHistory(t, s))
	if err = s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err = s.ScheduleTraceSnapshots(ctx, "UTC", now); err != nil {
		t.Fatal(err)
	}
	if len(auditHistory(t, s)) != count {
		t.Fatal("scheduler progress polluted configuration audit")
	}
	if err = s.DeleteProbePlan(ctx, plan.ID, plan.Revision, now); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNetworkTarget(ctx, target.ID, target.Revision, now); err != nil {
		t.Fatal(err)
	}
	items := auditHistory(t, s)
	want := map[string]int{"probe_target_created": 1, "probe_target_updated": 1, "probe_target_deleted": 1, "probe_plan_created": 1, "probe_plan_updated": 1, "probe_plan_deleted": 1, "trace_schedule_updated": 1, "alert_rule_updated": 1, "quiet_hours_updated": 1}
	for _, e := range items {
		want[e.Action]--
		if e.Actor.Kind != "admin" || e.Actor.ID != nil || e.Details.CredentialsChanged {
			t.Fatal(e)
		}
		if e.Action == "probe_target_updated" && !reflect.DeepEqual(e.Details.ChangedFields, []string{"host"}) {
			t.Fatal(e)
		}
		if e.Action == "probe_plan_updated" && !reflect.DeepEqual(e.Details.ChangedFields, []string{"frequency_seconds"}) {
			t.Fatal(e)
		}
	}
	for action, n := range want {
		if n != 0 {
			t.Fatal("audit action coverage", action, n)
		}
	}
	raw, _ := json.Marshal(items)
	if bytes.Contains(raw, []byte(host)) || bytes.Contains(raw, []byte("8.8.8.8")) || bytes.Contains(raw, []byte("private target name")) || bytes.Contains(raw, []byte("private plan name")) {
		t.Fatal("configuration values entered audit before policy decision")
	}
}

func TestAuditPowerIntentDispatchAndObservedCompletion(t *testing.T) {
	s := powerStore(t)
	ctx := model.WithJobTrigger(context.Background(), "panel")
	now := time.Now().UTC()
	event := model.NewID("event_")
	j, err := s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", event, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{event, model.NewID("alias_")} {
		got, e := s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", key, now)
		if e != nil || got.ID != j.ID {
			t.Fatal(got, e)
		}
	}
	if len(auditHistory(t, s)) != 1 {
		t.Fatal("transport retry duplicated audit request")
	}
	if _, err = s.EnqueuePowerJob(ctx, "ecs", "start", "", event, now); !errors.Is(err, ErrPowerEventConflict) {
		t.Fatal(err)
	}
	p := claimPower(t, s, now.Add(time.Second))
	if err = s.SavePowerBaseline(ctx, p.Job, model.PowerObservation{Status: "running", ObservedAt: now.Add(time.Second)}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	worker := model.WithJobTrigger(ctx, "cli")
	if _, err = s.BeginPowerDispatch(worker, p.Job, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordPowerDispatch(worker, p.Job, "accepted", "", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(auditHistory(t, s)) != 4 {
		t.Fatal("acceptance fabricated completion")
	}
	done, err := s.ObservePower(worker, p.Job, model.PowerObservation{Status: "stopped", StoppedMode: new("keep_charging"), ObservedAt: now.Add(4 * time.Second)}, now.Add(4*time.Second))
	if err != nil || !done {
		t.Fatal(done, err)
	}
	items := auditHistory(t, s)
	want := []string{"instance_power_finished", "instance_power_dispatch_recorded", "instance_power_dispatch_started", "instance_power_started", "instance_power_requested"}
	if len(items) != len(want) {
		t.Fatal(items)
	}
	for i, e := range items {
		actor := "system"
		if i == 4 {
			actor = "admin"
		}
		if e.Action != want[i] || e.Actor.Kind != actor || e.TargetID != "ecs" || e.AccountID == nil || *e.AccountID != "a" || e.JobID == nil || *e.JobID != j.ID || e.Details.Operation != "stop" {
			t.Fatal(e)
		}
	}
	if items[0].Details.Status != "succeeded" || items[0].Details.ConfirmationBasis != "target_state" || items[1].Details.DispatchOutcome != "accepted" || items[2].Details.DispatchOutcome != "dispatching" {
		t.Fatal(items)
	}
	if _, err = s.EnqueuePowerJob(ctx, "ecs", "stop", "keep_charging", event, now.Add(time.Hour)); err != nil || len(auditHistory(t, s)) != 5 {
		t.Fatal("completed retry changed evidence", err)
	}
}

func TestAuditPowerCancellationRecoveryAndKeyRevocation(t *testing.T) {
	for _, reason := range []string{"api_key_revoked", "power_outcome_unknown", "backup_restored"} {
		t.Run(reason, func(t *testing.T) {
			s := powerStore(t)
			admin := model.WithJobTrigger(context.Background(), "panel")
			now := time.Now().UTC()
			key, token, err := s.CreateAPIKey(admin, model.APIKeyCreate{Name: "private key label", Scopes: []string{"instance:control"}}, now)
			if err != nil {
				t.Fatal(err)
			}
			ctx := model.WithAPIKeyPrincipal(context.Background(), key.ID)
			j, err := s.EnqueuePowerJob(ctx, "ecs", "start", "", model.NewID("event_"), now)
			if err != nil {
				t.Fatal(err)
			}
			if reason == "api_key_revoked" {
				if err = s.RevokeAPIKey(admin, key.ID, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				count := len(auditHistory(t, s))
				if err = s.RevokeAPIKey(admin, key.ID, now.Add(2*time.Second)); err != nil || len(auditHistory(t, s)) != count {
					t.Fatal("revocation retry duplicated audit", err)
				}
			} else {
				p := claimPower(t, s, now)
				if _, err = s.BeginPowerDispatch(ctx, p.Job, now); err != nil {
					t.Fatal(err)
				}
				if reason == "backup_restored" {
					_, err = s.PrepareRestoredCopy(admin, now.Add(time.Second))
				} else {
					_, err = s.ClaimPowerJob(ctx, now.Add(2*time.Minute))
					if errors.Is(err, ErrNotFound) {
						err = nil
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			items := auditHistory(t, s)
			finished, requested := 0, 0
			for _, e := range items {
				if e.Action == "instance_power_requested" {
					requested++
					if e.Actor.Kind != "api_key" || e.Actor.ID == nil || *e.Actor.ID != key.ID {
						t.Fatal(e)
					}
				}
				if e.Action == "instance_power_finished" {
					finished++
					outcome := "unknown"
					if reason == "api_key_revoked" {
						outcome = "not_sent"
					}
					if e.Actor.Kind != "system" || e.JobID == nil || *e.JobID != j.ID || e.Details.Status != "failed" || e.Details.ErrorCode != reason || e.Details.DispatchOutcome != outcome {
						t.Fatal(e)
					}
				}
			}
			if finished != 1 || requested != 1 {
				t.Fatal(items)
			}
			raw, _ := json.Marshal(items)
			if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte("private key label")) || bytes.Contains(raw, []byte("token_hash")) {
				t.Fatal("key material leaked to audit")
			}
		})
	}
}

func TestAuditFailurePreventsPowerDispatchAndKeyCreation(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	j := queuePower(t, s, "start", now)
	p := claimPower(t, s, now)
	if _, err := s.DB.Exec("CREATE TRIGGER audit_reject BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'audit unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginPowerDispatch(ctx, p.Job, now); err == nil {
		t.Fatal("dispatch permitted without committed audit")
	}
	got, err := s.PowerOperation(ctx, j.ID)
	if err != nil || got.DispatchStartedAt != nil || got.DispatchOutcome != "not_sent" {
		t.Fatal(got, err)
	}
	if _, token, err := s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "rollback", Scopes: []string{"summary:read"}}, now); err == nil || token != "" {
		t.Fatal("API key escaped failed transaction")
	}
	keys, err := s.APIKeys(ctx, 0, 201)
	if err != nil || len(keys) != 0 {
		t.Fatal("partial key insert", err)
	}
}

func TestAuditPaginationAndExactFilters(t *testing.T) {
	s := agentStore(t)
	ctx := model.WithJobTrigger(context.Background(), "telegram")
	now := time.Now().UTC()
	ids := []string{}
	for i := 0; i < 7; i++ {
		key, _, err := s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "not persisted in audit", Scopes: []string{"summary:read"}}, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, key.ID)
	}
	first, err := s.AuditLog(ctx, model.AuditFilter{Actor: "telegram", Action: "api_key_created", Limit: 3})
	if err != nil || len(first) != 3 || first[0].TargetID != ids[6] {
		t.Fatal(first, err)
	}
	if _, _, err = s.CreateAPIKey(context.Background(), model.APIKeyCreate{Name: "later", Scopes: []string{"summary:read"}}, now); err != nil {
		t.Fatal(err)
	}
	before, _ := strconv.ParseInt(first[2].ID, 10, 64)
	second, err := s.AuditLog(ctx, model.AuditFilter{Actor: "telegram", Action: "api_key_created", Before: before, Limit: 201})
	if err != nil || len(second) != 4 || second[0].TargetID != ids[3] {
		t.Fatal(second, err)
	}
	from, to := now.Add(time.Second), now.Add(3*time.Second)
	filtered, err := s.AuditLog(ctx, model.AuditFilter{Actor: "telegram", TargetType: "api_key", From: &from, To: &to, Limit: 50})
	if err != nil || len(filtered) != 2 || filtered[0].TargetID != ids[2] || filtered[1].TargetID != ids[1] {
		t.Fatal(filtered, err)
	}
	filtered, err = s.AuditLog(ctx, model.AuditFilter{TargetID: ids[0], Limit: 50})
	if err != nil || len(filtered) != 1 {
		t.Fatal(filtered, err)
	}
	for _, f := range []model.AuditFilter{{Limit: 0}, {Limit: 202}, {Limit: 50, Before: -1}, {Limit: 50, Actor: "untrusted"}, {Limit: 50, From: &from}, {Limit: 50, From: &to, To: &from}, {Limit: 50, Action: strings.Repeat("a", 81)}} {
		if _, err = s.AuditLog(ctx, f); !errors.Is(err, ErrAuditFilter) {
			t.Fatal("unbounded or invalid audit filter", err)
		}
	}
	if !slices.IsSortedFunc(second, func(a, b model.AuditEntry) int {
		aID, _ := strconv.ParseInt(a.ID, 10, 64)
		bID, _ := strconv.ParseInt(b.ID, 10, 64)
		if aID > bID {
			return -1
		}
		if aID < bID {
			return 1
		}
		return 0
	}) {
		t.Fatal("unstable audit sequence")
	}
}

func TestAuditAgentCredentialChangesAreTransactionalAndDeduplicated(t *testing.T) {
	s := agentStore(t)
	admin := model.WithJobTrigger(context.Background(), "panel")
	now := time.Now().UTC()
	code, _, err := s.AgentGrant(admin, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	r := registration(code, "i-test")
	result, err := s.RegisterAgent(context.Background(), r, now)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.RegisterAgent(context.Background(), r, now); err != nil || again.AgentID != result.AgentID {
		t.Fatal(err)
	}
	if err = s.RevokeAgent(admin, "ecs", now); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAgent(admin, "ecs", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	items := auditHistory(t, s)
	if len(items) != 3 || items[0].Action != "agent_revoked" || items[1].Action != "agent_registered" || items[1].Actor.Kind != "system" || items[2].Action != "agent_registration_issued" || items[2].Actor.Kind != "admin" {
		t.Fatal(items)
	}
	for _, e := range items {
		if !e.Details.CredentialsChanged || e.AccountID == nil || *e.AccountID != "a" || e.TargetID != "ecs" {
			t.Fatal(e)
		}
	}
	raw, _ := json.Marshal(items)
	if bytes.Contains(raw, []byte(code)) || bytes.Contains(raw, []byte(r.Nonce)) || bytes.Contains(raw, []byte(agentproto.DeriveToken(code, r.Nonce))) || bytes.Contains(raw, []byte(r.Metadata.InstanceID)) {
		t.Fatal("agent credential or metadata copied into audit")
	}
	if _, err = s.DB.Exec("CREATE TRIGGER audit_reject BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT,'audit unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	if code, _, err = s.AgentGrant(admin, "ecs", now.Add(2*time.Second)); err == nil || code != "" {
		t.Fatal("grant escaped failed audit transaction")
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM agent_registration").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial grant", err)
	}
}

func TestAuditMigrationPreservesExistingWorkWithoutInventedHistory(t *testing.T) {
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
		if file.Name() >= "037_audit_log.sql" {
			break
		}
		raw, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(file.Name(), err)
		}
		if _, err = db.Exec("INSERT INTO schema_migration VALUES(?,'now')", file.Name()); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO app_meta VALUES('mode','production')",
		"INSERT INTO account VALUES('a','account','china','now')",
		"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','ecs','cn-shanghai','running','running','now')",
		"INSERT INTO job(id,type,scope,account_ids_json,status,created_at,started_at,lease_until,instance_id) VALUES('old','instance_power','power:ecs','[\"a\"]','running','2026-11-12T00:00:00Z','2026-11-12T00:00:00Z',0,'ecs')",
		"INSERT INTO instance_power_job(job_id,account_revision,action,dispatch_started_at,dispatch_outcome) VALUES('old',1,'start','2026-11-12T00:00:01Z','dispatching')",
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
	if len(auditHistory(t, s)) != 0 {
		t.Fatal("migration fabricated old actor/history")
	}
	if _, err = s.ClaimPowerJob(ctx, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	items := auditHistory(t, s)
	if len(items) != 1 || items[0].Action != "instance_power_finished" || items[0].Details.DispatchOutcome != "unknown" || items[0].Details.ErrorCode != "power_outcome_unknown" || items[0].JobID == nil || *items[0].JobID != "old" {
		t.Fatal(items)
	}
	var violations int
	if err = s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Fatal(violations, err)
	}
}

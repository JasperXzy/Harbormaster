package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestProbeRetentionKeepsAuthenticatedDuplicateACKAndIdempotency(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	at := now.Add(-31 * 24 * time.Hour)
	key := strings.Repeat("retention", 4)
	j, err := s.EnqueueAgentProbe(ctx, "ecs", key, probeRequest(), at)
	if err != nil {
		t.Fatal(err)
	}
	owner := strings.Repeat("a", 64)
	if _, err = s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: owner}, at); err != nil {
		t.Fatal(err)
	}
	report := probeReport(j.ID, owner, agent, at)
	if _, err = s.AcceptProbeResult(ctx, token, report, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", j.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(raw))
	out, err := s.PruneProbeHistory(ctx, now, 30, 10)
	if err != nil || out.Payloads != 1 || out.More {
		t.Fatal(out, err)
	}
	var retained sql.NullString
	var digest []byte
	if err = s.DB.QueryRow("SELECT result_json,result_digest FROM network_probe_job WHERE job_id=?", j.ID).Scan(&retained, &digest); err != nil || retained.Valid || string(digest) != string(wantDigest[:]) {
		t.Fatal("raw result retained or ACK digest lost", err)
	}
	ack, err := s.AcceptProbeResult(ctx, token, report, now)
	if err != nil || !ack.Duplicate {
		t.Fatal("ACK lost after retention", ack, err)
	}
	report.Result.Samples[0].RTTMS = new(float64(13))
	if _, err = s.AcceptProbeResult(ctx, token, report, now); !errors.Is(err, ErrConflict) {
		t.Fatal("modified expired result accepted", err)
	}
	report.Owner = strings.Repeat("b", 64)
	if _, err = s.AcceptProbeResult(ctx, token, report, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("digest bypassed owner binding", err)
	}
	again, err := s.EnqueueAgentProbe(ctx, "ecs", key, probeRequest(), now)
	if err != nil || again.ID != j.ID || again.Status != "succeeded" {
		t.Fatal("retention replayed an old request key", again, err)
	}
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "succeeded" {
		t.Fatal(job, err)
	}
	encoded, _ := json.Marshal(job.Result)
	if !strings.Contains(string(encoded), `"result_expired":true`) || strings.Contains(string(encoded), "rtt") || strings.Contains(string(encoded), token) {
		t.Fatal("invalid expiry projection", string(encoded))
	}
	out, err = s.PruneProbeHistory(ctx, now.Add(time.Hour), 30, 10)
	if err != nil || out.Payloads != 0 || out.HistoryRows != 0 || out.More {
		t.Fatal("cleanup repeated", out, err)
	}
}

func finishRetentionProbe(t *testing.T, s *Store, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	if err := s.ScheduleProbePlans(ctx, at); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimServerProbe(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	result := netprobe.Result{StartedAt: at, FinishedAt: at.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: at, RTTMS: new(float64(4))}}}
	if err = s.FinishServerProbe(ctx, claim, result, "", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return claim.Job.ID
}

func TestProbeRetentionBoundariesHistoryAndActiveWork(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour)
	plan := createProbePlan(t, s, "target_cloudflare_dns", old)
	first := finishRetentionProbe(t, s, old)
	// A terminal result exactly at the cutoff remains retained.
	boundary := finishRetentionProbe(t, s, now.Add(-30*24*time.Hour-time.Second))
	if _, err := s.DB.Exec("INSERT INTO probe_alert_progress(rule_key,plan_id,last_seq,updated_at) VALUES('network_latency',?,123,?)", plan.ID, timestamp(old)); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleProbePlans(ctx, now); err != nil {
		t.Fatal(err)
	}
	var active string
	if err := s.DB.QueryRow("SELECT id FROM job WHERE type='network_probe' AND status='queued'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	out, err := s.PruneProbeHistory(ctx, now, 30, 1)
	if err != nil || out.Payloads != 1 || out.HistoryRows != 1 || !out.More {
		t.Fatal(out, err)
	}
	for _, id := range []string{boundary, active} {
		var marked sql.NullString
		if err = s.DB.QueryRow("SELECT result_pruned_at FROM network_probe_job WHERE job_id=?", id).Scan(&marked); err != nil || marked.Valid {
			t.Fatal("boundary/active task pruned", id, err)
		}
	}
	var raw sql.NullString
	if err = s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", first).Scan(&raw); err != nil || raw.Valid {
		t.Fatal(err)
	}
	var watermark int64
	if err = s.DB.QueryRow("SELECT last_seq FROM probe_alert_progress WHERE plan_id=? AND rule_key='network_latency'", plan.ID).Scan(&watermark); err != nil || watermark != 123 {
		t.Fatal("retention changed alert evidence watermark", watermark, err)
	}
	runs, err := s.ProbeRuns(ctx, plan.ID, old.Add(-time.Second), old.Add(time.Minute), 0, 10)
	if err != nil || len(runs) != 0 {
		t.Fatal("expired run still visible", runs, err)
	}
	out, err = s.PruneProbeHistory(ctx, now, 30, 1)
	if err != nil || out.Payloads != 0 || out.HistoryRows != 0 || out.More {
		t.Fatal(out, err)
	}
}

func TestProbeRetentionRollbackTraceIsolationAndSkippedRuns(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	plan := createProbePlan(t, s, "target_cloudflare_dns", old)
	id := finishRetentionProbe(t, s, old)
	trace, err := s.EnqueueServerTrace(ctx, strings.Repeat("t", 32), netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, MaxHops: 3, Count: 1, TimeoutMS: 1000}, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE network_probe_job SET result_json='{\"trace\":\"retained fixture\"}' WHERE job_id=?", trace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE job SET status='failed',finished_at=? WHERE id=?", timestamp(old), trace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO probe_plan_run(plan_id,plan_revision,target_id,target_revision,point_kind,created_at,skip_reason) VALUES(?,1,?,1,'server',?,'point_unavailable')", plan.ID, plan.TargetID, timestamp(old.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER retention_abort BEFORE DELETE ON probe_plan_run BEGIN SELECT RAISE(ABORT,'test rollback'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PruneProbeHistory(ctx, now, 30, 10); err == nil {
		t.Fatal("partial cleanup committed")
	}
	var raw, marker sql.NullString
	if err = s.DB.QueryRow("SELECT result_json,result_pruned_at FROM network_probe_job WHERE job_id=?", id).Scan(&raw, &marker); err != nil || !raw.Valid || marker.Valid {
		t.Fatal("payload escaped rollback", err)
	}
	if _, err = s.DB.Exec("DROP TRIGGER retention_abort"); err != nil {
		t.Fatal(err)
	}
	out, err := s.PruneProbeHistory(ctx, now, 30, 10)
	if err != nil || out.Payloads != 1 || out.HistoryRows != 2 {
		t.Fatal(out, err)
	}
	if err = s.DB.QueryRow("SELECT result_json,result_pruned_at FROM network_probe_job WHERE job_id=?", trace.ID).Scan(&raw, &marker); err != nil || !raw.Valid || !strings.Contains(raw.String, "retained fixture") || marker.Valid {
		t.Fatal("trace was pruned as latency data", err)
	}
	var violations int
	if err = s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Fatal(violations, err)
	}
	for _, args := range [][2]int{{0, 10}, {3651, 10}, {30, 0}, {30, 1001}} {
		if _, err = s.PruneProbeHistory(ctx, now, args[0], args[1]); err == nil {
			t.Fatal("invalid retention limits accepted")
		}
	}
}

func TestProbeRetentionMigrationQueuesExistingTerminalLatency(t *testing.T) {
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
		if f.Name() >= "039_latency_probe_retention.sql" {
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
		`INSERT INTO job(id,type,scope,account_ids_json,status,created_at,finished_at) VALUES('legacy','network_probe','probe:legacy','[]','succeeded','2026-01-01T00:00:00.000000000Z','2026-01-01T00:00:01.000000000Z')`,
		`INSERT INTO network_probe_job(job_id,request_key,point_kind,request_json,queued_until,action,result_json) VALUES('legacy','legacy-request-key','server','{}',0,'probe','{"result":"legacy fixture"}')`,
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
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM latency_probe_retention").Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy result not queued", count, err)
	}
	out, err := s.PruneProbeHistory(ctx, time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), 30, 10)
	if err != nil || out.Payloads != 1 {
		t.Fatal(out, err)
	}
	job, err := s.Job(ctx, "legacy")
	if err != nil || job.Status != "succeeded" || job.Sequence != 1 {
		t.Fatal("migration changed task proof", job, err)
	}
}

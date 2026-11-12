package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func completedTrace(t *testing.T, s *Store, key string, now time.Time) model.TraceAccepted {
	t.Helper()
	in := traceSubmission()
	in.RequestKey = strings.Repeat(key, 32)
	a, err := s.SubmitTrace(context.Background(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimServerTrace(context.Background(), now)
	if err != nil || c.Job.ID != a.JobID {
		t.Fatal(c, err)
	}
	if err = s.FinishServerTrace(context.Background(), c, echoTraceResult(now), "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTraceEnrichmentLeaseReclaimsOnlyReadOnlyWorkAndFreezesResults(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := completedTrace(t, s, "a", now)
	b := completedTrace(t, s, "b", now)
	var rawBefore string
	if err := s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", a.JobID).Scan(&rawBefore); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan TraceEnrichmentClaim, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.ClaimTraceEnrichment(ctx, now.Add(2*time.Second))
			if err == nil {
				claims <- c
			} else if !errors.Is(err, ErrNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(claims)
	var first TraceEnrichmentClaim
	count := 0
	for c := range claims {
		first = c
		count++
	}
	if count != 1 || first.TraceID != a.TraceID {
		t.Fatal("concurrent enrichment", count, first)
	}
	if _, err := s.ClaimTraceEnrichment(ctx, now.Add(3*time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimTraceEnrichment(ctx, first.ExpiresAt.Add(time.Second))
	if err != nil || reclaimed.TraceID != a.TraceID || reclaimed.Owner == first.Owner {
		t.Fatal(reclaimed, err)
	}
	result := &model.TraceEnrichment{Status: "complete", EnrichedAt: first.ExpiresAt.Add(time.Second), DetourStatus: "unconfirmed"}
	if err = s.FinishTraceEnrichment(ctx, first, result, "", first.ExpiresAt.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("old owner overwrote", err)
	}
	if err = s.FinishTraceEnrichment(ctx, reclaimed, result, "", first.ExpiresAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishTraceEnrichment(ctx, reclaimed, result, "", first.ExpiresAt.Add(3*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("finished history rewritten", err)
	}
	d, err := s.Trace(ctx, a.TraceID)
	if err != nil || d.EnrichmentStatus != "complete" || d.Enrichment == nil || !d.UpdatedAt.After(now.Add(time.Second)) || d.Status != "succeeded" {
		t.Fatal(d, err)
	}
	var rawAfter string
	if err = s.DB.QueryRow("SELECT result_json FROM network_probe_job WHERE job_id=?", a.JobID).Scan(&rawAfter); err != nil || rawAfter != rawBefore {
		t.Fatal("raw measurement modified", err)
	}
	second, err := s.ClaimTraceEnrichment(ctx, first.ExpiresAt.Add(3*time.Second))
	if err != nil || second.TraceID != b.TraceID {
		t.Fatal(second, err)
	}
	// Summary pagination must not decode large enrichment or raw packet JSON.
	if _, err = s.DB.Exec("UPDATE trace_enrichment SET result_json='bad json' WHERE trace_seq=?", a.TraceID); err != nil {
		t.Fatal(err)
	}
	items, err := s.Traces(ctx, model.TraceFilter{From: now.Add(-time.Second), To: now.Add(time.Hour), Limit: 10})
	if err != nil || len(items) != 2 || items[1].EnrichmentStatus != "complete" {
		t.Fatal(items, err)
	}
}

func TestTraceEnrichmentMigrationBackfillsOnceAndRestoreCancelsPendingWork(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := completedTrace(t, s, "a", now)
	b := completedTrace(t, s, "b", now)
	if _, err := s.DB.Exec("DROP TABLE trace_enrichment"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM schema_migration WHERE name='034_trace_enrichment.sql'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM trace_enrichment").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	c, err := s.ClaimTraceEnrichment(ctx, now.Add(2*time.Second))
	if err != nil || c.TraceID != a.TraceID {
		t.Fatal(c, err)
	}
	if _, err = s.PrepareRestoredCopy(ctx, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.TraceID, b.TraceID} {
		d, err := s.Trace(ctx, id)
		if err != nil || d.EnrichmentStatus != "failed" || d.EnrichmentError == nil || d.EnrichmentError.Code != "backup_restored" || d.Result == nil || d.Status != "succeeded" {
			t.Fatal(d, err)
		}
	}
	if _, err = s.ClaimTraceEnrichment(ctx, c.ExpiresAt.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatal("restore replayed old lookups", err)
	}
}

func TestTraceResultAndEnrichmentQueueCommitAtomically(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a, err := s.SubmitTrace(ctx, traceSubmission(), now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimServerTrace(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("CREATE TRIGGER reject_enrichment BEFORE INSERT ON trace_enrichment BEGIN SELECT RAISE(ABORT,'test failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishServerTrace(ctx, c, echoTraceResult(now), "", now.Add(time.Second)); err == nil {
		t.Fatal("queue error ignored")
	}
	d, err := s.Trace(ctx, a.TraceID)
	if err != nil || d.Status != "running" || d.Result != nil || d.Outcome != nil || d.EnrichmentStatus != "waiting_result" {
		t.Fatal("partially committed result", d, err)
	}
	if _, err = s.DB.Exec("DROP TRIGGER reject_enrichment"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishServerTrace(ctx, c, echoTraceResult(now), "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err = s.Trace(ctx, a.TraceID)
	if err != nil || d.Status != "succeeded" || d.EnrichmentStatus != "queued" {
		t.Fatal(d, err)
	}
}

package traceinfo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func traceDatabase(t *testing.T) (*store.Store, model.TraceAccepted) {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	in := model.DefaultTraceSubmission()
	in.RequestKey = strings.Repeat("t", 32)
	in.PointKind = "server"
	in.Family = "ipv4"
	in.Host = new("1.1.1.1")
	in.Method = "icmp"
	in.Count = 1
	in.MaxHops = 1
	now := time.Now().UTC()
	a, err := s.SubmitTrace(context.Background(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimServerTrace(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	r := netprobe.TraceResult{StartedAt: now, FinishedAt: now, Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Hops: []netprobe.TraceHop{{TTL: 1, Samples: []netprobe.TraceSample{{Sequence: 1, StartedAt: now, Address: new("1.1.1.1"), RTTMS: new(float64(10)), Reply: new("echo_reply"), ICMPType: new(0), ICMPCode: new(0)}}}}}
	if err = s.FinishServerTrace(context.Background(), c, r, "", now); err != nil {
		t.Fatal(err)
	}
	return s, a
}

func TestRunnerPersistsFrozenLookupAndNeverRerunsCompletedWork(t *testing.T) {
	s, a := traceDatabase(t)
	db, err := ipinfo.New(ipinfo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	calls := 0
	runner := Runner{Store: s, Enricher: Enricher{Database: db, Resolver: resolverFunc(func(ctx context.Context, ip string) ([]string, error) {
		calls++
		return []string{"router.example."}, nil
	})}}
	worked, err := runner.RunOne(context.Background())
	if err != nil || !worked || calls != 1 {
		t.Fatal(worked, err, calls)
	}
	d, err := s.Trace(context.Background(), a.TraceID)
	if err != nil || d.Enrichment == nil || d.Enrichment.Addresses[0].ASN.Number != 13335 || d.EnrichmentStatus != "complete" || d.Enrichment.Addresses[0].PTR.Names[0] != "router.example" {
		t.Fatal(d, err)
	}
	before, _ := json.Marshal(d.Enrichment)
	db.Close()
	worked, err = runner.RunOne(context.Background())
	if err != nil || worked || calls != 1 {
		t.Fatal("completed lookup replayed", worked, err, calls)
	}
	d, err = s.Trace(context.Background(), a.TraceID)
	after, _ := json.Marshal(d.Enrichment)
	if err != nil || string(before) != string(after) {
		t.Fatal("database change rewrote history", err)
	}
}

func TestRunnerQuarantinesMalformedStoredReportAndCancellationKeepsLease(t *testing.T) {
	for _, mode := range []string{"invalid", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, a := traceDatabase(t)
			ctx := context.Background()
			runner := Runner{Store: s}
			if mode == "invalid" {
				if _, err := s.DB.Exec("UPDATE network_probe_job SET result_json='bad json' WHERE job_id=?", a.JobID); err != nil {
					t.Fatal(err)
				}
				worked, err := runner.RunOne(ctx)
				if err != nil || !worked {
					t.Fatal(worked, err)
				}
				var state, code string
				if err = s.DB.QueryRow("SELECT state,error_code FROM trace_enrichment WHERE trace_seq=?", a.TraceID).Scan(&state, &code); err != nil || state != "failed" || code != "trace_enrichment_invalid" {
					t.Fatal(state, code, err)
				}
				return
			}
			runner.Enricher.Resolver = resolverFunc(func(ctx context.Context, _ string) ([]string, error) { <-ctx.Done(); return nil, ctx.Err() })
			ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			worked, err := runner.RunOne(ctx)
			if !worked || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(worked, err)
			}
			d, err := s.Trace(context.Background(), a.TraceID)
			if err != nil || d.Enrichment != nil || d.EnrichmentStatus != "running" || d.Status != "succeeded" {
				t.Fatal(d, err)
			}
		})
	}
}

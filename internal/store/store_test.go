package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var journal string
	if err = s.DB.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("WAL: %q %v", journal, err)
	}
	if _, err = s.DB.Exec("INSERT INTO instance VALUES('bad','missing','x','ecs','x','x','running','running','x')"); err == nil {
		t.Fatal("foreign key not enforced")
	}
	if err = s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow("SELECT count(*) FROM instance").Scan(&n)
	if n != 3 {
		t.Fatal(n)
	}
	if other, err := Open(ctx, dir, false); err == nil {
		other.Close()
		t.Fatal("production opened demo db")
	}
	_, _ = s.DB.Exec("INSERT INTO session VALUES(x'00','2099-01-01T00:00:00Z')")
	if err = s.ResetPassword(ctx, "$argon2id$test-only"); err != nil {
		t.Fatal(err)
	}
	_ = s.DB.QueryRow("SELECT count(*) FROM session").Scan(&n)
	if n != 0 {
		t.Fatal("sessions survived password reset")
	}
}

func TestReportIdempotencyAndAuthentication(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := agentproto.Report{Version: 1, AgentID: agentproto.DemoAgentID, CounterEpoch: "epoch-a", Sequence: 1, SampledAt: now, UptimeMS: 1, RXBytes: 100, TXBytes: 200}
	if _, err = s.AcceptReport(ctx, "wrong", r, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if dup, err := s.AcceptReport(ctx, agentproto.DemoToken, r, now); err != nil || dup {
		t.Fatal(err)
	}
	if dup, err := s.AcceptReport(ctx, agentproto.DemoToken, r, now.Add(time.Second)); err != nil || !dup {
		t.Fatal(err)
	}
	var last string
	_ = s.DB.QueryRow("SELECT last_received_at FROM agent").Scan(&last)
	if last != timestamp(now) {
		t.Fatal("replay refreshed heartbeat")
	}
	r.RXBytes++
	if _, err = s.AcceptReport(ctx, agentproto.DemoToken, r, now); !errors.Is(err, ErrConflict) {
		t.Fatal("changed duplicate accepted")
	}
	r.Sequence = 2
	r.UptimeMS = 2
	r.RXBytes = 10
	if _, err = s.AcceptReport(ctx, agentproto.DemoToken, r, now); !errors.Is(err, ErrConflict) {
		t.Fatal("counter rollback accepted")
	}
	r.CounterEpoch = "epoch-b"
	if _, err = s.AcceptReport(ctx, agentproto.DemoToken, r, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	r.CounterEpoch = "epoch-a"
	r.Sequence = 3
	if _, err = s.AcceptReport(ctx, agentproto.DemoToken, r, now.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("old epoch resurrected")
	}
	_, _ = s.DB.Exec("UPDATE agent SET revoked_at=?", timestamp(now))
	r.CounterEpoch = "epoch-c"
	if _, err = s.AcceptReport(ctx, agentproto.DemoToken, r, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked token accepted")
	}
}

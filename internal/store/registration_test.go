package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

func agentStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, query := range []string{
		"INSERT INTO account VALUES('a','account','china','now')",
		"INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)",
		"INSERT INTO instance VALUES('ecs','a','i-test','ecs','ecs','cn-shanghai','running','stopped','now')",
		"INSERT INTO instance VALUES('swas','a','swas-uuid','swas','swas','cn-shanghai','running','stopped','now')",
		"INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')",
		"INSERT INTO resource_snapshot VALUES('a','swas','swas','cn-shanghai','{}',1,'now')",
	} {
		if _, err = s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func registration(code, instance string) agentproto.Registration {
	return agentproto.Registration{Version: 1, Code: code, Nonce: strings.Repeat("a", 64), Metadata: agentproto.Metadata{InstanceID: instance, RegionID: "cn-shanghai"}, AgentVersion: "test"}
}
func TestAgentRegistrationPrebindingRetriesRotationAndRevocation(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	code, expiry, err := s.AgentGrant(ctx, "ecs", now)
	if err != nil || !expiry.Equal(now.Add(10*time.Minute)) {
		t.Fatal(err)
	}
	r := registration(code, "i-wrong")
	if _, err = s.RegisterAgent(ctx, r, now); !errors.Is(err, ErrIdentity) {
		t.Fatal("ECS identity mismatch accepted", err)
	}
	r.Metadata.InstanceID = "i-test"
	registered, err := s.RegisterAgent(ctx, r, now)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, r.Nonce)
	retry, err := s.RegisterAgent(ctx, r, now.Add(time.Second))
	if err != nil || retry.AgentID != registered.AgentID {
		t.Fatal("lost response cannot retry", err)
	}
	r.Nonce = strings.Repeat("b", 64)
	if _, err = s.RegisterAgent(ctx, r, now); !errors.Is(err, ErrRegistrationConsumed) {
		t.Fatal("consumed code reused by different client", err)
	}
	newCode, _, err := s.AgentGrant(ctx, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	report := agentproto.Report{Version: 1, AgentID: registered.AgentID, CounterEpoch: "first", Sequence: 1, SampledAt: now, UptimeMS: 1000, RXBytes: 100, TXBytes: 50}
	if _, err = s.AcceptReport(ctx, token, report, now); err != nil {
		t.Fatal("issuing grant revoked active token", err)
	}
	r = registration(newCode, "i-test")
	rotated, err := s.RegisterAgent(ctx, r, now)
	if err != nil || rotated.AgentID != registered.AgentID {
		t.Fatal("rotation changed binding", err)
	}
	if _, err = s.AcceptReport(ctx, token, report, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("old token survived completed rotation", err)
	}
	if _, err = s.RegisterAgent(ctx, r, expiry); !errors.Is(err, ErrRegistration) {
		t.Fatal("expired grant accepted", err)
	}
	newToken := agentproto.DeriveToken(newCode, r.Nonce)
	if _, err = s.AcceptReport(ctx, newToken, report, now); err != nil {
		t.Fatal("new token rejected duplicate report", err)
	}
	if err = s.RevokeAgent(ctx, "ecs", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptReport(ctx, newToken, report, now); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revocation ineffective")
	}
	if _, err = s.RegisterAgent(ctx, r, now); !errors.Is(err, ErrRegistration) {
		t.Fatal("revoked code resurrected token")
	}
	lightCode, _, err := s.AgentGrant(ctx, "swas", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterAgent(ctx, registration(lightCode, "i-underlying-ecs"), now); err != nil {
		t.Fatal("approved SWAS prebinding rejected", err)
	}
	if _, _, err = s.AgentGrant(ctx, "unmanaged", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("unmanaged instance got grant")
	}
	if _, err = s.DB.Exec("UPDATE account_settings SET enabled=0"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AgentGrant(ctx, "ecs", now); !errors.Is(err, ErrAccountDisabled) {
		t.Fatal("disabled account got grant")
	}
}

func TestBatchesAreAtomicStaleDoesNotMeanOnlineAndEpochLedgerSurvivesRetention(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	code, _, err := s.AgentGrant(ctx, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	r := registration(code, "i-test")
	g, err := s.RegisterAgent(ctx, r, now)
	if err != nil {
		t.Fatal(err)
	}
	token := agentproto.DeriveToken(code, r.Nonce)
	first := agentproto.Report{Version: 1, AgentID: g.AgentID, CounterEpoch: "first", Sequence: 1, SampledAt: now.Add(-5 * time.Minute), UptimeMS: 1000, RXBytes: 100, TXBytes: 50, AgentVersion: "test", RulesVersion: "nft-public-v1"}
	bad := first
	bad.Sequence = 2
	bad.UptimeMS = 2000
	bad.RXBytes = 99
	if _, err = s.AcceptReports(ctx, token, []agentproto.Report{first, bad}, now); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting batch accepted", err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM traffic_sample").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial batch committed")
	}
	second := first
	second.Sequence = 2
	second.UptimeMS = 2000
	second.RXBytes = 200
	second.SampledAt = first.SampledAt.Add(time.Second)
	if _, err = s.AcceptReports(ctx, token, []agentproto.Report{first, second}, now); err != nil {
		t.Fatal(err)
	}
	var online int
	if err = s.DB.QueryRow("SELECT count(*) FROM agent WHERE last_received_at IS NOT NULL").Scan(&online); err != nil || online != 0 {
		t.Fatal("old backlog made agent online")
	}
	fresh := second
	fresh.Sequence = 3
	fresh.UptimeMS = 3000
	fresh.SampledAt = now
	fresh.RXBytes = 300
	if _, err = s.AcceptReport(ctx, token, fresh, now); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM agent WHERE last_received_at IS NOT NULL").Scan(&online); err != nil || online != 1 {
		t.Fatal("fresh sample not recognized")
	}
	changed := fresh
	changed.DroppedSamples = 1
	if _, err = s.AcceptReport(ctx, token, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal("changed duplicate accepted")
	}
	next := fresh
	next.CounterEpoch = "second"
	next.Sequence = 1
	next.UptimeMS = 10
	next.RXBytes = 0
	next.TXBytes = 0
	if _, err = s.AcceptReport(ctx, token, next, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("DELETE FROM traffic_sample WHERE counter_epoch='first'"); err != nil {
		t.Fatal(err)
	}
	old := fresh
	old.Sequence = 99
	old.UptimeMS = 10000
	old.RXBytes = 999
	if _, err = s.AcceptReport(ctx, token, old, now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal("retained epoch resurrected after sample cleanup", err)
	}
	if _, err = s.DB.Exec("UPDATE account_settings SET enabled=0"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptReport(ctx, token, next, now); !errors.Is(err, ErrAccountDisabled) {
		t.Fatal("disabled account still receives traffic", err)
	}
}

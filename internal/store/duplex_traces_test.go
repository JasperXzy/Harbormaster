package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

type duplexFixture struct {
	s              *Store
	now            time.Time
	agents, tokens map[string]string
}

func newDuplexFixture(t *testing.T) duplexFixture {
	t.Helper()
	f := duplexFixture{s: agentStore(t), now: time.Now().UTC(), agents: map[string]string{}, tokens: map[string]string{}}
	ctx := context.Background()
	for index, id := range []string{"ecs", "swas"} {
		v4 := []string{"1.1.1.1", "8.8.8.8"}[index]
		v6 := []string{"2606:4700:4700::1111", "2001:4860:4860::8888"}[index]
		if _, err := f.s.DB.Exec("INSERT INTO instance_probe_address(instance_id,credential_version,observed_at,ipv4_json,ipv6_json) VALUES(?,(SELECT credential_version FROM account_settings WHERE account_id='a'),?,?,?)", id, timestamp(f.now), `["`+v4+`"]`, `["`+v6+`"]`); err != nil {
			t.Fatal(err)
		}
		code, _, err := f.s.AgentGrant(ctx, id, f.now)
		if err != nil {
			t.Fatal(err)
		}
		metadata := []string{"i-test", "i-underlying-ecs"}[index]
		request := registration(code, metadata)
		registered, err := f.s.RegisterAgent(ctx, request, f.now)
		if err != nil {
			t.Fatal(err)
		}
		f.agents[id], f.tokens[id] = registered.AgentID, agentproto.DeriveToken(code, request.Nonce)
		if _, err = f.s.AcceptReport(ctx, f.tokens[id], agentproto.Report{Version: 1, AgentID: registered.AgentID, CounterEpoch: "duplex", Sequence: 1, SampledAt: f.now, UptimeMS: 10000}, f.now); err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.ClaimAgentTrace(ctx, f.tokens[id], agentproto.ProbeClaim{AgentID: registered.AgentID, Owner: strings.Repeat("a", 64)}, f.now); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func duplexInput() model.DuplexTraceSubmission {
	in := model.DefaultDuplexTraceSubmission()
	in.RequestKey = strings.Repeat("d", 32)
	in.FromInstanceID, in.ToInstanceID = "ecs", "swas"
	in.Family, in.Method = "ipv4", "tcp"
	in.ForwardPort, in.ReversePort = 443, 8443
	in.MaxHops, in.Count = 3, 1
	return in
}
func (f duplexFixture) claim(t *testing.T, instance, owner string, at time.Time) *agentproto.TraceTask {
	t.Helper()
	v, err := f.s.ClaimAgentTrace(context.Background(), f.tokens[instance], agentproto.ProbeClaim{AgentID: f.agents[instance], Owner: owner}, at)
	if err != nil || v.Task == nil {
		t.Fatal(v, err)
	}
	return v.Task
}
func (f duplexFixture) complete(t *testing.T, instance string, task *agentproto.TraceTask, at time.Time) agentproto.TraceReport {
	t.Helper()
	r := traceReport(task.ID, task.Owner, f.agents[instance], at)
	r.Result.Address = task.Request.Host
	r.Result.ResolvedAddresses = []string{task.Request.Host}
	r.Result.Hops[0].Samples[0].Address = new(task.Request.Host)
	ack, err := f.s.AcceptTraceResult(context.Background(), f.tokens[instance], r, at.Add(time.Second))
	if err != nil || ack.Duplicate {
		t.Fatal(ack, err)
	}
	return r
}

func TestDuplexTraceAtomicConcurrentIdempotencyAndIndependentAgentResults(t *testing.T) {
	for _, family := range []string{"ipv4", "ipv6"} {
		t.Run(family, func(t *testing.T) {
			f := newDuplexFixture(t)
			ctx := context.Background()
			in := duplexInput()
			in.Family = family
			var wg sync.WaitGroup
			answers := make(chan model.DuplexTraceAccepted, 4)
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					a, err := f.s.SubmitDuplexTrace(ctx, in, f.now)
					if err != nil {
						t.Error(err)
					}
					answers <- a
				}()
			}
			wg.Wait()
			close(answers)
			var a model.DuplexTraceAccepted
			for v := range answers {
				if a.DuplexID != "" && a != v {
					t.Fatal("duplicate created another pair", a, v)
				}
				a = v
			}
			var pairs, legs int
			if err := f.s.DB.QueryRow("SELECT count(*) FROM duplex_trace").Scan(&pairs); err != nil || pairs != 1 {
				t.Fatal(pairs, err)
			}
			if err := f.s.DB.QueryRow("SELECT count(*) FROM duplex_trace_leg").Scan(&legs); err != nil || legs != 2 {
				t.Fatal(legs, err)
			}
			d, err := f.s.DuplexTrace(ctx, a.DuplexID)
			if err != nil || d.From.Kind != "ecs" || d.To.Kind != "swas" || d.Forward.Request.Host != d.To.PublicAddress || d.Reverse.Request.Host != d.From.PublicAddress || d.Forward.Request.Port != 443 || d.Reverse.Request.Port != 8443 || d.Forward.Status != "queued" || d.Reverse.Status != "queued" {
				t.Fatal(d, err)
			}
			if d.Forward.DuplexID == nil || *d.Forward.DuplexID != a.DuplexID || *d.Forward.DuplexDirection != "forward" || *d.Reverse.DuplexDirection != "reverse" {
				t.Fatal(d)
			}
			changed := in
			changed.ReversePort = 443
			if _, err = f.s.SubmitDuplexTrace(ctx, changed, f.now); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			forward := f.claim(t, "ecs", strings.Repeat("f", 64), f.now.Add(time.Second))
			reverse := f.claim(t, "swas", strings.Repeat("b", 64), f.now.Add(3*time.Second))
			if forward.ID != a.Forward.JobID || reverse.ID != a.Reverse.JobID {
				t.Fatal(forward, reverse, a)
			}
			report := f.complete(t, "ecs", forward, f.now.Add(time.Second))
			d, err = f.s.DuplexTrace(ctx, a.DuplexID)
			if err != nil || d.Forward.Status != "succeeded" || d.Reverse.Status != "running" || d.Reverse.Result != nil {
				t.Fatal("directions did not remain independent", d, err)
			}
			ack, err := f.s.AcceptTraceResult(ctx, f.tokens["ecs"], report, f.now.Add(4*time.Second))
			if err != nil || !ack.Duplicate {
				t.Fatal(ack, err)
			}
			f.complete(t, "swas", reverse, f.now.Add(3*time.Second))
			d, err = f.s.DuplexTrace(ctx, a.DuplexID)
			if err != nil || d.Reverse.Status != "succeeded" || d.Reverse.Outcome.StartedAt.Equal(d.Forward.Outcome.StartedAt) {
				t.Fatal(d, err)
			}
			if _, err = f.s.DB.Exec("UPDATE instance SET name='changed after query'"); err != nil {
				t.Fatal(err)
			}
			if err = f.s.RevokeAgent(ctx, "swas", f.now.Add(6*time.Second)); err != nil {
				t.Fatal(err)
			}
			again, err := f.s.SubmitDuplexTrace(ctx, in, f.now.Add(time.Hour))
			if err != nil || again != a {
				t.Fatal("retry reevaluated old endpoints", again, err)
			}
			d, err = f.s.DuplexTrace(ctx, a.DuplexID)
			if err != nil || d.From.Name == "changed after query" || d.To.Name == "changed after query" {
				t.Fatal("endpoint snapshot changed", d, err)
			}
			data, _ := json.Marshal(d)
			text := model.DuplexTraceText(d)
			for _, private := range []string{f.tokens["ecs"], f.tokens["swas"], forward.Owner, reverse.Owner, "request_key", "token_hash", "input_json"} {
				if strings.Contains(string(data), private) || strings.Contains(text, private) {
					t.Fatal("private task data exposed", private)
				}
			}
			for _, want := range []string{"[forward]", "[reverse]", "port: 8443", "tcp_reset"} {
				if !strings.Contains(text, want) {
					t.Fatal("incomplete paired export", want)
				}
			}
			items, err := f.s.Traces(ctx, model.TraceFilter{From: f.now.Add(-time.Second), To: f.now.Add(time.Minute), Limit: 10})
			if err != nil || len(items) != 2 || items[0].DuplexID == nil || *items[0].DuplexID != a.DuplexID {
				t.Fatal(items, err)
			}
		})
	}
}

func TestDuplexTraceUnavailableEndpointOrFullSecondQueueRollsBackBothLegs(t *testing.T) {
	for _, mode := range []string{"agent_revoked", "queue_full", "same_address", "ambiguous", "stale", "missing_v6"} {
		t.Run(mode, func(t *testing.T) {
			f := newDuplexFixture(t)
			ctx := context.Background()
			in := duplexInput()
			var err error
			switch mode {
			case "agent_revoked":
				err = f.s.RevokeAgent(ctx, "swas", f.now)
			case "queue_full":
				for i := 0; i < 100; i++ {
					_, err = f.s.EnqueueAgentProbe(ctx, "swas", fmt.Sprintf("duplex_queue_%020d", i), probeRequest(), f.now)
					if err != nil {
						break
					}
				}
			case "same_address":
				_, err = f.s.DB.Exec(`UPDATE instance_probe_address SET ipv4_json='["1.1.1.1"]' WHERE instance_id='swas'`)
			case "ambiguous":
				_, err = f.s.DB.Exec(`UPDATE instance_probe_address SET ipv4_json='["1.1.1.1","8.8.8.8"]' WHERE instance_id='swas'`)
			case "stale":
				_, err = f.s.DB.Exec("UPDATE instance_probe_address SET observed_at=? WHERE instance_id='swas'", timestamp(f.now.Add(-11*time.Minute)))
			case "missing_v6":
				in.Family = "ipv6"
				_, err = f.s.DB.Exec("UPDATE instance_probe_address SET ipv6_json=NULL WHERE instance_id='swas'")
			}
			if err != nil {
				t.Fatal(err)
			}
			var before, after int
			if err = f.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.SubmitDuplexTrace(ctx, in, f.now); err == nil {
				t.Fatal("unavailable pair accepted")
			}
			if err = f.s.DB.QueryRow("SELECT count(*) FROM job").Scan(&after); err != nil || after != before {
				t.Fatal("half a pair committed", before, after, err)
			}
			for _, table := range []string{"duplex_trace", "duplex_trace_leg", "network_trace_run"} {
				var count int
				if err = f.s.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("partial pair", table, count, err)
				}
			}
		})
	}
}

func TestDuplexTraceRechecksQueuedAddressesAndKeepsRunningObservation(t *testing.T) {
	for _, mode := range []string{"changed", "incomplete", "stale", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			f := newDuplexFixture(t)
			ctx := context.Background()
			a, err := f.s.SubmitDuplexTrace(ctx, duplexInput(), f.now)
			if err != nil {
				t.Fatal(err)
			}
			forward := f.claim(t, "ecs", strings.Repeat("f", 64), f.now)
			switch mode {
			case "changed":
				_, err = f.s.DB.Exec(`UPDATE instance_probe_address SET ipv4_json='["9.9.9.9"]' WHERE instance_id='ecs'`)
			case "incomplete":
				_, err = f.s.DB.Exec("UPDATE instance_probe_address SET ipv4_json=NULL WHERE instance_id='ecs'")
			case "stale":
				_, err = f.s.DB.Exec("UPDATE instance_probe_address SET observed_at=? WHERE instance_id='ecs'", timestamp(f.now.Add(-11*time.Minute)))
			case "ambiguous":
				_, err = f.s.DB.Exec(`UPDATE instance_probe_address SET ipv4_json='["1.1.1.1","9.9.9.9"]' WHERE instance_id='ecs'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.ExpireNetworkProbes(ctx, f.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			d, err := f.s.DuplexTrace(ctx, a.DuplexID)
			if err != nil || d.Forward.Status != "running" || d.Reverse.Status != "failed" || d.Reverse.Error.Code != "probe_target_changed" || d.Reverse.Result != nil || d.From.PublicAddress != "1.1.1.1" {
				t.Fatal(d, err)
			}
			f.complete(t, "ecs", forward, f.now.Add(time.Second))
			d, err = f.s.DuplexTrace(ctx, a.DuplexID)
			if err != nil || d.Forward.Status != "succeeded" || d.Forward.Request.Host != "8.8.8.8" || d.Reverse.Status != "failed" {
				t.Fatal(d, err)
			}
		})
	}
}

func TestDuplexTraceRevokedOneAgentDoesNotReplayOrPreventOtherDirection(t *testing.T) {
	f := newDuplexFixture(t)
	ctx := context.Background()
	a, err := f.s.SubmitDuplexTrace(ctx, duplexInput(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.RevokeAgent(ctx, "ecs", f.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	reverse := f.claim(t, "swas", strings.Repeat("b", 64), f.now.Add(2*time.Second))
	f.complete(t, "swas", reverse, f.now.Add(2*time.Second))
	d, err := f.s.DuplexTrace(ctx, a.DuplexID)
	if err != nil || d.Forward.Status != "failed" || d.Forward.Error.Code != "probe_binding_changed" || d.Forward.Result != nil || d.Reverse.Status != "succeeded" {
		t.Fatal(d, err)
	}
	if _, err = f.s.PrepareRestoredCopy(ctx, f.now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err = f.s.DuplexTrace(ctx, a.DuplexID)
	if err != nil || d.Reverse.Status != "succeeded" || d.Reverse.Result == nil {
		t.Fatal("restore lost finished direction", d, err)
	}
}

func TestDuplexTraceRestoreRetainsFinishedLegAndCancelsQueuedLeg(t *testing.T) {
	f := newDuplexFixture(t)
	ctx := context.Background()
	in := duplexInput()
	in.Method = "icmp"
	in.ForwardPort, in.ReversePort = 0, 0
	a, err := f.s.SubmitDuplexTrace(ctx, in, f.now)
	if err != nil {
		t.Fatal(err)
	}
	forward := f.claim(t, "ecs", strings.Repeat("f", 64), f.now)
	r := traceReport(forward.ID, forward.Owner, f.agents["ecs"], f.now)
	r.Result.Address, r.Result.ResolvedAddresses = forward.Request.Host, []string{forward.Request.Host}
	r.Result.Hops[0].Samples[0].Address = new(forward.Request.Host)
	r.Result.Hops[0].Samples[0].Reply = new("echo_reply")
	r.Result.Hops[0].Samples[0].ICMPType, r.Result.Hops[0].Samples[0].ICMPCode = new(0), new(0)
	if _, err = f.s.AcceptTraceResult(ctx, f.tokens["ecs"], r, f.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PrepareRestoredCopy(ctx, f.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	d, err := f.s.DuplexTrace(ctx, a.DuplexID)
	if err != nil || d.Forward.Status != "succeeded" || d.Forward.Result == nil || d.Reverse.Status != "failed" || d.Reverse.Error.Code != "backup_restored" || d.Reverse.Result != nil {
		t.Fatal(d, err)
	}
	claim, err := f.s.ClaimAgentTrace(ctx, f.tokens["swas"], agentproto.ProbeClaim{AgentID: f.agents["swas"], Owner: strings.Repeat("b", 64)}, f.now.Add(3*time.Second))
	if err != nil || claim.Task != nil {
		t.Fatal("restored queued direction replayed", claim, err)
	}
	again, err := f.s.SubmitDuplexTrace(ctx, in, f.now.Add(time.Hour))
	if err != nil || again != a {
		t.Fatal("restore lost operation idempotency", again, err)
	}
	rows, err := f.s.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("invalid duplex foreign keys", rows.Err())
	}
}

package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentmeter"
	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
	"github.com/JasperXzy/harbormaster/internal/api"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func setupProbeClient(t *testing.T) (*Client, *store.Store, string, *security.Box) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, q := range []string{"INSERT INTO account VALUES('a','test','china','now')", "INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)", "INSERT INTO instance VALUES('ecs','a','i-test','ecs','test','cn-shanghai','running','stopped','now')", `INSERT INTO resource_snapshot VALUES('a','ecs','ecs','cn-shanghai','{}',1,'now')`} {
		if _, err = s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	grant, _, err := s.AgentGrant(ctx, "ecs", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	registration := agentproto.Registration{Version: 1, Code: grant, Nonce: strings.Repeat("a", 64), Metadata: agentproto.Metadata{InstanceID: "i-test", RegionID: "cn-shanghai"}, AgentVersion: "test"}
	identity, err := s.RegisterAgent(ctx, registration, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	server.Config.Handler = api.NewHandler(s, api.NewProductionPanel(s, config.Config{PublicURL: origin, Timezone: "Asia/Shanghai"}, nil), nil)
	server.StartTLS()
	t.Cleanup(server.Close)
	box, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	dir := filepath.Join(t.TempDir(), "state")
	j, err := agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{HTTP: server.Client(), Journal: j, Version: "test"}
	c.HTTP.Timeout = 10 * time.Second
	t.Cleanup(func() { c.Journal.Close() })
	if err = j.SetIdentity(agentstate.Identity{Server: origin, AgentID: identity.AgentID, Token: agentproto.DeriveToken(grant, registration.Nonce)}); err != nil {
		t.Fatal(err)
	}
	return c, s, dir, box
}
func clientProbeRequest() netprobe.Request {
	return netprobe.Request{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 1, TimeoutMS: 1000, IntervalMS: 100}
}
func successfulProbe() netprobe.Result {
	now := time.Now().UTC()
	return netprobe.Result{StartedAt: now, FinishedAt: now.Add(time.Millisecond), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: now, RTTMS: new(float64(1))}}}
}
func TestProbeLostClaimAndResultResponsesSurviveRestartWithoutReexecution(t *testing.T) {
	c, s, dir, box := setupProbeClient(t)
	ctx := context.Background()
	job, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("k", 32), clientProbeRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	c.probeRun = func(context.Context, netprobe.Request) (netprobe.Result, error) {
		runs++
		if c.Journal.Meta().Probe.Task == nil {
			t.Fatal("no durable start marker")
		}
		return successfulProbe(), nil
	}
	transport := c.HTTP.Transport
	claimLost, resultLost := false, false
	c.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		res, err := transport.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if r.URL.Path == agentproto.ProbeClaimPath && !claimLost {
			claimLost = true
			res.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		if r.URL.Path == agentproto.ProbeResultPath && !resultLost {
			resultLost = true
			res.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return res, nil
	})
	if _, err = c.ProbeOnce(ctx); err != ErrTransport || runs != 0 {
		t.Fatal(runs, err)
	}
	owner := c.Journal.Meta().Probe.Owner
	if owner == "" || c.Journal.Meta().Probe.Task != nil {
		t.Fatal("claim retry state")
	}
	c.Journal.Close()
	c.Journal, err = agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ProbeOnce(ctx); err != ErrTransport || runs != 1 {
		t.Fatal(runs, err)
	}
	if c.Journal.Meta().Probe.Report == nil || c.Journal.Meta().Probe.Owner != owner {
		t.Fatal("result not durable")
	}
	ended, err := s.Job(ctx, job.ID)
	if err != nil || ended.Status != "succeeded" {
		t.Fatal(ended, err)
	}
	c.Journal.Close()
	c.Journal, err = agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := c.ProbeOnce(ctx); err != nil || !worked || runs != 1 || c.Journal.Meta().Probe != nil {
		t.Fatal(worked, runs, err)
	}
	var rows int
	if err = s.DB.QueryRow("SELECT count(*) FROM network_probe_job WHERE result_json IS NOT NULL").Scan(&rows); err != nil || rows != 1 {
		t.Fatal(rows, err)
	}
}
func TestRecoveredStartedProbeReportsInterruptedAndNeverRuns(t *testing.T) {
	c, s, dir, box := setupProbeClient(t)
	ctx := context.Background()
	owner := strings.Repeat("a", 64)
	job, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("k", 32), clientProbeRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Journal.BeginProbe(owner); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimAgentProbe(ctx, c.Journal.Meta().Identity.Token, agentproto.ProbeClaim{AgentID: c.Journal.Meta().Identity.AgentID, Owner: owner}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Journal.StartProbe(owner, *claim.Task); err != nil {
		t.Fatal(err)
	}
	c.Journal.Close()
	c.Journal, err = agentstate.Open(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	c.probeRun = func(context.Context, netprobe.Request) (netprobe.Result, error) {
		t.Fatal("replayed started probe")
		return netprobe.Result{}, nil
	}
	if worked, err := c.ProbeOnce(ctx); err != nil || !worked || c.Journal.Meta().Probe != nil {
		t.Fatal(worked, err)
	}
	ended, err := s.Job(ctx, job.ID)
	if err != nil || ended.Status != "failed" || ended.Error == nil || ended.Error.Code != "probe_interrupted" {
		t.Fatal(ended, err)
	}
}
func TestBlockedProbeDoesNotBlockTrafficAndCancellationPreservesResult(t *testing.T) {
	c, s, _, _ := setupProbeClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("k", 32), clientProbeRequest(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	var runs atomic.Int32
	c.probeRun = func(ctx context.Context, _ netprobe.Request) (netprobe.Result, error) {
		runs.Add(1)
		close(started)
		now := time.Now().UTC()
		<-ctx.Done()
		return netprobe.Result{StartedAt: now, FinishedAt: time.Now().UTC(), Samples: []netprobe.Sample{}, ResolvedAddresses: []string{}}, netprobe.ErrCancelled
	}
	go func() { _, err := c.ProbeOnce(ctx); result <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	if err = c.Sample(agentmeter.Sample{Generation: "kernel", UptimeMS: 10000, IPv4RX: 10}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if sent, err := c.Flush(ctx); err != nil || !sent {
		t.Fatal("traffic blocked by probe", sent, err)
	}
	var samples int
	if err = s.DB.QueryRow("SELECT count(*) FROM traffic_sample").Scan(&samples); err != nil || samples != 1 {
		t.Fatal(samples, err)
	}
	cancel()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe ignored cancellation")
	}
	if c.Journal.Meta().Probe.Report == nil {
		t.Fatal("cancelled result lost")
	}
	if _, err = c.ProbeOnce(context.Background()); err != nil || runs.Load() != 1 {
		t.Fatal(runs.Load(), err)
	}
	ended, err := s.Job(context.Background(), job.ID)
	if err != nil || ended.Error == nil || ended.Error.Code != "probe_cancelled" {
		t.Fatal(ended, err)
	}
}
func TestProbeUntrustedTasksAndUnavailableProtocolNeverExecute(t *testing.T) {
	for _, mode := range []string{"old_server", "private_target", "wrong_owner", "unknown_command", "wrong_id", "result_conflict", "result_expired"} {
		t.Run(mode, func(t *testing.T) {
			c := testClient(t)
			if err := c.Journal.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "test-token-never-real"}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			c.probeRun = func(context.Context, netprobe.Request) (netprobe.Result, error) {
				runs++
				return successfulProbe(), nil
			}
			c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == agentproto.ProbeResultPath {
					if mode == "wrong_id" {
						return response(200, agentproto.ProbeAck{TaskID: "different"}), nil
					}
					code := "probe_result_conflict"
					if mode == "result_expired" {
						code = "probe_expired"
					}
					return response(409, map[string]any{"error": map[string]string{"code": code}}), nil
				}
				if mode == "old_server" {
					return response(404, map[string]string{}), nil
				}
				var in agentproto.ProbeClaim
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Fatal("claim body")
				}
				now := time.Now().UTC()
				task := agentproto.ProbeTask{ID: "job_test", Owner: in.Owner, Request: clientProbeRequest(), ExpiresAt: now.Add(5 * time.Minute)}
				if mode == "private_target" {
					task.Request.Host = "100.100.100.200"
				}
				if mode == "wrong_owner" {
					task.Owner = strings.Repeat("f", 64)
				}
				if mode == "unknown_command" {
					return response(200, map[string]any{"task": task, "server_time": now, "command": "arbitrary"}), nil
				}
				return response(200, agentproto.ProbeClaimed{Task: &task, ServerTime: now}), nil
			})}
			_, err := c.ProbeOnce(context.Background())
			if mode == "old_server" {
				if !errors.Is(err, ErrProbeUnsupported) || runs != 0 {
					t.Fatal(runs, err)
				}
			} else if mode == "result_expired" {
				if err != nil || runs != 1 || c.Journal.Meta().Probe != nil {
					t.Fatal(runs, err)
				}
			} else {
				if err != ErrProtocol {
					t.Fatal(err)
				}
				if mode != "wrong_id" && mode != "result_conflict" && runs != 0 {
					t.Fatal("bad task ran")
				}
				if (mode == "wrong_id" || mode == "result_conflict") && c.Journal.Meta().Probe.Report == nil {
					t.Fatal("unconfirmed result discarded")
				}
			}
		})
	}
}

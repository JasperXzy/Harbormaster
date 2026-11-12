package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

func TestServerTracePointIsolationDeduplicationAndLeases(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	q := traceRequest()
	key := strings.Repeat("k", 32)
	local, err := s.EnqueueServerTrace(ctx, key, q, now)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.EnqueueAgentTrace(ctx, "ecs", strings.Repeat("r", 32), q, now)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.EnqueueServerTrace(ctx, key, q, now)
	if err != nil || repeat.ID != local.ID {
		t.Fatal(repeat, err)
	}
	if _, err = s.EnqueueAgentTrace(ctx, "ecs", key, q, now); !errors.Is(err, ErrConflict) {
		t.Fatal("agent reused local request", err)
	}
	if _, err = s.EnqueueServerTrace(ctx, strings.Repeat("r", 32), q, now); !errors.Is(err, ErrConflict) {
		t.Fatal("local reused agent request", err)
	}
	changed := q
	changed.Port = 80
	if _, err = s.EnqueueServerTrace(ctx, key, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := []ServerTraceClaim{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := s.ClaimServerTrace(ctx, now)
			if err == nil {
				mu.Lock()
				claims = append(claims, claim)
				mu.Unlock()
			} else if !errors.Is(err, ErrNotFound) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(claims) != 1 || claims[0].Job.ID != local.ID {
		t.Fatal(claims)
	}
	claimed, err := s.ClaimAgentTrace(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: strings.Repeat("a", 64)}, now)
	if err != nil || claimed.Task == nil || claimed.Task.ID != remote.ID {
		t.Fatal(claimed, err)
	}
	claim := claims[0]
	if err = s.AuthorizeServerTrace(ctx, claim, now); err != nil {
		t.Fatal(err)
	}
	altered := claim
	altered.Request.Port = 80
	if err = s.AuthorizeServerTrace(ctx, altered, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("request modified", err)
	}
	result := traceReport("", "", "", now).Result
	if err = s.FinishServerTrace(ctx, altered, result, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	if _, err = s.PrepareRestoredCopy(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthorizeServerTrace(ctx, claim, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("restored task authorized", err)
	}
	if err = s.FinishServerTrace(ctx, claim, result, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("restored task finished", err)
	}
}

func TestServerTraceAndProbeShareOnePointLease(t *testing.T) {
	s, _, _, now := historyAgent(t)
	ctx := context.Background()
	if _, err := s.EnqueueServerTrace(ctx, strings.Repeat("t", 32), traceRequest(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueServerProbe(ctx, strings.Repeat("p", 32), probeRequest(), now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, action := range []string{"probe", "trace"} {
		wg.Add(1)
		go func(action string) {
			defer wg.Done()
			_, err := s.claimServerNetwork(ctx, action, now)
			if err != nil && !errors.Is(err, ErrNotFound) {
				t.Error(err)
			}
		}(action)
	}
	wg.Wait()
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM network_probe_job p JOIN job j ON j.id=p.job_id WHERE p.point_kind='server' AND j.status='running'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err := s.ClaimCollectionJob(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("collection claimed network work", err)
	}
}

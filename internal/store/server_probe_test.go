package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestServerProbePointIsolationDeduplicationAndLeases(t *testing.T) {
	s, agent, token, now := historyAgent(t)
	ctx := context.Background()
	q := probeRequest()
	key := strings.Repeat("k", 32)
	local, err := s.EnqueueServerProbe(ctx, key, q, now)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := s.EnqueueAgentProbe(ctx, "ecs", strings.Repeat("r", 32), q, now)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.EnqueueServerProbe(ctx, key, q, now)
	if err != nil || repeat.ID != local.ID {
		t.Fatal(repeat, err)
	}
	if _, err = s.EnqueueAgentProbe(ctx, "ecs", key, q, now); !errors.Is(err, ErrConflict) {
		t.Fatal("agent reused local request", err)
	}
	if _, err = s.EnqueueServerProbe(ctx, strings.Repeat("r", 32), q, now); !errors.Is(err, ErrConflict) {
		t.Fatal("local reused agent request", err)
	}
	changed := q
	changed.Port = 80
	if _, err = s.EnqueueServerProbe(ctx, key, changed, now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claims := []ServerProbeClaim{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := s.ClaimServerProbe(ctx, now)
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
	claimed, err := s.ClaimAgentProbe(ctx, token, agentproto.ProbeClaim{AgentID: agent, Owner: strings.Repeat("a", 64)}, now)
	if err != nil || claimed.Task == nil || claimed.Task.ID != remote.ID {
		t.Fatal(claimed, err)
	}
	claim := claims[0]
	if err = s.AuthorizeServerProbe(ctx, claim, now); err != nil {
		t.Fatal(err)
	}
	altered := claim
	altered.Request.Port = 80
	if err = s.AuthorizeServerProbe(ctx, altered, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("request modified", err)
	}
	result := netprobe.Result{StartedAt: now, FinishedAt: now, Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: now, RTTMS: new(float64(2))}}}
	if err = s.FinishServerProbe(ctx, altered, result, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal(err)
	}
	if _, err = s.PrepareRestoredCopy(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthorizeServerProbe(ctx, claim, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("restored task authorized", err)
	}
	if err = s.FinishServerProbe(ctx, claim, result, "", now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("restored task finished", err)
	}
}

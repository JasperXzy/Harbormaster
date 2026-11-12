package heartbeat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type pingFunc func(context.Context, string) error

func (f pingFunc) Send(ctx context.Context, target string) error { return f(ctx, target) }
func heartbeatFixture(t *testing.T) (*store.Store, *security.Box, model.HeartbeatSettings) {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	sealed, err := box.Encrypt([]byte("https://monitor.example/private-test-token"), model.HeartbeatCredentialOwner)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.SaveHeartbeat(context.Background(), 0, true, 60, &sealed, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return s, box, state
}
func TestRunnerGatesBothPeriodicAndManualPings(t *testing.T) {
	s, box, state := heartbeatFixture(t)
	ctx := context.Background()
	calls := 0
	healthy := false
	runner := Runner{Store: s, Box: box, Healthy: func(context.Context) bool { return healthy }, Sender: pingFunc(func(ctx context.Context, target string) error {
		calls++
		if target != "https://monitor.example/private-test-token" {
			t.Fatal("wrong decrypted destination")
		}
		return nil
	})}
	if worked, err := runner.RunOne(ctx); err != nil || !worked || calls != 0 {
		t.Fatal(worked, err, calls)
	}
	_, current, err := s.HeartbeatSettings(ctx)
	if err != nil || current.LastError == nil || current.LastError.Code != "heartbeat_unhealthy" || current.LastSuccessAt != nil {
		t.Fatal(current, err)
	}
	// A manual test is explicit, but still must not fake success while unhealthy.
	j, err := s.EnqueueHeartbeatTest(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "failed" || calls != 0 {
		t.Fatal(job, err, calls)
	}
	healthy = true
	state, err = s.SaveHeartbeat(ctx, state.Revision, true, 60, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunOne(ctx); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	_, current, err = s.HeartbeatSettings(ctx)
	if err != nil || current.LastSuccessAt == nil || current.LastError != nil {
		t.Fatal(current, err)
	}
	if worked, err := runner.RunOne(ctx); err != nil || worked || calls != 1 {
		t.Fatal("replayed periodic ping", worked, err, calls)
	}
}
func TestRunnerFencesSettingsChangeAndSanitizesSenderErrors(t *testing.T) {
	s, box, state := heartbeatFixture(t)
	ctx := context.Background()
	calls := 0
	runner := Runner{Store: s, Box: box, Sender: pingFunc(func(context.Context, string) error { calls++; return errors.New("URL private-test-token") }), Healthy: func(context.Context) bool {
		_, err := s.SaveHeartbeat(ctx, state.Revision, false, 60, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return true
	}}
	if _, err := runner.RunOne(ctx); !errors.Is(err, store.ErrLeaseLost) || calls != 0 {
		t.Fatal(err, calls)
	}
	_, current, err := s.HeartbeatSettings(ctx)
	if err != nil || current.LastSuccessAt != nil {
		t.Fatal(current, err)
	}
	_, err = s.SaveHeartbeat(ctx, current.Revision, true, 60, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	runner.Healthy = func(context.Context) bool { return true }
	if _, err = runner.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	_, current, err = s.HeartbeatSettings(ctx)
	if err != nil || current.LastError == nil || current.LastError.Code != "heartbeat_failed" || strings.Contains(current.LastError.Message, "private-test-token") {
		t.Fatal(current, err)
	}
}
func TestHealthGateRequiresHTTPReadyAndRecentSchedulerProgress(t *testing.T) {
	var mu sync.Mutex
	status := 200
	body := `{"status":"ready"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Error("wrong local probe")
		}
		mu.Lock()
		code, text := status, body
		mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(text))
	}))
	defer server.Close()
	progress := time.Now()
	gate := NewHealthGate(server.URL+"/readyz", func() time.Time { return progress })
	if !gate.Healthy(context.Background()) {
		t.Fatal("ready rejected")
	}
	progress = time.Now().Add(-SchedulerGrace - time.Second)
	if gate.Healthy(context.Background()) {
		t.Fatal("stalled scheduler accepted")
	}
	progress = time.Now()
	mu.Lock()
	status = 503
	mu.Unlock()
	if gate.Healthy(context.Background()) {
		t.Fatal("database unready accepted")
	}
	mu.Lock()
	status = 200
	body = `{"status":"not_ready"}`
	mu.Unlock()
	if gate.Healthy(context.Background()) {
		t.Fatal("false readiness accepted")
	}
	progress = time.Time{}
	if gate.Healthy(context.Background()) {
		t.Fatal("scheduler has not started")
	}
}

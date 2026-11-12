package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/api"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestRunOnceDoesNotSendOrConsumeHeartbeatTests(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := bytes.Repeat([]byte{8}, 32)
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("HM_MASTER_KEY_FILE", "")
	t.Setenv("HM_DEV", "false")
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := security.NewBox(key)
	cipher, err := box.Encrypt([]byte("https://example.invalid/private-test-token"), model.HeartbeatCredentialOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveHeartbeat(ctx, 0, true, 60, &cipher, time.Now()); err != nil {
		t.Fatal(err)
	}
	j, err := s.EnqueueHeartbeatTest(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var output, logs bytes.Buffer
	if code := Server(ctx, []string{"run-once", "--data-dir", dir}, strings.NewReader(""), &output, &logs); code != 0 {
		t.Fatal(code, logs.String())
	}
	s, err = store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, state, err := s.HeartbeatSettings(ctx)
	if err != nil || state.LastAttemptAt != nil {
		t.Fatal("run-once attempted heartbeat", state, err)
	}
	job, err := s.Job(ctx, j.ID)
	if err != nil || job.Status != "queued" || job.Attempts != 0 {
		t.Fatal("run-once consumed heartbeat job", job, err)
	}
	if strings.Contains(logs.String(), "private-test-token") {
		t.Fatal("secret URL logged")
	}
}

type cliPingFunc func(context.Context, string) error

func (f cliPingFunc) Send(ctx context.Context, target string) error { return f(ctx, target) }
func TestServeHeartbeatWiringRequiresActualHTTPReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := store.Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	cipher, _ := box.Encrypt([]byte("https://example.invalid/private-test-token"), model.HeartbeatCredentialOwner)
	state, err := s.SaveHeartbeat(ctx, 0, true, 60, &cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e := &collect.Engine{Store: s, Box: box, Workers: 1}
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	defer func() { cancel(); <-done }()
	server := httptest.NewUnstartedServer(api.NewHandler(s, nil, nil))
	server.Start()
	defer server.Close()
	runner := newHeartbeatRunner(config.Config{}, s, e, server.Listener, nil)
	if runner == nil {
		t.Fatal("production runner missing")
	}
	if newHeartbeatRunner(config.Config{Dev: true}, s, e, server.Listener, nil) != nil {
		t.Fatal("development started sender")
	}
	calls := 0
	runner.Sender = cliPingFunc(func(context.Context, string) error { calls++; return nil })
	deadline := time.Now().Add(3 * time.Second)
	for e.SchedulerProgress().IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err = runner.RunOne(ctx); err != nil || calls != 1 {
		t.Fatal("healthy serve did not ping", calls, err)
	}
	// A live scheduler alone cannot mask an HTTP panel that stopped serving.
	server.Close()
	if _, err = s.SaveHeartbeat(ctx, state.Revision, true, 60, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunOne(ctx); err != nil || calls != 1 {
		t.Fatal("closed panel still pinged", calls, err)
	}
	_, state, err = s.HeartbeatSettings(ctx)
	if err != nil || state.LastError == nil || state.LastError.Code != "heartbeat_unhealthy" {
		t.Fatal(state, err)
	}
}

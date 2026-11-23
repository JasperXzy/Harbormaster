package agentcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentclient"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
	"github.com/JasperXzy/harbormaster/internal/security"
)

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProbeWorkerEndpointFailureIsIndependentAndStopsOnContext(t *testing.T) {
	box, _ := security.NewBox(bytes.Repeat([]byte{3}, 32))
	j, err := agentstate.Open(filepath.Join(t.TempDir(), "state"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "private-test-token"}); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	client := &agentclient.Client{Journal: j, HTTP: &http.Client{Transport: probeTransport(func(*http.Request) (*http.Response, error) {
		called <- struct{}{}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"not_found"}}`)), Header: http.Header{}}, nil
	})}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- probeDelivery(ctx, client, logger) }()
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not poll")
	}
	select {
	case err = <-done:
		t.Fatal("missing feature stopped worker", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker failed to stop")
	}
	if strings.Contains(logs.String(), "private-test-token") || strings.Contains(logs.String(), "panel.example") {
		t.Fatal("probe logs leaked private data")
	}
}
func TestProbeWorkerReportsDurabilityFailure(t *testing.T) {
	box, _ := security.NewBox(bytes.Repeat([]byte{3}, 32))
	j, err := agentstate.Open(filepath.Join(t.TempDir(), "state"), box)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "private-test-token"}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	err = probeDelivery(context.Background(), &agentclient.Client{Journal: j}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if !errors.Is(err, agentstate.ErrState) {
		t.Fatal(err)
	}
}

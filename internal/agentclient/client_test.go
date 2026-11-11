package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentmeter"
	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
	"github.com/JasperXzy/harbormaster/internal/security"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{}}
}
func testClient(t *testing.T) *Client {
	t.Helper()
	box, _ := security.NewBox(bytes.Repeat([]byte{9}, 32))
	j, err := agentstate.Open(filepath.Join(t.TempDir(), "state"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return &Client{Journal: j, Version: "test"}
}
func TestRegistrationLostResponseKeepsNonceAndEncryptedToken(t *testing.T) {
	c := testClient(t)
	code := strings.Repeat("a", 64)
	if err := c.PrepareRegistration("https://panel.example", code, agentproto.Metadata{InstanceID: "i-test", RegionID: "cn-shanghai"}); err != nil {
		t.Fatal(err)
	}
	nonce := c.Journal.Meta().Identity.Registration.Nonce
	attempt := 0
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		attempt++
		var reg agentproto.Registration
		if json.NewDecoder(r.Body).Decode(&reg) != nil || reg.Nonce != nonce || reg.Code != code {
			t.Fatal("retry changed identity")
		}
		if attempt == 1 {
			return nil, io.ErrUnexpectedEOF
		}
		return response(201, agentproto.Registered{AgentID: "agent-test", TokenScheme: "hmac-sha256-v1", ReportIntervalSeconds: 10}), nil
	})}
	if err := c.Register(context.Background()); err != ErrTransport {
		t.Fatal(err)
	}
	if c.Journal.Meta().Identity.Registration == nil {
		t.Fatal("lost response discarded registration")
	}
	if err := c.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity := c.Journal.Meta().Identity
	if identity.Registration != nil || identity.Token != agentproto.DeriveToken(code, nonce) {
		t.Fatal("token derivation or secret cleanup failed")
	}
}
func TestCounterGenerationResetsAndAckMustMatchEpoch(t *testing.T) {
	c := testClient(t)
	if err := c.Journal.SetIdentity(agentstate.Identity{Server: "https://panel.example", AgentID: "agent-test", Token: "test-token-not-real"}); err != nil {
		t.Fatal(err)
	}
	s := agentmeter.Sample{Generation: "kernel-one", UptimeMS: 1000, IPv4RX: 10, IPv4TX: 20}
	if err := c.Sample(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	first := *c.Journal.Meta().Last
	s.UptimeMS = 11000
	s.IPv4RX = 100
	if err := c.Sample(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	if last := c.Journal.Meta().Last; last.CounterEpoch != first.CounterEpoch || last.Sequence != 2 {
		t.Fatal("process continuity lost")
	}
	// Family reset must reset epoch even when total counter increased.
	s.UptimeMS = 21000
	s.IPv4RX = 1
	s.IPv6RX = 1000
	if err := c.Sample(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	last := c.Journal.Meta().Last
	if last.CounterEpoch == first.CounterEpoch || last.Sequence != 1 {
		t.Fatal("family reset unnoticed")
	}
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return response(200, agentproto.Ack{CounterEpoch: first.CounterEpoch, AcceptedSequence: 1}), nil
	})}
	if _, err := c.Flush(context.Background()); err != ErrProtocol || len(c.Journal.Batch(100)) != 3 {
		t.Fatal("wrong ACK deleted data", err)
	}
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-token-not-real" {
			t.Fatal("missing auth")
		}
		return response(200, agentproto.Ack{CounterEpoch: last.CounterEpoch, AcceptedSequence: last.Sequence}), nil
	})}
	if sent, err := c.Flush(context.Background()); err != nil || !sent || len(c.Journal.Batch(100)) != 0 {
		t.Fatal(sent, err)
	}
}
func TestMetadataOnlyUsesFixedIMDSv2IdentityPaths(t *testing.T) {
	paths := []string{"/latest/api/token", "/latest/meta-data/instance-id", "/latest/meta-data/region-id"}
	values := []string{"metadata-test-token", "i-test", "cn-shanghai"}
	calls := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "100.100.100.200" || r.URL.Path != paths[calls] || (calls > 0 && r.Header.Get("X-aliyun-ecs-metadata-token") != values[0]) {
			t.Fatal("unexpected metadata access")
		}
		if calls == 0 && (r.Method != "PUT" || r.Header.Get("X-aliyun-ecs-metadata-token-ttl-seconds") != "60") {
			t.Fatal("v2 token missing")
		}
		body := values[calls]
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	meta, err := Metadata(context.Background(), client)
	if err != nil || meta.InstanceID != "i-test" || calls != 3 {
		t.Fatal(meta, err)
	}
	for _, raw := range []string{"http://panel.example", "https://u:p@panel.example", "https://panel.example/path", "https://panel.example?token=test", "https://panel.example/#frag"} {
		if _, err := Origin(raw); err == nil {
			t.Fatal("unsafe origin", raw)
		}
	}
}

package agentclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentmeter"
	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
)

var ErrTransport = errors.New("agent_server_unavailable")
var ErrProtocol = errors.New("agent_server_protocol_error")
var ErrCredentials = errors.New("agent_registration_or_token_rejected")
var ErrMetadata = errors.New("aliyun_metadata_unavailable")

type Client struct {
	ReportIntervalSeconds int
	HTTP                  *http.Client
	Journal               *agentstate.Journal
	Version               string
}

func NewHTTP() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return "", ErrProtocol
	}
	return strings.TrimSuffix(raw, "/"), nil
}
func (c *Client) post(ctx context.Context, path, token string, body, out any) error {
	origin, err := Origin(c.Journal.Meta().Identity.Server)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ErrProtocol
	}
	defer clear(raw)
	req, err := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(raw))
	if err != nil {
		return ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return ErrTransport
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrCredentials
	}
	if res.StatusCode == 429 || res.StatusCode >= 500 {
		return ErrTransport
	}
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return ErrProtocol
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrProtocol
	}
	return nil
}

// PrepareRegistration must commit the encrypted code and nonce before any
// request. Re-running Register after a lost response reuses exactly that nonce.
func (c *Client) PrepareRegistration(server, code string, metadata agentproto.Metadata) error {
	server, err := Origin(server)
	if err != nil {
		return err
	}
	if len(c.Journal.Batch(1)) > 0 {
		return errors.New("agent_backlog_requires_delivery_before_registration")
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return ErrProtocol
	}
	r := agentproto.Registration{Version: 1, Code: code, Nonce: hex.EncodeToString(nonce[:]), Metadata: metadata, AgentVersion: c.Version}
	if r.Validate() != nil {
		return ErrProtocol
	}
	// Replacing an existing identity is explicit local registration, never a
	// directive from a server response. The previous binding stays in journal.
	previous := c.Journal.Meta().Identity
	return c.Journal.SetIdentity(agentstate.Identity{Server: server, AgentID: previous.AgentID, Registration: &r})
}
func (c *Client) Register(ctx context.Context) error {
	identity := c.Journal.Meta().Identity
	if identity.Registration == nil {
		if identity.AgentID != "" && identity.Token != "" {
			return nil
		}
		return ErrCredentials
	}
	var result agentproto.Registered
	if err := c.post(ctx, agentproto.RegisterPath, "", identity.Registration, &result); err != nil {
		return err
	}
	if result.AgentID == "" || len(result.AgentID) > 128 || result.TokenScheme != "hmac-sha256-v1" || result.ReportIntervalSeconds != 10 {
		return ErrProtocol
	}
	identity.AgentID = result.AgentID
	identity.Token = agentproto.DeriveToken(identity.Registration.Code, identity.Registration.Nonce)
	identity.Registration = nil
	return c.Journal.SetIdentity(identity)
}

// Sample is called at the configured cadence, independent of network I/O.
func (c *Client) Sample(sample agentmeter.Sample, at time.Time) error {
	state := c.Journal.Meta()
	if state.Identity.AgentID == "" || state.Identity.Token == "" {
		return ErrCredentials
	}
	epoch := ""
	sequence := int64(1)
	last := state.Last
	familiesMonotonic := true
	if last != nil && last.FamilyCounters != nil {
		f := last.FamilyCounters
		familiesMonotonic = sample.IPv4RX >= f.IPv4RX && sample.IPv4TX >= f.IPv4TX && sample.IPv6RX >= f.IPv6RX && sample.IPv6TX >= f.IPv6TX
	}
	if last != nil && last.AgentID == state.Identity.AgentID && familiesMonotonic && state.Generation == sample.Generation && last.RulesVersion == agentmeter.RulesVersion && sample.UptimeMS > last.UptimeMS && sample.IPv4RX+sample.IPv6RX >= last.RXBytes && sample.IPv4TX+sample.IPv6TX >= last.TXBytes {
		epoch = last.CounterEpoch
		sequence = last.Sequence + 1
	} else {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return ErrProtocol
		}
		epoch = hex.EncodeToString(raw[:])
	}
	r := agentproto.Report{Version: 1, AgentID: state.Identity.AgentID, CounterEpoch: epoch, Sequence: sequence, SampledAt: at.UTC(), UptimeMS: sample.UptimeMS, RXBytes: sample.IPv4RX + sample.IPv6RX, TXBytes: sample.IPv4TX + sample.IPv6TX, AgentVersion: c.Version, RulesVersion: agentmeter.RulesVersion, FamilyCounters: &agentproto.FamilyCounters{IPv4RX: sample.IPv4RX, IPv4TX: sample.IPv4TX, IPv6RX: sample.IPv6RX, IPv6TX: sample.IPv6TX}}
	if c.ReportIntervalSeconds != agentproto.DefaultReportIntervalSeconds {
		r.ReportIntervalSeconds = c.ReportIntervalSeconds
	}
	if r.Validate(at) != nil {
		return ErrProtocol
	}
	return c.Journal.Append(sample.Generation, r)
}
func (c *Client) Flush(ctx context.Context) (bool, error) {
	identity := c.Journal.Meta().Identity
	batch := c.Journal.Batch(agentproto.MaxBatch)
	if len(batch) == 0 {
		return false, nil
	}
	var ack agentproto.Ack
	if err := c.post(ctx, agentproto.BatchPath, identity.Token, agentproto.Batch{Reports: batch}, &ack); err != nil {
		return false, err
	}
	last := batch[len(batch)-1]
	if ack.CounterEpoch != last.CounterEpoch || ack.AcceptedSequence != last.Sequence {
		return false, ErrProtocol
	}
	if err := c.Journal.Ack(ack); err != nil {
		return false, err
	}
	return true, nil
}

// Metadata reads only fixed instance identity endpoints, never RAM credentials,
// user-data or arbitrary URLs. IMDSv2 is required; failure does not weaken it.
func Metadata(ctx context.Context, client *http.Client) (agentproto.Metadata, error) {
	const base = "http://100.100.100.200/latest/"
	read := func(method, path, token string) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, method, base+path, nil)
		if method == "PUT" {
			req.Header.Set("X-aliyun-ecs-metadata-token-ttl-seconds", "60")
		} else {
			req.Header.Set("X-aliyun-ecs-metadata-token", token)
		}
		res, err := client.Do(req)
		if err != nil {
			return "", ErrMetadata
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return "", ErrMetadata
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 4097))
		if err != nil || len(raw) > 4096 {
			return "", ErrMetadata
		}
		return strings.TrimSpace(string(raw)), nil
	}
	token, err := read("PUT", "api/token", "")
	if err != nil || token == "" {
		return agentproto.Metadata{}, ErrMetadata
	}
	id, err := read("GET", "meta-data/instance-id", token)
	if err != nil {
		return agentproto.Metadata{}, err
	}
	region, err := read("GET", "meta-data/region-id", token)
	if err != nil {
		return agentproto.Metadata{}, err
	}
	return agentproto.Metadata{InstanceID: id, RegionID: region}, nil
}

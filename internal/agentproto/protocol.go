// Package agentproto defines the private, versioned agent protocol, independent
// of the frontend OpenAPI contract.
package agentproto

import (
	"errors"
	"time"
)

const Path = "/agent/v1/reports"

// This literal grants access only to fabricated loopback development data.
const DemoToken = "hm-demo-only-not-a-real-credential"
const DemoAgentID = "demo-agent-sin"

type Report struct {
	Version      int       `json:"version"`
	AgentID      string    `json:"agent_id"`
	CounterEpoch string    `json:"counter_epoch"`
	Sequence     int64     `json:"sequence"`
	SampledAt    time.Time `json:"sampled_at"`
	UptimeMS     int64     `json:"uptime_ms"`
	RXBytes      int64     `json:"rx_bytes"`
	TXBytes      int64     `json:"tx_bytes"`
	AgentVersion string    `json:"agent_version"`
}

type Ack struct {
	AcceptedSequence int64     `json:"accepted_sequence"`
	ServerTime       time.Time `json:"server_time"`
	Duplicate        bool      `json:"duplicate"`
}

func (r Report) Validate(now time.Time) error {
	if r.Version != 1 || r.AgentID == "" || len(r.AgentID) > 128 || r.CounterEpoch == "" || len(r.CounterEpoch) > 128 || len(r.AgentVersion) > 64 {
		return errors.New("invalid_report")
	}
	if r.Sequence < 1 || r.UptimeMS < 0 || r.RXBytes < 0 || r.TXBytes < 0 {
		return errors.New("invalid_counter")
	}
	if r.SampledAt.IsZero() || r.SampledAt.After(now.Add(2*time.Minute)) {
		return errors.New("invalid_sample_time")
	}
	return nil
}

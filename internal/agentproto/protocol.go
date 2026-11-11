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

const DefaultReportIntervalSeconds = 10
const MaxReportIntervalSeconds = 60

// RateWindow allows scheduling jitter without presenting long-gap averages as live rates.
func RateWindow(seconds int) time.Duration {
	if seconds == 0 {
		seconds = DefaultReportIntervalSeconds
	}
	return time.Duration(max(30, 3*seconds)) * time.Second
}

type Report struct {
	ReportIntervalSeconds int             `json:"report_interval_seconds,omitempty"`
	Version               int             `json:"version"`
	AgentID               string          `json:"agent_id"`
	CounterEpoch          string          `json:"counter_epoch"`
	Sequence              int64           `json:"sequence"`
	SampledAt             time.Time       `json:"sampled_at"`
	UptimeMS              int64           `json:"uptime_ms"`
	RXBytes               int64           `json:"rx_bytes"`
	TXBytes               int64           `json:"tx_bytes"`
	AgentVersion          string          `json:"agent_version"`
	RulesVersion          string          `json:"rules_version,omitempty"`
	DroppedSamples        int64           `json:"dropped_samples,omitempty"`
	FamilyCounters        *FamilyCounters `json:"family_counters,omitempty"`
}

type FamilyCounters struct {
	IPv4RX int64 `json:"ipv4_rx_bytes"`
	IPv4TX int64 `json:"ipv4_tx_bytes"`
	IPv6RX int64 `json:"ipv6_rx_bytes"`
	IPv6TX int64 `json:"ipv6_tx_bytes"`
}

type Ack struct {
	AcceptedSequence int64     `json:"accepted_sequence"`
	CounterEpoch     string    `json:"counter_epoch"`
	ServerTime       time.Time `json:"server_time"`
	Duplicate        bool      `json:"duplicate"`
}

// IntervalSeconds normalizes legacy reports that omit the optional cadence.
func (r Report) IntervalSeconds() int {
	if r.ReportIntervalSeconds == 0 {
		return DefaultReportIntervalSeconds
	}
	return r.ReportIntervalSeconds
}

func (r Report) Validate(now time.Time) error {
	if r.IntervalSeconds() < 1 || r.IntervalSeconds() > MaxReportIntervalSeconds {
		return errors.New("invalid_report_interval")
	}
	if r.Version != 1 || r.AgentID == "" || len(r.AgentID) > 128 || r.CounterEpoch == "" || len(r.CounterEpoch) > 128 || len(r.AgentVersion) > 64 {
		return errors.New("invalid_report")
	}
	if r.Sequence < 1 || r.UptimeMS < 0 || r.RXBytes < 0 || r.TXBytes < 0 || r.DroppedSamples < 0 || len(r.RulesVersion) > 64 {
		return errors.New("invalid_counter")
	}
	if c := r.FamilyCounters; c != nil {
		if c.IPv4RX < 0 || c.IPv4TX < 0 || c.IPv6RX < 0 || c.IPv6TX < 0 || c.IPv4RX > r.RXBytes || c.IPv4TX > r.TXBytes || c.IPv6RX != r.RXBytes-c.IPv4RX || c.IPv6TX != r.TXBytes-c.IPv4TX {
			return errors.New("invalid_counter")
		}
	}
	if r.SampledAt.IsZero() || r.SampledAt.After(now.Add(2*time.Minute)) {
		return errors.New("invalid_sample_time")
	}
	return nil
}

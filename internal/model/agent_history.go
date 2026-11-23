package model

import "time"

type AgentNetworkInterval struct {
	PreviousSequence int64     `json:"previous_sequence"`
	StartAt          time.Time `json:"start_at"`
	EndAt            time.Time `json:"end_at"`
	DurationMS       int64     `json:"duration_ms"`
	RXBytes          int64     `json:"rx_bytes"`
	TXBytes          int64     `json:"tx_bytes"`
	RXRate           int64     `json:"rx_bytes_per_second"`
	TXRate           int64     `json:"tx_bytes_per_second"`
}
type AgentNetworkSample struct {
	CounterEpoch              string                `json:"counter_epoch"`
	Sequence                  int64                 `json:"sequence"`
	SampledAt                 time.Time             `json:"sampled_at"`
	ReceivedAt                time.Time             `json:"received_at"`
	UptimeMS                  int64                 `json:"uptime_ms"`
	RXBytes                   int64                 `json:"rx_bytes"`
	TXBytes                   int64                 `json:"tx_bytes"`
	Quality                   string                `json:"quality"`
	RulesVersion              *string               `json:"rules_version"`
	ReportIntervalSeconds     *int                  `json:"report_interval_seconds"`
	DroppedSamples            *int64                `json:"dropped_samples"`
	IntervalStatus            string                `json:"interval_status"`
	IntervalUnavailableReason *string               `json:"interval_unavailable_reason"`
	Interval                  *AgentNetworkInterval `json:"interval"`
}
type AgentNetworkHistoryFilter struct {
	InstanceID     string
	From, To       time.Time
	BeforeTime     time.Time
	BeforeEpoch    string
	BeforeSequence int64
}

func (f AgentNetworkHistoryFilter) Valid(now time.Time) bool {
	if f.InstanceID == "" || len(f.InstanceID) > 256 || f.From.IsZero() || !f.To.After(f.From) || f.To.Sub(f.From) > 7*24*time.Hour || f.To.After(now) {
		return false
	}
	if f.BeforeTime.IsZero() {
		return f.BeforeSequence == 0 && f.BeforeEpoch == ""
	}
	return f.BeforeSequence > 0 && len(f.BeforeEpoch) > 0 && len(f.BeforeEpoch) <= 128 && !f.BeforeTime.Before(f.From) && f.BeforeTime.Before(f.To)
}

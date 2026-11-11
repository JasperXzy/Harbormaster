package model

import "time"

type AgentStatus struct {
	Status       string     `json:"status"`
	Version      *string    `json:"version"`
	RegisteredAt *time.Time `json:"registered_at"`
	LastReportAt *time.Time `json:"last_report_at"`
	OfflineSince *time.Time `json:"offline_since"`
	WorstCase    any        `json:"worst_case"`
	Pending      bool       `json:"pending_first_report"`
	Quality      *string    `json:"data_quality"`
}
type InstanceRealtime struct {
	RX        int64     `json:"rx_bytes_per_second"`
	TX        int64     `json:"tx_bytes_per_second"`
	SampledAt time.Time `json:"sampled_at"`
}
type AgentView struct {
	Agent    AgentStatus       `json:"agent"`
	Realtime *InstanceRealtime `json:"realtime"`
}

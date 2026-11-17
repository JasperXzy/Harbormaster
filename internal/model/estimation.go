package model

import "time"

type EstimationSettings struct {
	Revision     int64    `json:"revision"`
	LagSeconds   int      `json:"lag_window_seconds"`
	BandwidthIDs []string `json:"bandwidth_mode_instance_ids"`
}
type TrafficEstimation struct {
	Mode           string     `json:"mode"`
	Status         string     `json:"status"`
	BasisID        string     `json:"basis_id"`
	From           *time.Time `json:"from_at"`
	To             time.Time  `json:"to_at"`
	CalculatedAt   time.Time  `json:"calculated_at"`
	LagSeconds     int        `json:"lag_window_seconds"`
	Increment      *int64     `json:"increment_bytes"`
	KnownIncrement int64      `json:"known_increment_bytes"`
	WorstCase      *int64     `json:"worst_case_bytes"`
	Estimated      *int64     `json:"estimated_bytes"`
	CurrentRate    *int64     `json:"current_rate_bytes_per_second"`
	Qualified      bool       `json:"qualified_for_execution"`
	Reasons        []string   `json:"reasons"`
}

package model

import "time"

type CDTReconciliation struct {
	ID            int64      `json:"id"`
	AccountID     string     `json:"account_id"`
	Scope         string     `json:"scope"`
	ObservationID string     `json:"observation_id"`
	PreviousID    *string    `json:"previous_observation_id"`
	CoveredUntil  *time.Time `json:"covered_until"`
	CheckedAt     time.Time  `json:"checked_at"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason"`
	Predicted     *int64     `json:"predicted_bytes"`
	Official      *int64     `json:"official_bytes"`
	RelativeError *float64   `json:"relative_error"`
	Exceeds       *bool      `json:"exceeds_ten_percent"`
	BadStreak     int        `json:"bad_streak"`
	HealthyStreak int        `json:"healthy_streak"`
	AlertActive   bool       `json:"alert_active"`
}

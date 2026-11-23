package model

import "time"

const HeartbeatCredentialOwner = "system/heartbeat-url/v1"

type HeartbeatSettings struct {
	Enabled         bool       `json:"enabled"`
	URLConfigured   bool       `json:"url_configured"`
	IntervalSeconds int        `json:"interval_seconds"`
	Revision        int64      `json:"revision"`
	UpdatedAt       *time.Time `json:"updated_at"`
	LastAttemptAt   *time.Time `json:"last_attempt_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	LastError       *APIError  `json:"last_error"`
}
type HeartbeatClaim struct {
	Settings   HeartbeatSettings
	Ciphertext []byte
	Owner      string
	Job        *Job
	ExpiresAt  time.Time
}

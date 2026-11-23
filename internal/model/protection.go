package model

import "time"

type ProtectionRuleInput struct {
	Revision                 int64    `json:"revision"`
	Name                     string   `json:"name"`
	Enabled                  bool     `json:"enabled"`
	AccountID                string   `json:"account_id"`
	Scope                    string   `json:"scope"`
	SourceInstanceID         string   `json:"source_instance_id"`
	ThresholdKind            string   `json:"threshold_kind"`
	Threshold                int64    `json:"threshold"`
	Action                   string   `json:"action"`
	Targets                  []string `json:"target_instance_ids"`
	StopMode                 string   `json:"stop_mode"`
	ReserveSeconds           int      `json:"reserve_seconds"`
	DryRun                   bool     `json:"dry_run"`
	AllowUnverifiedExecution bool     `json:"allow_unverified_execution"`
	ConfirmedSelfID          string   `json:"confirmed_self_instance_id"`
}
type ProtectionRule struct {
	ProtectionRuleInput
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Host identity and execution gates are trusted process configuration, never
// fields restored from a database or accepted from a browser request.
type ProtectionPolicy struct {
	PowerEnabled   bool
	SelfKnown      bool
	SelfInstanceID string
	Timezone       string
}
type QuotaSimulation struct {
	Revision         int64     `json:"revision"`
	AccountID        string    `json:"account_id"`
	Scope            string    `json:"scope"`
	SourceInstanceID string    `json:"source_instance_id"`
	Usage            int64     `json:"usage_bytes"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type ProtectionTarget struct {
	InstanceID string  `json:"instance_id"`
	Outcome    string  `json:"outcome"`
	JobID      *string `json:"job_id"`
}
type QuotaDecision struct {
	ID           int64              `json:"id"`
	RuleID       string             `json:"rule_id"`
	RuleRevision int64              `json:"rule_revision"`
	AccountID    string             `json:"account_id"`
	Scope        string             `json:"scope"`
	EvaluatedAt  time.Time          `json:"evaluated_at"`
	State        string             `json:"state"`
	Simulation   bool               `json:"simulation"`
	Threshold    *int64             `json:"threshold_bytes"`
	Value        *int64             `json:"evaluated_bytes"`
	Reasons      []string           `json:"reasons"`
	Targets      []ProtectionTarget `json:"targets"`
}

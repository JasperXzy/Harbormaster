package model

import (
	"context"
	"time"
)

type AuditActor struct {
	Kind string  `json:"kind"`
	ID   *string `json:"id"`
}

func ValidAuditActor(kind string) bool {
	return kind == "admin" || kind == "api_key" || kind == "cli" || kind == "telegram" || kind == "system"
}

// AuditActorForContext uses trusted entry-point context, never request fields.
func AuditActorForContext(ctx context.Context) AuditActor {
	if id := APIKeyPrincipal(ctx); id != "" {
		return AuditActor{Kind: "api_key", ID: &id}
	}
	switch JobTrigger(ctx) {
	case "panel":
		return AuditActor{Kind: "admin"}
	case "cli":
		return AuditActor{Kind: "cli"}
	case "telegram":
		return AuditActor{Kind: "telegram"}
	default:
		return AuditActor{Kind: "system"}
	}
}

// Deliberately typed: arbitrary configuration, credentials and provider error
// bodies cannot be passed as an audit payload. Before/after policy is pending.
type AuditDetails struct {
	ChangedFields      []string `json:"changed_fields"`
	CredentialsChanged bool     `json:"credentials_changed"`
	Revision           *int64   `json:"revision,omitempty"`
	Operation          string   `json:"operation,omitempty"`
	StopMode           string   `json:"stop_mode,omitempty"`
	Status             string   `json:"status,omitempty"`
	DispatchOutcome    string   `json:"dispatch_outcome,omitempty"`
	ErrorCode          string   `json:"error_code,omitempty"`
	ConfirmationBasis  string   `json:"confirmation_basis,omitempty"`
}
type AuditEntry struct {
	ID         string       `json:"id"`
	CreatedAt  time.Time    `json:"created_at"`
	Actor      AuditActor   `json:"actor"`
	Action     string       `json:"action"`
	TargetType string       `json:"target_type"`
	TargetID   string       `json:"target_id"`
	AccountID  *string      `json:"account_id"`
	JobID      *string      `json:"job_id"`
	Summary    string       `json:"summary"`
	Details    AuditDetails `json:"details"`
}
type AuditFilter struct {
	Actor, Action, TargetType, TargetID, AccountID string
	From, To                                       *time.Time
	Before                                         int64
	Limit                                          int
}

func (f AuditFilter) Validate() bool {
	if f.Actor != "" && !ValidAuditActor(f.Actor) || len(f.Action) > 80 || len(f.TargetType) > 40 || len(f.TargetID) > 128 || len(f.AccountID) > 128 || f.Before < 0 || f.Limit < 1 || f.Limit > 201 {
		return false
	}
	if (f.From == nil) != (f.To == nil) {
		return false
	}
	return f.From == nil || f.From.Before(*f.To) && f.To.Sub(*f.From) <= 31*24*time.Hour
}

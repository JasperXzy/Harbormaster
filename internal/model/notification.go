package model

import "time"

const TelegramCredentialOwner = "notify/telegram/credentials/v1"

type Notification struct {
	ResourceID        *string    `json:"resource_id"`
	ProbePlanID       *string    `json:"probe_plan_id"`
	Urgent            bool       `json:"urgent"`
	SuppressionReason *string    `json:"suppression_reason"`
	SuppressedAt      *time.Time `json:"suppressed_at"`
	ID                string     `json:"id"`
	CreatedAt         time.Time  `json:"created_at"`
	Kind              string     `json:"kind"`
	Severity          string     `json:"severity"`
	RuleKey           *string    `json:"rule_key"`
	Title             string     `json:"title"`
	Body              string     `json:"body"`
	AccountID         *string    `json:"account_id"`
	InstanceID        *string    `json:"instance_id"`
	DeliveryStatus    string     `json:"delivery_status"`
	// Lease, event, job and provider identifiers remain private. Safe delivery
	// diagnostics below are included in the panel history contract.
	Sequence        int64      `json:"-"`
	EventKey        string     `json:"-"`
	JobID           string     `json:"-"`
	ChannelRevision int64      `json:"-"`
	Attempts        int        `json:"attempts"`
	Owner           string     `json:"-"`
	LastError       *APIError  `json:"last_error"`
	Uncertain       bool       `json:"has_uncertain_attempt"`
	DeliveredAt     *time.Time `json:"delivered_at"`
	MessageID       *int64     `json:"-"`
}
type TelegramChannelState struct {
	Revision      int64
	UpdatedAt     time.Time
	LastSuccessAt *time.Time
	LastError     *APIError
}

// TelegramChannel is the only channel representation returned to browsers.
// Bot tokens and proxy usernames/passwords never appear in this DTO.
type TelegramChannel struct {
	BotTokenSet   bool       `json:"bot_token_set"`
	ChatID        string     `json:"chat_id"`
	ProxyMode     string     `json:"proxy_mode"`
	ProxyURL      string     `json:"proxy_url"`
	ProxyAuthSet  bool       `json:"proxy_auth_set"`
	Status        string     `json:"status"`
	LastError     *APIError  `json:"last_error"`
	LastSuccessAt *time.Time `json:"last_success_at"`
}

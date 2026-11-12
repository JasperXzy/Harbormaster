package model

import "time"

// CloudSystemEvent preserves provider semantics. A provider's Executed status
// describes its event lifecycle, not proof that an instance was reclaimed.
type CloudSystemEvent struct {
	CloudEventID       string     `json:"cloud_event_id"`
	CloudInstanceID    string     `json:"cloud_instance_id"`
	Category           string     `json:"category"`
	EventType          string     `json:"event_type"`
	EventTypeCode      *int64     `json:"event_type_code"`
	ProviderStatus     string     `json:"provider_status"`
	ProviderStatusCode *int64     `json:"provider_status_code"`
	PublishedAt        *time.Time `json:"published_at"`
	NotBefore          *time.Time `json:"not_before"`
	FinishedAt         *time.Time `json:"finished_at"`
}

type SystemEventObservation struct {
	AccountID string             `json:"account_id"`
	RegionID  string             `json:"region_id"`
	Source    string             `json:"source"`
	FetchedAt time.Time          `json:"fetched_at"`
	Events    []CloudSystemEvent `json:"events"`
	Error     *APIError          `json:"error"`
}

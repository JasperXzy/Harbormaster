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

// SystemEvent is a stable local record of a provider event, with discovery time
// separate from the latest observation and from the provider's own timestamps.
type SystemEvent struct {
	CloudSystemEvent
	ID               string    `json:"id"`
	AccountID        string    `json:"account_id"`
	RegionID         string    `json:"region_id"`
	Source           string    `json:"source"`
	InstanceID       *string   `json:"instance_id"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	ObservedAt       time.Time `json:"observed_at"`
	SeenInLatestScan bool      `json:"seen_in_latest_scan"`
	Sequence         int64     `json:"-"`
}
type SystemEventSource struct {
	AccountID     string     `json:"account_id"`
	RegionID      string     `json:"region_id"`
	Source        string     `json:"source"`
	Status        string     `json:"status"`
	CheckedAt     time.Time  `json:"checked_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     *APIError  `json:"last_error"`
}
type SystemEventFilter struct{ AccountID, InstanceID, RegionID, Source, Category string }

func (f SystemEventFilter) Validate() bool {
	if f.Source != "" && f.Source != "ecs_history" && f.Source != "cms_spot" {
		return false
	}
	if f.Category != "" && f.Category != "maintenance" && f.Category != "spot_interruption" && f.Category != "other" {
		return false
	}
	return f.RegionID == "" || len(f.RegionID) <= 64 && RegionPattern.MatchString(f.RegionID)
}

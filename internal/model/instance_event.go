package model

import "time"

type InstanceEvent struct {
	ID              string         `json:"id"`
	AccountID       string         `json:"account_id"`
	InstanceID      string         `json:"instance_id"`
	CloudInstanceID string         `json:"cloud_instance_id"`
	InstanceKind    string         `json:"instance_kind"`
	RegionID        string         `json:"region_id"`
	InstanceName    string         `json:"instance_name"`
	Kind            string         `json:"kind"`
	ObservedAt      time.Time      `json:"observed_at"`
	Before          map[string]any `json:"before"`
	After           map[string]any `json:"after"`
	Sequence        int64          `json:"-"`
}

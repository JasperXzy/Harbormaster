package model

import (
	"regexp"
	"time"
)

var powerEventKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func ValidPowerEventKey(key string) bool { return powerEventKeyPattern.MatchString(key) }

// PowerJobResult records request intent separately from observed cloud state.
type PowerJobResult struct {
	Action            string            `json:"action"`
	StopMode          *string           `json:"stop_mode"`
	DispatchOutcome   string            `json:"dispatch_outcome"`
	Baseline          *PowerObservation `json:"baseline"`
	Observation       *PowerObservation `json:"observation"`
	ConfirmationBasis *string           `json:"confirmation_basis"`
}

func ValidPowerAction(kind, action, stopMode string) bool {
	if kind != "ecs" && kind != "swas" {
		return false
	}
	if action != "start" && action != "stop" && action != "reboot" {
		return false
	}
	if kind == "ecs" && action == "stop" {
		return stopMode == "keep_charging" || stopMode == "stop_charging"
	}
	return stopMode == ""
}

// PowerObservation contains a fresh targeted cloud query, not an inventory cache.
type PowerObservation struct {
	Status      string     `json:"status"`
	StoppedMode *string    `json:"stopped_mode"`
	StartedAt   *time.Time `json:"started_at"`
	ObservedAt  time.Time  `json:"observed_at"`
}

func (o PowerObservation) Valid() bool {
	switch o.Status {
	case "pending", "starting", "running", "stopping", "stopped", "resetting", "upgrading", "disabled":
	default:
		return false
	}
	if o.StoppedMode != nil && *o.StoppedMode != "keep_charging" && *o.StoppedMode != "stop_charging" && *o.StoppedMode != "not_applicable" {
		return false
	}
	return !o.ObservedAt.IsZero() && (o.StartedAt == nil || !o.StartedAt.After(o.ObservedAt))
}

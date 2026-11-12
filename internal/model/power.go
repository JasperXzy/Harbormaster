package model

import "regexp"

var powerEventKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func ValidPowerEventKey(key string) bool { return powerEventKeyPattern.MatchString(key) }

// PowerJobResult records request intent separately from observed cloud state.
type PowerJobResult struct {
	Action          string  `json:"action"`
	StopMode        *string `json:"stop_mode"`
	DispatchOutcome string  `json:"dispatch_outcome"`
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

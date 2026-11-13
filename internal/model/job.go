package model

import "context"

type jobTriggerKey struct{}

func ValidJobTrigger(value string) bool {
	switch value {
	case "panel", "cli", "api", "telegram", "schedule", "system", "unknown":
		return true
	default:
		return false
	}
}

// WithJobTrigger is set by trusted entry points, never from a request payload.
func WithJobTrigger(ctx context.Context, trigger string) context.Context {
	if !ValidJobTrigger(trigger) {
		trigger = "unknown"
	}
	return context.WithValue(ctx, jobTriggerKey{}, trigger)
}
func JobTrigger(ctx context.Context) string {
	if trigger, ok := ctx.Value(jobTriggerKey{}).(string); ok {
		return trigger
	}
	return "system"
}

type JobFilter struct {
	AccountID, InstanceID, Type, Status, Trigger string
}

func (f JobFilter) Validate() map[string]string {
	fields := map[string]string{}
	if f.Type != "" && f.Type != "refresh" && f.Type != "permission_check" && f.Type != "telegram_test" && f.Type != "instance_power" && f.Type != "instance_release" && f.Type != "heartbeat_test" && f.Type != "network_probe" {
		fields["type"] = "invalid_format"
	}
	if f.Status != "" && f.Status != "queued" && f.Status != "running" && f.Status != "succeeded" && f.Status != "failed" {
		fields["status"] = "invalid_format"
	}
	if f.Trigger != "" && !ValidJobTrigger(f.Trigger) {
		fields["trigger"] = "invalid_format"
	}
	return fields
}

package model

type AlertParams struct {
	BeforeDays          *int `json:"before_days,omitempty"`
	ConsecutiveFailures *int `json:"consecutive_failures,omitempty"`
	OfflineAfterSeconds *int `json:"offline_after_seconds,omitempty"`
}
type AlertRule struct {
	Key     string      `json:"key"`
	Enabled bool        `json:"enabled"`
	Params  AlertParams `json:"params"`
	Urgent  bool        `json:"urgent"`
}
type AlertRuleUpdate struct {
	Enabled *bool        `json:"enabled"`
	Params  *AlertParams `json:"params"`
}

func (r AlertRule) Validate() map[string]string {
	fields := map[string]string{}
	if r.Key != "instance_expiring" && r.Params.BeforeDays != nil {
		fields["params.before_days"] = "not_applicable"
	}
	switch r.Key {
	case "spec_bandwidth_insufficient", "instance_public_ip_changed":
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "instance_expiring":
		if r.Params.BeforeDays == nil {
			fields["params.before_days"] = "required"
		} else if *r.Params.BeforeDays < 0 || *r.Params.BeforeDays > 365 {
			fields["params.before_days"] = "out_of_range"
		}
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "account_api_failure", "cdt_official_unavailable":
		if r.Params.ConsecutiveFailures == nil {
			fields["params.consecutive_failures"] = "required"
		} else if *r.Params.ConsecutiveFailures < 1 || *r.Params.ConsecutiveFailures > 1000 {
			fields["params.consecutive_failures"] = "out_of_range"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "agent_offline":
		if r.Params.OfflineAfterSeconds == nil {
			fields["params.offline_after_seconds"] = "required"
		} else if *r.Params.OfflineAfterSeconds < 10 || *r.Params.OfflineAfterSeconds > 86400 {
			fields["params.offline_after_seconds"] = "out_of_range"
		}
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
	default:
		fields["key"] = "not_supported"
	}
	return fields
}

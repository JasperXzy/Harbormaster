package model

import "time"

type RetentionSettings struct {
	LatencyProbeDays  int `json:"latency_probe_days"`
	AgentRawDays      int `json:"agent_raw_days"`
	AgentHourlyMonths int `json:"agent_hourly_months"`
	AuditLogDays      int `json:"audit_log_days"`
	JobHistoryDays    int `json:"job_history_days"`
}

func DefaultRetentionSettings(probeDays int) RetentionSettings {
	if probeDays <= 0 {
		probeDays = 30
	}
	return RetentionSettings{
		LatencyProbeDays:  probeDays,
		AgentRawDays:      7,
		AgentHourlyMonths: 13,
		AuditLogDays:      180,
		JobHistoryDays:    90,
	}
}

type RetentionSettingsUpdate struct {
	LatencyProbeDays  *int `json:"latency_probe_days"`
	AgentRawDays      *int `json:"agent_raw_days"`
	AgentHourlyMonths *int `json:"agent_hourly_months"`
	AuditLogDays      *int `json:"audit_log_days"`
	JobHistoryDays    *int `json:"job_history_days"`
}

type SettingsUpdate struct {
	BusinessTimezone *string                  `json:"business_timezone"`
	Retention        *RetentionSettingsUpdate `json:"retention"`
}

func (u SettingsUpdate) Validate() map[string]string {
	fields := map[string]string{}
	if u.BusinessTimezone != nil {
		if *u.BusinessTimezone == "" || len(*u.BusinessTimezone) > 64 {
			fields["business_timezone"] = "invalid_timezone"
		} else if _, err := time.LoadLocation(*u.BusinessTimezone); err != nil {
			fields["business_timezone"] = "invalid_timezone"
		}
	}
	if u.Retention != nil {
		r := u.Retention
		if r.LatencyProbeDays != nil && (*r.LatencyProbeDays < 1 || *r.LatencyProbeDays > 3650) {
			fields["retention.latency_probe_days"] = "out_of_range"
		}
		if r.AgentRawDays != nil && (*r.AgentRawDays < 2 || *r.AgentRawDays > 365) {
			fields["retention.agent_raw_days"] = "out_of_range"
		}
		if r.AgentHourlyMonths != nil && (*r.AgentHourlyMonths < 1 || *r.AgentHourlyMonths > 60) {
			fields["retention.agent_hourly_months"] = "out_of_range"
		}
		if r.AuditLogDays != nil && (*r.AuditLogDays < 7 || *r.AuditLogDays > 3650) {
			fields["retention.audit_log_days"] = "out_of_range"
		}
		if r.JobHistoryDays != nil && (*r.JobHistoryDays < 7 || *r.JobHistoryDays > 3650) {
			fields["retention.job_history_days"] = "out_of_range"
		}
	}
	return fields
}

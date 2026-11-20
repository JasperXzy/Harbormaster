package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type SettingsValidationError struct{ Fields map[string]string }

func (*SettingsValidationError) Error() string { return "validation_failed" }

func readSettingsTx(ctx context.Context, q queryRower, defaultZone string, defaultProbeDays int) (string, model.RetentionSettings, error) {
	if defaultZone == "" {
		defaultZone = "Asia/Shanghai"
	}
	zone := defaultZone
	err := q.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='business_timezone'").Scan(&zone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", model.RetentionSettings{}, err
	}
	ret := model.DefaultRetentionSettings(defaultProbeDays)
	var raw string
	err = q.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='retention_settings'").Scan(&raw)
	if err == nil {
		var saved model.RetentionSettings
		if json.Unmarshal([]byte(raw), &saved) == nil {
			if saved.LatencyProbeDays > 0 {
				ret.LatencyProbeDays = saved.LatencyProbeDays
			}
			if saved.AgentRawDays >= 2 {
				ret.AgentRawDays = saved.AgentRawDays
			}
			if saved.AgentHourlyMonths >= 1 {
				ret.AgentHourlyMonths = saved.AgentHourlyMonths
			}
			if saved.AuditLogDays >= 7 {
				ret.AuditLogDays = saved.AuditLogDays
			}
			if saved.JobHistoryDays >= 7 {
				ret.JobHistoryDays = saved.JobHistoryDays
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", model.RetentionSettings{}, err
	}
	return zone, ret, nil
}

func (s *Store) Settings(ctx context.Context, defaultZone string, defaultProbeDays int) (string, model.RetentionSettings, error) {
	return readSettingsTx(ctx, s.DB, defaultZone, defaultProbeDays)
}

func (s *Store) UpdateSettings(ctx context.Context, defaultZone string, defaultProbeDays int, in model.SettingsUpdate, now time.Time) (string, model.RetentionSettings, error) {
	if fields := in.Validate(); len(fields) > 0 {
		return "", model.RetentionSettings{}, &SettingsValidationError{Fields: fields}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", model.RetentionSettings{}, err
	}
	defer tx.Rollback()
	prevZone, prevRet, err := readSettingsTx(ctx, tx, defaultZone, defaultProbeDays)
	if err != nil {
		return "", model.RetentionSettings{}, err
	}
	nextZone, nextRet := prevZone, prevRet
	if in.BusinessTimezone != nil {
		nextZone = *in.BusinessTimezone
	}
	if in.Retention != nil {
		r := in.Retention
		if r.LatencyProbeDays != nil {
			nextRet.LatencyProbeDays = *r.LatencyProbeDays
		}
		if r.AgentRawDays != nil {
			nextRet.AgentRawDays = *r.AgentRawDays
		}
		if r.AgentHourlyMonths != nil {
			nextRet.AgentHourlyMonths = *r.AgentHourlyMonths
		}
		if r.AuditLogDays != nil {
			nextRet.AuditLogDays = *r.AuditLogDays
		}
		if r.JobHistoryDays != nil {
			nextRet.JobHistoryDays = *r.JobHistoryDays
		}
	}
	details := auditDiff(
		auditChange{"business_timezone", prevZone, nextZone},
		auditChange{"latency_probe_days", prevRet.LatencyProbeDays, nextRet.LatencyProbeDays},
		auditChange{"agent_raw_days", prevRet.AgentRawDays, nextRet.AgentRawDays},
		auditChange{"agent_hourly_months", prevRet.AgentHourlyMonths, nextRet.AgentHourlyMonths},
		auditChange{"audit_log_days", prevRet.AuditLogDays, nextRet.AuditLogDays},
		auditChange{"job_history_days", prevRet.JobHistoryDays, nextRet.JobHistoryDays},
	)
	if len(details.ChangedFields) == 0 {
		return nextZone, nextRet, tx.Commit()
	}
	if nextZone != prevZone {
		if _, err = tx.ExecContext(ctx, "INSERT INTO app_meta(key,value) VALUES('business_timezone',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", nextZone); err != nil {
			return "", model.RetentionSettings{}, err
		}
	}
	if nextRet != prevRet {
		raw, err := json.Marshal(nextRet)
		if err != nil {
			return "", model.RetentionSettings{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO app_meta(key,value) VALUES('retention_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw)); err != nil {
			return "", model.RetentionSettings{}, err
		}
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "settings_updated", TargetType: "settings", TargetID: "general", Details: details}, now); err != nil {
		return "", model.RetentionSettings{}, err
	}
	return nextZone, nextRet, tx.Commit()
}

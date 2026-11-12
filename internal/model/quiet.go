package model

import "time"

type MaintenanceWindow struct {
	StartAt time.Time `json:"start_at"`
	EndAt   time.Time `json:"end_at"`
}
type QuietHours struct {
	Revision     int64              `json:"revision"`
	Enabled      bool               `json:"enabled"`
	Start        string             `json:"start"`
	End          string             `json:"end"`
	Maintenance  *MaintenanceWindow `json:"maintenance"`
	Timezone     string             `json:"timezone"`
	UpdatedAt    *time.Time         `json:"updated_at"`
	Active       bool               `json:"active"`
	ActiveReason *string            `json:"active_reason"`
}

func clockMinute(raw string) (int, bool) {
	if len(raw) != 5 || raw[2] != ':' {
		return 0, false
	}
	at, err := time.Parse("15:04", raw)
	if err != nil {
		return 0, false
	}
	return at.Hour()*60 + at.Minute(), true
}
func (q QuietHours) Validate() map[string]string {
	fields := map[string]string{}
	_, startOK := clockMinute(q.Start)
	_, endOK := clockMinute(q.End)
	if !startOK {
		fields["start"] = "invalid_format"
	}
	if !endOK {
		fields["end"] = "invalid_format"
	}
	if startOK && endOK && q.Start == q.End {
		fields["end"] = "must_differ"
	}
	if q.Revision < 0 {
		fields["revision"] = "out_of_range"
	}
	if q.Maintenance != nil && (q.Maintenance.StartAt.IsZero() || q.Maintenance.EndAt.IsZero() || !q.Maintenance.EndAt.After(q.Maintenance.StartAt)) {
		fields["maintenance"] = "invalid_range"
	}
	return fields
}
func (q *QuietHours) Evaluate(now time.Time) error {
	q.Active = false
	q.ActiveReason = nil
	if q.Maintenance != nil && !now.Before(q.Maintenance.StartAt) && now.Before(q.Maintenance.EndAt) {
		reason := "maintenance"
		q.Active = true
		q.ActiveReason = &reason
		return nil
	}
	if !q.Enabled {
		return nil
	}
	location, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return err
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	start, _ := clockMinute(q.Start)
	end, _ := clockMinute(q.End)
	active := minute >= start && minute < end
	if start > end {
		active = minute >= start || minute < end
	}
	if active {
		reason := "daily"
		q.Active = true
		q.ActiveReason = &reason
	}
	return nil
}

// These classifications cannot be demoted by caller-supplied false values.
func (n Notification) AlwaysUrgent() bool {
	if n.Urgent || n.Kind == "test" || n.Severity == "critical" {
		return true
	}
	if n.RuleKey != nil {
		switch *n.RuleKey {
		case "agent_offline", "cdt_usage", "cdt_forecast_exceed", "cdt_official_unavailable":
			return true
		}
	}
	return false
}

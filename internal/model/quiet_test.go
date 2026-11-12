package model

import (
	"testing"
	"time"
)

func TestQuietHoursBoundariesAndMaintenancePriority(t *testing.T) {
	q := QuietHours{Enabled: true, Start: "23:00", End: "08:00", Timezone: "Asia/Singapore"}
	for _, tc := range []struct {
		at     string
		active bool
	}{{"2026-11-12T14:59:59Z", false}, {"2026-11-12T15:00:00Z", true}, {"2026-11-12T23:59:59Z", true}, {"2026-11-13T00:00:00Z", false}} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if err := q.Evaluate(at); err != nil || q.Active != tc.active {
			t.Fatal(tc, q, err)
		}
	}
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	q.Enabled = false
	q.Maintenance = &MaintenanceWindow{StartAt: start, EndAt: start.Add(time.Hour)}
	if err := q.Evaluate(start); err != nil || !q.Active || q.ActiveReason == nil || *q.ActiveReason != "maintenance" {
		t.Fatal(q, err)
	}
	if err := q.Evaluate(start.Add(time.Hour)); err != nil || q.Active {
		t.Fatal("maintenance end included", q, err)
	}
	q.Enabled = true
	q.Start = "00:00"
	q.End = "23:59"
	if err := q.Evaluate(start); err != nil || *q.ActiveReason != "maintenance" {
		t.Fatal("maintenance precedence", q, err)
	}
}
func TestQuietHoursValidationAndUrgentExemptions(t *testing.T) {
	for _, clock := range []string{"1:00", "24:00", "23:60", "ab:cd", ""} {
		q := QuietHours{Start: clock, End: "08:00"}
		if len(q.Validate()) == 0 {
			t.Fatal("invalid clock accepted", clock)
		}
	}
	if len((QuietHours{Start: "08:00", End: "08:00"}).Validate()) == 0 {
		t.Fatal("ambiguous full-day window accepted")
	}
	for _, key := range []string{"agent_offline", "cdt_usage", "cdt_forecast_exceed", "cdt_official_unavailable"} {
		n := Notification{Kind: "alert", Severity: "info", RuleKey: &key}
		if !n.AlwaysUrgent() {
			t.Fatal(key)
		}
	}
	for _, n := range []Notification{{Kind: "test"}, {Urgent: true}, {Severity: "critical"}} {
		if !n.AlwaysUrgent() {
			t.Fatal(n)
		}
	}
}

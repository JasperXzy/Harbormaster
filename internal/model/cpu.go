package model

import "time"

const CPUFreshness = 10 * time.Minute

type CPUMetric struct {
	MetricName    string     `json:"metric_name"`
	Unit          string     `json:"unit"`
	PeriodSeconds int        `json:"period_seconds"`
	Value         *float64   `json:"value"`
	Status        string     `json:"status"`
	ObservedAt    *time.Time `json:"observed_at"`
	CheckedAt     time.Time  `json:"checked_at"`
	LastError     *APIError  `json:"last_error"`
}
type InstanceCPU struct {
	Source        string    `json:"source"`
	Namespace     string    `json:"namespace"`
	Usage         CPUMetric `json:"usage"`
	CreditBalance CPUMetric `json:"credit_balance"`
}

func EmptyCPUMetric(name string, now time.Time) CPUMetric {
	unit := "count"
	if name == "CPUUtilization" {
		unit = "percent"
	}
	return CPUMetric{MetricName: name, Unit: unit, PeriodSeconds: 60, Status: "unknown", CheckedAt: now, LastError: Error("metric_data_unavailable")}
}
func (m *CPUMetric) Age(now time.Time) {
	if m.Status == "ok" && (m.ObservedAt == nil || m.ObservedAt.Add(CPUFreshness).Before(now) || m.ObservedAt.After(now)) {
		m.Status = "stale"
	}
}

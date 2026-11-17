package model

import "time"

type CDTRegion struct {
	Region          string `json:"region"`
	ISP             string `json:"isp"`
	ReportedTraffic int64  `json:"reported_traffic"`
}

// Values are provisionally interpreted as bytes by explicit user decision.
// The provider does not supply units, a billing period, or a cutoff timestamp.
type CDTObservation struct {
	CoveredUntil *time.Time  `json:"covered_until"`
	PeriodStart  *time.Time  `json:"period_start"`
	PeriodEnd    *time.Time  `json:"period_end"`
	Source       string      `json:"source"`
	ID           string      `json:"id"`
	AccountID    string      `json:"account_id"`
	FetchedAt    time.Time   `json:"fetched_at"`
	Mainland     *int64      `json:"mainland_bytes"`
	Overseas     *int64      `json:"overseas_bytes"`
	Quality      string      `json:"official_quality"`
	Error        *APIError   `json:"error"`
	Details      []CDTRegion `json:"-"`
}

type QuotaStatus struct {
	Estimation        *TrafficEstimation `json:"estimation"`
	AccountID         string             `json:"account_id"`
	Scope             string             `json:"scope"`
	CycleStart        time.Time          `json:"cycle_start"`
	CycleEnd          time.Time          `json:"cycle_end"`
	QuotaBytes        int64              `json:"quota_bytes"`
	OfficialBytes     *int64             `json:"official_bytes"`
	OfficialAsOf      *time.Time         `json:"official_as_of"`
	OfficialStatus    string             `json:"official_status"`
	OfficialQuality   string             `json:"official_quality"`
	FetchedAt         *time.Time         `json:"fetched_at"`
	RealtimeIncrement *int64             `json:"realtime_increment_bytes"`
	WorstCaseBytes    *int64             `json:"worst_case_bytes"`
	EstimatedBytes    *int64             `json:"estimated_bytes"`
	EffectiveBytes    *int64             `json:"effective_bytes"`
	Thresholds        []int              `json:"thresholds_percent"`
	Forecast          any                `json:"forecast"`
	UpdatedAt         *time.Time         `json:"updated_at"`
}

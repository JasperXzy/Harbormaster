package model

import "time"

type DailyScope struct {
	Bytes         *int64     `json:"bytes"`
	KnownBytes    int64      `json:"known_bytes"`
	HasData       bool       `json:"has_data"`
	Source        string     `json:"source"`
	Status        string     `json:"status"`
	CompleteDay   bool       `json:"is_complete_day"`
	From          time.Time  `json:"from_at"`
	Until         time.Time  `json:"until_at"`
	ObservedFrom  *time.Time `json:"observed_from"`
	ObservedUntil *time.Time `json:"observed_until"`
	Reasons       []string   `json:"reasons"`
}
type DailyTrafficDay struct {
	Date        string                `json:"date"`
	Mainland    *int64                `json:"mainland_bytes"`
	Overseas    *int64                `json:"overseas_bytes"`
	Provisional bool                  `json:"provisional"`
	Scopes      map[string]DailyScope `json:"scopes"`
}
type UsagePoint struct {
	At    time.Time `json:"at"`
	Date  string    `json:"date"`
	Bytes int64     `json:"bytes"`
}
type DailyTraffic struct {
	AccountID      string                  `json:"account_id"`
	CycleStart     time.Time               `json:"cycle_start"`
	CycleEnd       time.Time               `json:"cycle_end"`
	Timezone       string                  `json:"data_timezone"`
	Days           []DailyTrafficDay       `json:"days"`
	UpdatedAt      *time.Time              `json:"updated_at"`
	SourceStatus   string                  `json:"source_status"`
	OfficialPoints map[string][]UsagePoint `json:"official_points"`
}
type TrafficForecast struct {
	DailyAverage      int64        `json:"daily_average_bytes"`
	CycleEndBytes     int64        `json:"cycle_end_bytes"`
	ExceedsOn         *string      `json:"exceeds_on"`
	AlreadyExceeded   bool         `json:"already_exceeded"`
	SampleDays        int          `json:"sample_days"`
	SampleDates       []string     `json:"sample_dates"`
	Source            string       `json:"source"`
	Unverified        bool         `json:"unverified"`
	BaselineBytes     int64        `json:"baseline_bytes"`
	BaselineAt        time.Time    `json:"baseline_at"`
	BaselineSource    string       `json:"baseline_source"`
	BaselineAtAssumed bool         `json:"baseline_at_assumed"`
	CalculatedAt      time.Time    `json:"calculated_at"`
	Points            []UsagePoint `json:"points"`
	Reasons           []string     `json:"reasons"`
}

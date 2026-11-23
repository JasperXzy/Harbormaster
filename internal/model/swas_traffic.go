package model

import "time"

const SWASTrafficFreshness = 10 * time.Minute
const MaxSWASTrafficBytes = (1 << 53) - 1

type SWASTrafficUsage struct {
	Total     int64 `json:"total_bytes"`
	Used      int64 `json:"used_bytes"`
	Remaining int64 `json:"remaining_bytes"`
	Overflow  int64 `json:"overflow_bytes"`
}

func (u SWASTrafficUsage) Valid() bool {
	for _, n := range []int64{u.Total, u.Used, u.Overflow} {
		if n < 0 || n > MaxSWASTrafficBytes {
			return false
		}
	}
	return u.Remaining >= -MaxSWASTrafficBytes && u.Remaining <= MaxSWASTrafficBytes
}

type SWASTrafficObservation struct {
	InstanceID, PlanID string
	QueriedAt          time.Time
	EntryPresent       *bool
	Usage              *SWASTrafficUsage
	Error              *APIError
}

type SWASTrafficTarget struct{ InstanceID, PlanID string }

type SWASTrafficPackage struct {
	Source        string            `json:"source"`
	Status        string            `json:"status"`
	EntryPresent  *bool             `json:"entry_present"`
	Usage         *SWASTrafficUsage `json:"usage"`
	FetchedAt     *time.Time        `json:"fetched_at"`
	LastAttemptAt *time.Time        `json:"last_attempt_at"`
	UpdatedAt     *time.Time        `json:"updated_at"`
	LastError     *APIError         `json:"last_error"`
	Direction     string            `json:"direction"`
	ResetDay      int               `json:"reset_day"`
	OfficialAsOf  *time.Time        `json:"official_as_of"`
	NextResetAt   *time.Time        `json:"next_reset_at"`
}

func EmptySWASTrafficPackage() SWASTrafficPackage {
	return SWASTrafficPackage{Source: "swas_list_instances_traffic_packages", Status: "never_fetched", Direction: "outbound", ResetDay: 1}
}

func (p *SWASTrafficPackage) Age(now time.Time) {
	if p.Status == "ok" && (p.FetchedAt == nil || p.FetchedAt.After(now) || p.FetchedAt.Add(SWASTrafficFreshness).Before(now)) {
		p.Status = "stale"
	}
}

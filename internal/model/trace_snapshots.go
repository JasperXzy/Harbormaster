package model

import (
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type TraceSchedule struct {
	PlanID        string     `json:"plan_id"`
	Revision      int64      `json:"revision"`
	Enabled       bool       `json:"enabled"`
	NotifyChanges bool       `json:"notify_changes"`
	TimeOfDay     string     `json:"time_of_day"`
	MaxHops       int        `json:"max_hops"`
	Count         int        `json:"count"`
	TimeoutMS     int        `json:"timeout_ms"`
	Active        bool       `json:"active"`
	Timezone      string     `json:"timezone"`
	UpdatedAt     *time.Time `json:"updated_at"`
	NextRunAt     *time.Time `json:"next_run_at"`
}

func DefaultTraceSchedule() TraceSchedule {
	return TraceSchedule{TimeOfDay: "03:00", MaxHops: 30, Count: 3, TimeoutMS: 1000}
}
func (s TraceSchedule) Validate() bool {
	_, ok := clockMinute(s.TimeOfDay)
	return ok && s.Revision >= 0 && (netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "icmp", MaxHops: s.MaxHops, Count: s.Count, TimeoutMS: s.TimeoutMS}).Validate() == nil
}

// NextTraceTime chooses the first occurrence of the daily civil time. A missing
// minute moves to the first later valid minute; a repeated hour never runs twice.
func NextTraceTime(now time.Time, clock string, loc *time.Location) (time.Time, error) {
	minute, ok := clockMinute(clock)
	if !ok || loc == nil {
		return time.Time{}, netprobe.ErrInvalid
	}
	local := now.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	for day := 0; day < 4; day++ {
		d := date.AddDate(0, 0, day)
		start := d.Add(-24 * time.Hour)
		for step := 0; step < 72*60; step++ {
			at := start.Add(time.Duration(step) * time.Minute)
			wall := at.In(loc)
			if wall.Year() != d.Year() || wall.Month() != d.Month() || wall.Day() != d.Day() || wall.Hour()*60+wall.Minute() < minute {
				continue
			}
			if at.After(now) {
				return at.UTC(), nil
			}
			break
		}
	}
	return time.Time{}, netprobe.ErrInvalid
}

type TraceComparison struct {
	Status              string          `json:"status"`
	Reason              *string         `json:"reason"`
	PreviousSnapshotID  *string         `json:"previous_snapshot_id"`
	PreviousTraceID     *string         `json:"previous_trace_id"`
	Address             *string         `json:"address"`
	PreviousAddress     *string         `json:"previous_address"`
	ASNs                []uint32        `json:"asns"`
	PreviousASNs        []uint32        `json:"previous_asns"`
	ASNDatabase         *ipinfo.Dataset `json:"asn_database"`
	PreviousASNDatabase *ipinfo.Dataset `json:"previous_asn_database"`
	NotificationID      *string         `json:"notification_id"`
}

func PendingTraceComparison() TraceComparison {
	return TraceComparison{Status: "waiting_result", ASNs: []uint32{}, PreviousASNs: []uint32{}}
}

type TraceSnapshot struct {
	ID               string          `json:"id"`
	PlanID           string          `json:"plan_id"`
	PlanName         string          `json:"plan_name"`
	PlanRevision     int64           `json:"plan_revision"`
	ScheduleRevision int64           `json:"schedule_revision"`
	TargetID         string          `json:"target_id"`
	TargetRevision   int64           `json:"target_revision"`
	DueAt            time.Time       `json:"due_at"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Status           string          `json:"status"`
	SkipReason       *string         `json:"skip_reason"`
	TraceID          *string         `json:"trace_id"`
	JobID            *string         `json:"job_id"`
	NotifyChanges    bool            `json:"notify_changes"`
	Comparison       TraceComparison `json:"comparison"`
}

// TraceASPath returns a sequence of complete observed-IP ASN candidates, not a
// BGP AS path. PTR and geography errors cannot change this ASN evidence.
func TraceASPath(d TraceDetail) ([]uint32, string) {
	if d.Status != "succeeded" || d.Outcome == nil || !d.Outcome.Reached || d.Outcome.Address == "" {
		return nil, "measurement_failed"
	}
	e := d.Enrichment
	if e == nil || e.ASNDatabase == nil || len(e.Hops) == 0 || len(e.Hops) != d.Outcome.HopCount {
		return nil, "enrichment_unavailable"
	}
	path := []uint32{}
	for i, h := range e.Hops {
		if h.TTL != i+1 || h.Attempts != d.Request.Count || h.Responses != h.Attempts {
			return nil, "incomplete_responses"
		}
		if len(h.ASNCandidates) > 1 {
			return nil, "multiple_asns"
		}
		if h.UnknownASN || len(h.ASNCandidates) != 1 || h.ASNCandidates[0] == 0 {
			return nil, "unknown_asn"
		}
		asn := h.ASNCandidates[0]
		if len(path) == 0 || path[len(path)-1] != asn {
			path = append(path, asn)
		}
	}
	return path, ""
}

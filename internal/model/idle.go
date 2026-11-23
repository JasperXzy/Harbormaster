package model

import "time"

var IdleResourceKinds = []string{"ecs", "swas", "disk", "eip"}

func IdleSourceName(kind string) string {
	return map[string]string{"ecs": "ecs_describe_instances", "swas": "swas_list_instances", "disk": "ecs_describe_disks", "eip": "vpc_describe_eip_addresses"}[kind]
}

type IdleCandidate struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	RegionID       string    `json:"region_id"`
	Kind           string    `json:"kind"`
	CloudID        string    `json:"cloud_id"`
	Name           string    `json:"name"`
	Reason         string    `json:"reason"`
	ChargeType     string    `json:"charge_type"`
	CloudStatus    string    `json:"cloud_status"`
	InstanceIDs    []string  `json:"instance_ids"`
	PublicIP       *string   `json:"public_ip"`
	SizeGiB        *int64    `json:"size_gib"`
	ObservedAt     time.Time `json:"observed_at"`
	SourceStatus   string    `json:"source_status"`
	EvidenceStatus string    `json:"evidence_status"`
	MonthlyCost    *string   `json:"monthly_cost"`
	Currency       *string   `json:"currency"`
}

func (c IdleCandidate) Valid() bool {
	if c.ID == "" || c.AccountID == "" || !RegionPattern.MatchString(c.RegionID) || c.CloudID == "" || c.Name == "" || c.InstanceIDs == nil || c.ObservedAt.IsZero() || c.MonthlyCost != nil || c.Currency != nil || (c.SizeGiB != nil && *c.SizeGiB < 0) {
		return false
	}
	if c.ChargeType != "subscription" && c.ChargeType != "pay_as_you_go" && c.ChargeType != "spot" && c.ChargeType != "unknown" {
		return false
	}
	switch c.Kind {
	case "ecs":
		return c.Reason == "stopped_subscription" || c.Reason == "stopped_keep_charging" || c.Reason == "stopped_not_charge_exempt"
	case "swas":
		return c.Reason == "stopped_subscription"
	case "disk":
		return c.Reason == "unattached_cloud_disk" || c.Reason == "disk_on_stopped_instances"
	case "eip":
		return c.Reason == "unbound_eip"
	}
	return false
}

type IdleData struct {
	Scanned         int
	UnclassifiedIDs []string
	NonCandidateIDs []string
	Candidates      []IdleCandidate
}

func (d IdleData) Valid() bool {
	if d.Scanned < 0 || d.Scanned > 10000 || d.UnclassifiedIDs == nil || d.Candidates == nil || len(d.UnclassifiedIDs)+len(d.NonCandidateIDs)+len(d.Candidates) > d.Scanned {
		return false
	}
	seen := map[string]bool{}
	for _, id := range d.UnclassifiedIDs {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, c := range d.Candidates {
		if !c.Valid() || seen[c.CloudID] {
			return false
		}
		seen[c.CloudID] = true
	}
	for _, id := range d.NonCandidateIDs {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

type IdleObservation struct {
	Region, Kind string
	QueriedAt    time.Time
	Data         *IdleData
	Error        *APIError
}

type IdleSource struct {
	AccountID     string     `json:"account_id"`
	RegionID      string     `json:"region_id"`
	Kind          string     `json:"kind"`
	Source        string     `json:"source"`
	Enabled       bool       `json:"enabled"`
	Status        string     `json:"status"`
	FetchedAt     *time.Time `json:"fetched_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastError     *APIError  `json:"last_error"`
	Scanned       *int       `json:"scanned_count"`
	Unclassified  *int       `json:"unclassified_count"`
	Candidates    *int       `json:"candidate_count"`
}

func (s *IdleSource) Age(now time.Time) {
	if !s.Enabled {
		s.Status = "disabled"
		return
	}
	if s.LastError != nil {
		s.Status = "failing"
		return
	}
	if s.FetchedAt == nil {
		s.Status = "never_fetched"
		return
	}
	if now.Before(*s.FetchedAt) || now.Sub(*s.FetchedAt) > 10*time.Minute {
		s.Status = "stale"
		return
	}
	s.Status = "ok"
	if s.Unclassified != nil && *s.Unclassified > 0 {
		s.Status = "partial"
	}
}

type IdleFilter struct {
	AccountID, RegionID, Kind, After string
	Limit                            int
}

func (f IdleFilter) Valid() bool {
	return len(f.AccountID) <= 128 && len(f.RegionID) <= 64 && (f.RegionID == "" || RegionPattern.MatchString(f.RegionID)) && (f.Kind == "" || IdleSourceName(f.Kind) != "") && len(f.After) <= 1024 && f.Limit >= 1 && f.Limit <= 201
}

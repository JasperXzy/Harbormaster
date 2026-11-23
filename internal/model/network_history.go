package model

import (
	"net/netip"
	"time"
)

type CloudRatePoint struct {
	SampledAt time.Time
	Value     float64
}

type CloudNetworkSample struct {
	Sequence      int64     `json:"-"`
	Source        string    `json:"source"`
	NetworkType   string    `json:"network_type"`
	IP            *string   `json:"ip"`
	Direction     string    `json:"direction"`
	MetricName    string    `json:"metric_name"`
	Unit          string    `json:"unit"`
	PeriodSeconds int       `json:"period_seconds"`
	Value         float64   `json:"value"`
	SampledAt     time.Time `json:"sampled_at"`
	CheckedAt     time.Time `json:"checked_at"`
}
type CloudNetworkHistoryFilter struct {
	InstanceID     string
	From, To       time.Time
	Direction, IP  string
	BeforeTime     time.Time
	BeforeSequence int64
}

func (f CloudNetworkHistoryFilter) Valid(now time.Time) bool {
	if f.InstanceID == "" || len(f.InstanceID) > 256 || f.From.IsZero() || !f.To.After(f.From) || f.To.Sub(f.From) > 7*24*time.Hour || f.To.After(now) {
		return false
	}
	if f.Direction != "" && f.Direction != "ingress" && f.Direction != "egress" {
		return false
	}
	if f.IP != "" {
		ip, err := netip.ParseAddr(f.IP)
		if err != nil || !ip.Is4() || ip.String() != f.IP {
			return false
		}
	}
	if f.BeforeTime.IsZero() {
		return f.BeforeSequence == 0
	}
	return f.BeforeSequence > 0 && !f.BeforeTime.Before(f.From) && f.BeforeTime.Before(f.To)
}

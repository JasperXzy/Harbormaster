package model

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type NetworkTarget struct {
	ID         string    `json:"id"`
	Revision   int64     `json:"revision"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Host       *string   `json:"host"`
	InstanceID *string   `json:"instance_id"`
	TCPPort    *int      `json:"tcp_port"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func validProbeName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 100 && strings.IndexFunc(name, unicode.IsControl) < 0
}
func (v NetworkTarget) Validate() bool {
	if v.Revision < 0 || !validProbeName(v.Name) || v.TCPPort != nil && (*v.TCPPort < 1 || *v.TCPPort > 65535) {
		return false
	}
	if v.Kind == "custom" {
		return v.Host != nil && v.InstanceID == nil && netprobe.ValidateHost(*v.Host) == nil
	}
	return v.Kind == "instance" && v.Host == nil && v.InstanceID != nil && *v.InstanceID != "" && len(*v.InstanceID) <= 128
}

type ProbePlan struct {
	ID               string     `json:"id"`
	Revision         int64      `json:"revision"`
	Name             string     `json:"name"`
	PointKind        string     `json:"point_kind"`
	InstanceID       *string    `json:"instance_id"`
	TargetID         string     `json:"target_id"`
	Family           string     `json:"family"`
	Method           string     `json:"method"`
	FrequencySeconds int        `json:"frequency_seconds"`
	Count            int        `json:"count"`
	TimeoutMS        int        `json:"timeout_ms"`
	IntervalMS       int        `json:"interval_ms"`
	Enabled          bool       `json:"enabled"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	NextRunAt        *time.Time `json:"next_run_at"`
	LastRun          *ProbeRun  `json:"last_run"`
}

func DefaultProbePlan() ProbePlan {
	return ProbePlan{FrequencySeconds: 300, Count: 5, TimeoutMS: 2000, IntervalMS: 1000}
}
func (p ProbePlan) Validate() bool {
	if p.Revision < 0 || !validProbeName(p.Name) || p.TargetID == "" || len(p.TargetID) > 128 || p.FrequencySeconds < 60 || p.FrequencySeconds > 86400 {
		return false
	}
	if p.PointKind != "server" && p.PointKind != "agent" || p.PointKind == "server" && p.InstanceID != nil || p.PointKind == "agent" && (p.InstanceID == nil || *p.InstanceID == "" || len(*p.InstanceID) > 128) {
		return false
	}
	host := "1.1.1.1"
	if p.Family == "ipv6" {
		host = "2606:4700:4700::1111"
	}
	port := 0
	if p.Method == "tcp" {
		port = 443
	}
	return p.Request(host, port).Validate() == nil
}
func (p ProbePlan) Request(host string, port int) netprobe.Request {
	return netprobe.Request{Host: host, Port: port, Family: p.Family, Method: p.Method, Count: p.Count, TimeoutMS: p.TimeoutMS, IntervalMS: p.IntervalMS}
}

type ProbePoint struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	PointKind         string     `json:"point_kind"`
	InstanceID        *string    `json:"instance_id"`
	Available         bool       `json:"available"`
	UnavailableReason *string    `json:"unavailable_reason"`
	UpdatedAt         *time.Time `json:"updated_at"`
}
type ProbeRun struct {
	ID             string            `json:"id"`
	PlanID         string            `json:"plan_id"`
	PlanRevision   int64             `json:"plan_revision"`
	TargetID       string            `json:"target_id"`
	TargetRevision int64             `json:"target_revision"`
	PointKind      string            `json:"point_kind"`
	InstanceID     *string           `json:"instance_id"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Status         string            `json:"status"`
	SkipReason     *string           `json:"skip_reason"`
	JobID          *string           `json:"job_id"`
	Request        *netprobe.Request `json:"request"`
	Result         *netprobe.Result  `json:"result"`
	Error          *APIError         `json:"error"`
}

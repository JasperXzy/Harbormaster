package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type TraceCapability struct {
	Available         bool       `json:"available"`
	UnavailableReason *string    `json:"unavailable_reason"`
	Methods           []string   `json:"methods"`
	LastPollAt        *time.Time `json:"last_poll_at"`
}
type TraceSubmission struct {
	RequestKey     string  `json:"request_key"`
	PointKind      string  `json:"point_kind"`
	InstanceID     *string `json:"instance_id"`
	TargetID       *string `json:"target_id"`
	TargetRevision *int64  `json:"target_revision"`
	Host           *string `json:"host"`
	Family         string  `json:"family"`
	Method         string  `json:"method"`
	Port           int     `json:"port"`
	MaxHops        int     `json:"max_hops"`
	Count          int     `json:"count"`
	TimeoutMS      int     `json:"timeout_ms"`
}

func DefaultTraceSubmission() TraceSubmission {
	return TraceSubmission{MaxHops: 30, Count: 3, TimeoutMS: 1000}
}
func (in TraceSubmission) Validate() bool {
	if len(in.RequestKey) < 16 || len(in.RequestKey) > 128 {
		return false
	}
	for _, c := range in.RequestKey {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	if in.PointKind != "server" && in.PointKind != "agent" || in.PointKind == "server" && in.InstanceID != nil || in.PointKind == "agent" && (in.InstanceID == nil || *in.InstanceID == "" || len(*in.InstanceID) > 128) {
		return false
	}
	host := "1.1.1.1"
	if in.Family == "ipv6" {
		host = "2606:4700:4700::1111"
	}
	if in.TargetID != nil {
		if *in.TargetID == "" || len(*in.TargetID) > 128 || in.TargetRevision == nil || *in.TargetRevision < 1 || in.Host != nil {
			return false
		}
	} else {
		if in.Host == nil || in.TargetRevision != nil {
			return false
		}
		host = *in.Host
	}
	return in.Request(host).Validate() == nil
}
func (in TraceSubmission) Request(host string) netprobe.TraceRequest {
	return netprobe.TraceRequest{Host: host, Family: in.Family, Method: in.Method, Port: in.Port, MaxHops: in.MaxHops, Count: in.Count, TimeoutMS: in.TimeoutMS}
}

type TraceOutcome struct {
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Address     string    `json:"address"`
	Reached     bool      `json:"reached"`
	StopReason  string    `json:"stop_reason"`
	HopCount    int       `json:"hop_count"`
	SampleCount int       `json:"sample_count"`
}

func SummarizeTrace(r netprobe.TraceResult) TraceOutcome {
	o := TraceOutcome{StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Address: r.Address, Reached: r.Reached, StopReason: r.StopReason, HopCount: len(r.Hops)}
	for _, hop := range r.Hops {
		o.SampleCount += len(hop.Samples)
	}
	return o
}

type TraceRun struct {
	ID               string                `json:"id"`
	JobID            string                `json:"job_id"`
	PointID          string                `json:"point_id"`
	PointName        string                `json:"point_name"`
	PointKind        string                `json:"point_kind"`
	InstanceID       *string               `json:"instance_id"`
	Target           *NetworkTarget        `json:"target"`
	Request          netprobe.TraceRequest `json:"request"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
	Status           string                `json:"status"`
	EnrichmentStatus string                `json:"enrichment_status"`
	DuplexID         *string               `json:"duplex_id"`
	DuplexDirection  *string               `json:"duplex_direction"`
	Outcome          *TraceOutcome         `json:"outcome"`
	Error            *APIError             `json:"error"`
}
type TraceDetail struct {
	TraceRun
	Result          *netprobe.TraceResult `json:"result"`
	Enrichment      *TraceEnrichment      `json:"enrichment"`
	EnrichmentError *APIError             `json:"enrichment_error"`
}
type TraceAccepted struct {
	JobID   string `json:"job_id"`
	TraceID string `json:"trace_id"`
}
type TraceFilter struct {
	From, To          time.Time
	PointID, TargetID string
	Before            int64
	Limit             int
}

func (f TraceFilter) Validate() bool {
	return f.From.Before(f.To) && f.To.Sub(f.From) <= 31*24*time.Hour && len(f.PointID) <= 128 && len(f.TargetID) <= 128 && f.Before >= 0 && f.Limit >= 1 && f.Limit <= 201
}

// TraceText exports stored observations and enrichment without doing lookups.
func TraceText(d TraceDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "trace: %s\njob: %s\nsource: %q (%s)\nstatus: %s\ntarget: %q\nfamily: %s\nmethod: %s\nport: %d\ncreated_at: %s\n", d.ID, d.JobID, d.PointName, d.PointID, d.Status, d.Request.Host, d.Request.Family, d.Request.Method, d.Request.Port, d.CreatedAt.Format(time.RFC3339Nano))
	if d.Error != nil {
		fmt.Fprintf(&b, "error: %s\n", d.Error.Code)
	}
	if d.DuplexID != nil && d.DuplexDirection != nil {
		fmt.Fprintf(&b, "duplex_trace: %s\nduplex_direction: %s\n", *d.DuplexID, *d.DuplexDirection)
	}
	fmt.Fprintf(&b, "enrichment_status: %s\n", d.EnrichmentStatus)
	if d.EnrichmentError != nil {
		fmt.Fprintf(&b, "enrichment_error: %s\n", d.EnrichmentError.Code)
	}
	if d.Result == nil {
		b.WriteString("result: pending_or_unavailable\n")
		return b.String()
	}
	r := d.Result
	fmt.Fprintf(&b, "address: %q\nreached: %t\nstop_reason: %s\nstarted_at: %s\nfinished_at: %s\n", r.Address, r.Reached, r.StopReason, r.StartedAt.Format(time.RFC3339Nano), r.FinishedAt.Format(time.RFC3339Nano))
	b.WriteString("ttl sequence address rtt_ms reply icmp_type icmp_code error\n")
	for _, h := range r.Hops {
		for _, s := range h.Samples {
			addr, rtt, reply, typ, code, failure := "*", "*", "-", "-", "-", "-"
			if s.Address != nil {
				addr = *s.Address
			}
			if s.RTTMS != nil {
				rtt = fmt.Sprintf("%.3f", *s.RTTMS)
			}
			if s.Reply != nil {
				reply = *s.Reply
			}
			if s.ICMPType != nil {
				typ = fmt.Sprint(*s.ICMPType)
			}
			if s.ICMPCode != nil {
				code = fmt.Sprint(*s.ICMPCode)
			}
			if s.ErrorCode != nil {
				failure = *s.ErrorCode
			}
			fmt.Fprintf(&b, "%d %d %s %s %s %s %s %s\n", h.TTL, s.Sequence, addr, rtt, reply, typ, code, failure)
		}
	}
	b.WriteString("* = no observed response; intermediate silence does not prove forwarding loss.\nTCP reset = target reached with port closed, not a successful service connection.\n")
	traceEnrichmentText(&b, d.Enrichment)
	return b.String()
}

package model

import (
	"fmt"
	"strings"
	"time"
)

type DuplexTraceSubmission struct {
	RequestKey     string `json:"request_key"`
	FromInstanceID string `json:"from_instance_id"`
	ToInstanceID   string `json:"to_instance_id"`
	Family         string `json:"family"`
	Method         string `json:"method"`
	ForwardPort    int    `json:"forward_port"`
	ReversePort    int    `json:"reverse_port"`
	MaxHops        int    `json:"max_hops"`
	Count          int    `json:"count"`
	TimeoutMS      int    `json:"timeout_ms"`
}

func DefaultDuplexTraceSubmission() DuplexTraceSubmission {
	return DuplexTraceSubmission{MaxHops: 30, Count: 3, TimeoutMS: 1000}
}
func (in DuplexTraceSubmission) Validate() bool {
	if in.FromInstanceID == in.ToInstanceID {
		return false
	}
	host := "1.1.1.1"
	if in.Family == "ipv6" {
		host = "2606:4700:4700::1111"
	}
	q := TraceSubmission{RequestKey: in.RequestKey, PointKind: "agent", InstanceID: &in.FromInstanceID, Host: &host, Family: in.Family, Method: in.Method, Port: in.ForwardPort, MaxHops: in.MaxHops, Count: in.Count, TimeoutMS: in.TimeoutMS}
	if !q.Validate() {
		return false
	}
	q.InstanceID, q.Port = &in.ToInstanceID, in.ReversePort
	return q.Validate()
}

type DuplexTraceAccepted struct {
	DuplexID string        `json:"duplex_id"`
	Forward  TraceAccepted `json:"forward"`
	Reverse  TraceAccepted `json:"reverse"`
}
type DuplexTraceEndpoint struct {
	InstanceID    string    `json:"instance_id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	PublicAddress string    `json:"public_address"`
	ObservedAt    time.Time `json:"observed_at"`
}
type DuplexTrace struct {
	ID        string              `json:"id"`
	CreatedAt time.Time           `json:"created_at"`
	From      DuplexTraceEndpoint `json:"from"`
	To        DuplexTraceEndpoint `json:"to"`
	Forward   TraceDetail         `json:"forward"`
	Reverse   TraceDetail         `json:"reverse"`
}

func DuplexTraceText(d DuplexTrace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "duplex_trace: %s\ncreated_at: %s\nfrom: %q (%s, %s, %s) observed_at=%s\nto: %q (%s, %s, %s) observed_at=%s\n", d.ID, d.CreatedAt.Format(time.RFC3339Nano), d.From.Name, d.From.InstanceID, d.From.Kind, d.From.PublicAddress, d.From.ObservedAt.Format(time.RFC3339Nano), d.To.Name, d.To.InstanceID, d.To.Kind, d.To.PublicAddress, d.To.ObservedAt.Format(time.RFC3339Nano))
	b.WriteString("Directions execute independently; inspect each measured start/finish time. No synchronized start or symmetric route is inferred.\n\n[forward]\n")
	b.WriteString(TraceText(d.Forward))
	b.WriteString("\n[reverse]\n")
	b.WriteString(TraceText(d.Reverse))
	return b.String()
}

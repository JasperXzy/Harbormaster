package netprobe

import (
	"context"
	"encoding/json"
	"math"
	"net/netip"
	"testing"
	"time"
)

func validTraceFixture() (TraceRequest, TraceResult, time.Time) {
	q := traceRequest()
	q.MaxHops, q.Count = 1, 1
	now := time.Now().UTC()
	r := TraceResult{StartedAt: now, FinishedAt: now.Add(time.Second), Address: q.Host, ResolvedAddresses: []string{q.Host}, Hops: []TraceHop{{TTL: 1, Samples: []TraceSample{{Sequence: 1, StartedAt: now, Address: new(q.Host), RTTMS: new(float64(1)), Reply: new("echo_reply"), ICMPType: new(0), ICMPCode: new(0)}}}}}
	return q, r, now
}
func TestTraceValidationRejectsMalformedResults(t *testing.T) {
	for name, mutate := range map[string]func(*TraceResult){
		"future":           func(r *TraceResult) { r.FinishedAt = r.FinishedAt.Add(time.Hour) },
		"duration":         func(r *TraceResult) { r.StartedAt = r.StartedAt.Add(-130 * time.Second) },
		"backward":         func(r *TraceResult) { r.FinishedAt = r.StartedAt.Add(-time.Second) },
		"target":           func(r *TraceResult) { r.Address = "8.8.8.8" },
		"private_target":   func(r *TraceResult) { r.Address = "10.0.0.1"; r.ResolvedAddresses = []string{r.Address} },
		"duplicate_dns":    func(r *TraceResult) { r.ResolvedAddresses = append(r.ResolvedAddresses, r.Address) },
		"mixed_dns":        func(r *TraceResult) { r.ResolvedAddresses = append(r.ResolvedAddresses, "10.0.0.1") },
		"wrong_family":     func(r *TraceResult) { r.ResolvedAddresses = append(r.ResolvedAddresses, "2606:4700:4700::1111") },
		"many_answers":     func(r *TraceResult) { r.ResolvedAddresses = make([]string, 65) },
		"no_target":        func(r *TraceResult) { r.Address = "" },
		"no_hops":          func(r *TraceResult) { r.Hops = nil },
		"too_many_hops":    func(r *TraceResult) { r.Hops = append(r.Hops, r.Hops[0]) },
		"wrong_ttl":        func(r *TraceResult) { r.Hops[0].TTL = 0 },
		"no_samples":       func(r *TraceResult) { r.Hops[0].Samples = nil },
		"too_many_samples": func(r *TraceResult) { r.Hops[0].Samples = append(r.Hops[0].Samples, r.Hops[0].Samples[0]) },
		"sequence":         func(r *TraceResult) { r.Hops[0].Samples[0].Sequence = 2 },
		"time":             func(r *TraceResult) { r.Hops[0].Samples[0].StartedAt = r.FinishedAt.Add(time.Second) },
		"not_target_echo":  func(r *TraceResult) { r.Hops[0].Samples[0].Address = new("10.0.0.1") },
		"invalid_address":  func(r *TraceResult) { r.Hops[0].Samples[0].Address = new("hostname") },
		"multicast":        func(r *TraceResult) { r.Hops[0].Samples[0].Address = new("224.0.0.1") },
		"nan":              func(r *TraceResult) { r.Hops[0].Samples[0].RTTMS = new(math.NaN()) },
		"infinite":         func(r *TraceResult) { r.Hops[0].Samples[0].RTTMS = new(math.Inf(1)) },
		"negative_rtt":     func(r *TraceResult) { r.Hops[0].Samples[0].RTTMS = new(float64(-1)) },
		"excess_rtt":       func(r *TraceResult) { r.Hops[0].Samples[0].RTTMS = new(float64(1101)) },
		"no_rtt":           func(r *TraceResult) { r.Hops[0].Samples[0].RTTMS = nil },
		"no_address":       func(r *TraceResult) { r.Hops[0].Samples[0].Address = nil },
		"no_reply":         func(r *TraceResult) { r.Hops[0].Samples[0].Reply = nil },
		"unknown_reply":    func(r *TraceResult) { r.Hops[0].Samples[0].Reply = new("untrusted text") },
		"no_code":          func(r *TraceResult) { r.Hops[0].Samples[0].ICMPCode = nil },
		"wrong_type":       func(r *TraceResult) { r.Hops[0].Samples[0].ICMPType = new(129) },
		"wrong_code":       func(r *TraceResult) { r.Hops[0].Samples[0].ICMPCode = new(1) },
		"tcp_in_icmp": func(r *TraceResult) {
			r.Hops[0].Samples[0].Reply = new("tcp_reset")
			r.Hops[0].Samples[0].ICMPType = nil
			r.Hops[0].Samples[0].ICMPCode = nil
		},
		"mixed_error": func(r *TraceResult) { r.Hops[0].Samples[0].ErrorCode = new("probe_timeout") },
	} {
		t.Run(name, func(t *testing.T) {
			q, r, now := validTraceFixture()
			mutate(&r)
			if ValidateTraceResult(q, &r, nil, now) != ErrInvalid {
				t.Fatal("accepted", r)
			}
		})
	}
}

func TestTraceValidationCanonicalizesOutcomesAndAcceptsPartialFailure(t *testing.T) {
	for _, mode := range []string{"echo", "tcp_reset", "tcp_syn_ack", "timeout", "unreachable", "parameter", "too_big", "hop", "partial", "setup", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			q, r, now := validTraceFixture()
			sample := &r.Hops[0].Samples[0]
			var code *string
			want := "reached"
			switch mode {
			case "tcp_reset", "tcp_syn_ack":
				q.Method, q.Port = "tcp", 443
				sample.Reply = new(mode)
				sample.ICMPType, sample.ICMPCode = nil, nil
			case "timeout":
				*sample = TraceSample{Sequence: 1, StartedAt: now, ErrorCode: new("probe_timeout")}
				want = "max_hops"
			case "unreachable":
				sample.Reply = new("destination_unreachable")
				sample.ICMPType = new(3)
				sample.ICMPCode = new(1)
				sample.Address = new("10.0.0.1")
				want = "unreachable"
			case "parameter":
				sample.Reply = new("parameter_problem")
				sample.ICMPType = new(12)
				sample.ICMPCode = new(2)
				want = "unreachable"
			case "too_big":
				q.Host, q.Family = "2606:4700:4700::1111", "ipv6"
				r.Address = q.Host
				r.ResolvedAddresses = []string{q.Host}
				sample.Address = new("fe80::1")
				sample.Reply = new("packet_too_big")
				sample.ICMPType = new(2)
				want = "unreachable"
			case "hop":
				sample.Address = new("10.0.0.1")
				sample.Reply = new("time_exceeded")
				sample.ICMPType = new(11)
				want = "max_hops"
			case "partial":
				q.Count = 2
				code = new("probe_cancelled")
				want = "incomplete"
			case "setup", "interrupted":
				r.Address = ""
				r.ResolvedAddresses = []string{}
				r.Hops = []TraceHop{}
				code = new("probe_unavailable")
				if mode == "interrupted" {
					code = new("probe_interrupted")
				}
				want = "incomplete"
			}
			r.Reached = true
			r.StopReason = "arbitrary caller value"
			if err := ValidateTraceResult(q, &r, code, now); err != nil || r.StopReason != want || r.Reached != (want == "reached" || mode == "partial") {
				t.Fatal(r, err)
			}
		})
	}
	q, r, now := validTraceFixture()
	q.MaxHops = 2
	r.Hops = append(r.Hops, TraceHop{TTL: 2, Samples: []TraceSample{{Sequence: 2, StartedAt: now, ErrorCode: new("probe_timeout")}}})
	if ValidateTraceResult(q, &r, nil, now) != ErrInvalid {
		t.Fatal("continued beyond terminal hop")
	}
}

func TestTraceExecutorResultsPassWireValidation(t *testing.T) {
	for _, localFailure := range []bool{false, true} {
		q := traceRequest()
		q.Count = 2
		q.MaxHops = 2
		s := &fakeTraceSession{probe: func(ttl, seq int) (traceReply, error) {
			if seq == 2 && localFailure {
				return traceReply{}, ErrIO
			}
			if ttl == 1 {
				return traceReply{address: netip.MustParseAddr("10.0.0.1"), kind: "time_exceeded", icmpType: new(11), icmpCode: new(0), elapsed: time.Millisecond}, nil
			}
			return traceReply{}, ErrTimeout
		}}
		runner := traceRunner{open: func(context.Context, TraceRequest, netip.Addr) (traceSession, error) { return s, nil }, wait: func(context.Context, time.Duration) error { return nil }}
		r, err := runner.run(context.Background(), q)
		var code *string
		if err != nil {
			code = new(FailureCode(err))
		}
		b, _ := json.Marshal(r)
		var wire TraceResult
		if json.Unmarshal(b, &wire) != nil {
			t.Fatal("decode")
		}
		if err := ValidateTraceResult(q, &wire, code, time.Now()); err != nil {
			t.Fatal(r, err)
		}
	}
}

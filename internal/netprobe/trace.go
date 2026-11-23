package netprobe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// TraceRequest permits only fixed echo/SYN probes, never packet contents or commands.
type TraceRequest struct {
	Host      string `json:"host"`
	Family    string `json:"family"`
	Method    string `json:"method"`
	Port      int    `json:"port"`
	MaxHops   int    `json:"max_hops"`
	Count     int    `json:"count"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (q TraceRequest) Validate() error {
	if q.MaxHops < 1 || q.MaxHops > 30 || q.Count < 1 || q.Count > 5 || q.TimeoutMS < 100 || q.TimeoutMS > 2000 {
		return ErrInvalid
	}
	// Include the fixed 100ms spacing, not just socket timeouts, in the budget.
	n := q.MaxHops * q.Count
	if n*q.TimeoutMS+(n-1)*100 > 120000 {
		return ErrInvalid
	}
	return (Request{Host: q.Host, Family: q.Family, Method: q.Method, Port: q.Port, Count: 1, TimeoutMS: q.TimeoutMS, IntervalMS: 100}).Validate()
}

type TraceSample struct {
	Sequence  int       `json:"sequence"`
	StartedAt time.Time `json:"started_at"`
	Address   *string   `json:"address"`
	RTTMS     *float64  `json:"rtt_ms"`
	Reply     *string   `json:"reply"`
	ICMPType  *int      `json:"icmp_type"`
	ICMPCode  *int      `json:"icmp_code"`
	ErrorCode *string   `json:"error_code"`
}
type TraceHop struct {
	TTL     int           `json:"ttl"`
	Samples []TraceSample `json:"samples"`
}
type TraceResult struct {
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        time.Time  `json:"finished_at"`
	Address           string     `json:"address"`
	ResolvedAddresses []string   `json:"resolved_addresses"`
	Hops              []TraceHop `json:"hops"`
	Reached           bool       `json:"reached"`
	// reached, unreachable, max_hops, or incomplete. A timeout is not proof of loss.
	StopReason string `json:"stop_reason"`
}

type traceReply struct {
	address            netip.Addr
	kind               string
	icmpType, icmpCode *int
	elapsed            time.Duration
	reached, terminal  bool
}
type traceSession interface {
	Probe(context.Context, int, int, time.Duration) (traceReply, error)
	Close() error
}
type traceRunner struct {
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	open   func(context.Context, TraceRequest, netip.Addr) (traceSession, error)
	wait   func(context.Context, time.Duration) error
}

// Trace executes at most 150 fixed probes, with a 125s overall budget including DNS.
// TCP raw probes currently require Linux; no trace invokes an external program.
func Trace(ctx context.Context, q TraceRequest) (TraceResult, error) {
	return (traceRunner{net.DefaultResolver.LookupNetIP, openTraceSession, wait}).run(ctx, q)
}
func (r traceRunner) run(ctx context.Context, q TraceRequest) (result TraceResult, err error) {
	result = TraceResult{StartedAt: time.Now().UTC(), ResolvedAddresses: []string{}, Hops: []TraceHop{}, StopReason: "incomplete"}
	defer func() { result.FinishedAt = time.Now().UTC() }()
	if err = q.Validate(); err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, ErrCancelled
	}
	ctx, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	addresses, err := resolve(ctx, Request{Host: q.Host, Family: q.Family}, r.lookup)
	if err != nil {
		return result, err
	}
	for _, ip := range addresses {
		result.ResolvedAddresses = append(result.ResolvedAddresses, ip.String())
	}
	result.Address = addresses[0].String()
	s, err := r.open(ctx, q, addresses[0])
	if err != nil {
		if ctx.Err() != nil {
			return result, ErrCancelled
		}
		return result, ErrUnavailable
	}
	defer s.Close()
	seq := 0
	for ttl := 1; ttl <= q.MaxHops; ttl++ {
		result.Hops = append(result.Hops, TraceHop{TTL: ttl, Samples: []TraceSample{}})
		hop := &result.Hops[len(result.Hops)-1]
		terminal := false
		for i := 0; i < q.Count; i++ {
			if seq > 0 {
				if err = r.wait(ctx, 100*time.Millisecond); err != nil {
					return result, ErrCancelled
				}
			}
			if ctx.Err() != nil {
				return result, ErrCancelled
			}
			seq++
			sample := TraceSample{Sequence: seq, StartedAt: time.Now().UTC()}
			reply, e := s.Probe(ctx, ttl, seq, time.Duration(q.TimeoutMS)*time.Millisecond)
			if ctx.Err() != nil {
				return result, ErrCancelled
			}
			if e != nil {
				if !errors.Is(e, ErrTimeout) {
					return result, ErrIO
				}
				code := ErrTimeout.Error()
				sample.ErrorCode = &code
			} else {
				if !reply.address.IsValid() || reply.elapsed < 0 {
					return result, ErrIO
				}
				addr, ms, kind := reply.address.String(), float64(reply.elapsed)/float64(time.Millisecond), reply.kind
				sample.Address, sample.RTTMS, sample.Reply = &addr, &ms, &kind
				sample.ICMPType, sample.ICMPCode = reply.icmpType, reply.icmpCode
				result.Reached = result.Reached || reply.reached
				terminal = terminal || reply.terminal
			}
			hop.Samples = append(hop.Samples, sample)
		}
		if result.Reached {
			result.StopReason = "reached"
			return result, nil
		}
		if terminal {
			result.StopReason = "unreachable"
			return result, nil
		}
	}
	result.StopReason = "max_hops"
	return result, nil
}

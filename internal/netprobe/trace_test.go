package netprobe

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func traceRequest() TraceRequest {
	return TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "icmp", MaxHops: 5, Count: 3, TimeoutMS: 100}
}

type fakeTraceSession struct {
	probe  func(int, int) (traceReply, error)
	closed bool
}

func (s *fakeTraceSession) Close() error { s.closed = true; return nil }
func (s *fakeTraceSession) Probe(_ context.Context, ttl, seq int, _ time.Duration) (traceReply, error) {
	return s.probe(ttl, seq)
}

func TestTraceBounds(t *testing.T) {
	for _, change := range []func(*TraceRequest){
		func(q *TraceRequest) { q.MaxHops = 0 }, func(q *TraceRequest) { q.MaxHops = 31 }, func(q *TraceRequest) { q.Count = 0 }, func(q *TraceRequest) { q.Count = 6 },
		func(q *TraceRequest) { q.TimeoutMS = 99 }, func(q *TraceRequest) { q.TimeoutMS = 2001 }, func(q *TraceRequest) { q.MaxHops = 30; q.Count = 5; q.TimeoutMS = 1000 },
		func(q *TraceRequest) { q.Host = "169.254.169.254" }, func(q *TraceRequest) { q.Family = "auto" }, func(q *TraceRequest) { q.Method = "udp" },
		func(q *TraceRequest) { q.Port = 443 }, func(q *TraceRequest) { q.Method = "tcp" }, func(q *TraceRequest) { q.Family = "ipv6" },
	} {
		q := traceRequest()
		change(&q)
		if q.Validate() == nil {
			t.Fatal("accepted", q)
		}
	}
	q := traceRequest()
	q.MaxHops, q.Count, q.TimeoutMS = 30, 3, 1000
	if q.Validate() != nil {
		t.Fatal("default budget rejected")
	}
	q.Method, q.Port = "tcp", 443
	if q.Validate() != nil {
		t.Fatal("tcp rejected")
	}
}

func TestTracePreservesEachRouterAndFinishesTerminalHop(t *testing.T) {
	q := traceRequest()
	calls, waits := 0, 0
	s := &fakeTraceSession{probe: func(ttl, seq int) (traceReply, error) {
		calls++
		if seq != calls || ttl != (seq-1)/q.Count+1 {
			t.Fatal(ttl, seq)
		}
		if seq == 2 {
			return traceReply{}, ErrTimeout
		}
		ip := netip.MustParseAddr("10.0.0.1")
		if seq == 3 {
			ip = netip.MustParseAddr("10.0.0.2")
		}
		kind := "time_exceeded"
		if ttl == 2 {
			ip = netip.MustParseAddr("1.1.1.1")
			kind = "echo_reply"
		}
		return traceReply{address: ip, kind: kind, elapsed: 12 * time.Millisecond, reached: ttl == 2, terminal: ttl == 2}, nil
	}}
	r := traceRunner{open: func(context.Context, TraceRequest, netip.Addr) (traceSession, error) { return s, nil }, wait: func(_ context.Context, d time.Duration) error {
		waits++
		if d != 100*time.Millisecond {
			t.Fatal(d)
		}
		return nil
	}}
	result, err := r.run(context.Background(), q)
	if err != nil || result.StopReason != "reached" || !result.Reached || calls != 6 || waits != 5 || !s.closed || len(result.Hops) != 2 {
		t.Fatal(result, err, calls, waits)
	}
	first := result.Hops[0].Samples
	if *first[0].Address != "10.0.0.1" || *first[2].Address != "10.0.0.2" || first[1].Address != nil || first[1].RTTMS != nil || first[1].Reply != nil || *first[1].ErrorCode != ErrTimeout.Error() {
		t.Fatal(first)
	}
	if result.Address != "1.1.1.1" || result.FinishedAt.Before(result.StartedAt) {
		t.Fatal(result)
	}
}

func TestTraceUnknownUnreachableAndLocalFailure(t *testing.T) {
	for _, mode := range []string{"timeout", "unreachable", "io", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			q := traceRequest()
			calls := 0
			s := &fakeTraceSession{probe: func(ttl, seq int) (traceReply, error) {
				calls++
				switch mode {
				case "timeout":
					return traceReply{}, ErrTimeout
				case "io":
					return traceReply{}, errors.New("private socket error")
				case "cancel":
					cancel()
					return traceReply{}, ErrIO
				default:
					return traceReply{address: netip.MustParseAddr("10.0.0.1"), kind: "destination_unreachable", terminal: true}, nil
				}
			}}
			r := traceRunner{open: func(context.Context, TraceRequest, netip.Addr) (traceSession, error) { return s, nil }, wait: func(context.Context, time.Duration) error { return nil }}
			result, err := r.run(ctx, q)
			if result.Reached || !s.closed {
				t.Fatal(result, s.closed)
			}
			switch mode {
			case "timeout":
				if err != nil || result.StopReason != "max_hops" || calls != 15 {
					t.Fatal(result, err)
				}
			case "unreachable":
				if err != nil || result.StopReason != "unreachable" || calls != 3 {
					t.Fatal(result, err)
				}
			case "io":
				if err != ErrIO || result.StopReason != "incomplete" {
					t.Fatal(result, err)
				}
			case "cancel":
				if err != ErrCancelled || result.StopReason != "incomplete" {
					t.Fatal(result, err)
				}
			}
		})
	}
}

func TestTraceDNSPinningAndMixedAnswerRejection(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		q := traceRequest()
		q.Host = "probe.example"
		lookups, opens := 0, 0
		s := &fakeTraceSession{probe: func(int, int) (traceReply, error) { return traceReply{}, ErrTimeout }}
		r := traceRunner{
			lookup: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				lookups++
				if network != "ip" || host != "probe.example" {
					t.Fatal(network, host)
				}
				deadline, _ := ctx.Deadline()
				if time.Until(deadline) > 5*time.Second {
					t.Fatal("DNS unbounded")
				}
				answer := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}
				if mixed {
					answer = append(answer, netip.MustParseAddr("fd00::1"))
				}
				return answer, nil
			},
			open: func(_ context.Context, _ TraceRequest, ip netip.Addr) (traceSession, error) {
				opens++
				if ip.String() != "1.1.1.1" {
					t.Fatal(ip)
				}
				return s, nil
			},
			wait: func(context.Context, time.Duration) error { return nil },
		}
		result, err := r.run(context.Background(), q)
		if lookups != 1 {
			t.Fatal(lookups)
		}
		if mixed {
			if err != ErrTarget || opens != 0 {
				t.Fatal(result, err, opens)
			}
		} else if err != nil || opens != 1 || len(result.ResolvedAddresses) != 2 {
			t.Fatal(result, err, opens)
		}
	}
}

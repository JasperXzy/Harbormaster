package netprobe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func request() Request {
	return Request{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, Count: 5, TimeoutMS: 1000, IntervalMS: 100}
}

type fakeSession struct {
	times  []time.Duration
	errors []error
	calls  int
	closed bool
}

func (s *fakeSession) Close() error { s.closed = true; return nil }
func (s *fakeSession) Probe(_ context.Context, seq int, _ time.Duration) (time.Duration, error) {
	s.calls++
	if seq != s.calls {
		panic("sequence")
	}
	return s.times[seq-1], s.errors[seq-1]
}
func TestRunSummariesAndLossBreakJitterPairs(t *testing.T) {
	q := request()
	q.Count = 6
	s := &fakeSession{times: []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 0, 100 * time.Millisecond, 140 * time.Millisecond, 0}, errors: []error{nil, nil, fmt.Errorf("private detail: %w", ErrTimeout), nil, nil, ErrRefused}}
	opens, waits := 0, 0
	r := runner{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("literal resolved")
		return nil, nil
	}, open: func(_ context.Context, got Request, ip netip.Addr) (session, error) {
		opens++
		if got != q || ip.String() != "1.1.1.1" {
			t.Fatal(got, ip)
		}
		return s, nil
	}, wait: func(context.Context, time.Duration) error { waits++; return nil }}
	result, err := r.run(context.Background(), q)
	if err != nil || opens != 1 || waits != 5 || !s.closed || len(result.Samples) != 6 || result.Summary == nil {
		t.Fatal(result, err)
	}
	stats := result.Summary
	if stats.Attempts != 6 || stats.Succeeded != 4 || *stats.MinimumMS != 10 || *stats.MaximumMS != 140 || *stats.AverageMS != 70 || *stats.JitterMS != 30 || math.Abs(stats.LossPercent-100.0/3) > 0.000001 {
		t.Fatal(stats)
	}
	if result.Samples[2].RTTMS != nil || *result.Samples[2].ErrorCode != "probe_timeout" || result.Samples[0].ErrorCode != nil || result.FinishedAt.Before(result.StartedAt) {
		t.Fatal(result)
	}
}
func TestNoResponsesHasNullRTTAndNoAdjacentSuccessHasNullJitter(t *testing.T) {
	zero := 0.0
	ten := 10.0
	for _, tc := range []struct {
		samples  []Sample
		received int
		mean     *float64
	}{
		{[]Sample{{}, {}, {}}, 0, nil},
		{[]Sample{{RTTMS: &zero}, {}, {RTTMS: &ten}}, 2, new(float64(5))},
	} {
		got := summarize(tc.samples)
		if got.Succeeded != tc.received || got.JitterMS != nil {
			t.Fatal(got)
		}
		if tc.mean == nil {
			if got.AverageMS != nil || got.MinimumMS != nil || got.MaximumMS != nil || got.LossPercent != 100 {
				t.Fatal(got)
			}
		} else if got.AverageMS == nil || *got.AverageMS != *tc.mean {
			t.Fatal(got)
		}
	}
}
func TestIncompleteRunsDoNotFabricateLossOrExposeRawError(t *testing.T) {
	for _, mode := range []string{"cancelled_before", "local_failure", "socket_failure", "cancelled_between"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			q := request()
			s := &fakeSession{times: []time.Duration{time.Millisecond, 0}, errors: []error{nil, errors.New("raw secret address")}}
			opens := 0
			r := runner{open: func(context.Context, Request, netip.Addr) (session, error) {
				opens++
				if mode == "socket_failure" {
					return nil, errors.New("raw secret address")
				}
				return s, nil
			}, wait: func(context.Context, time.Duration) error {
				if mode == "cancelled_between" {
					cancel()
					return ctx.Err()
				}
				return nil
			}}
			if mode == "cancelled_before" {
				cancel()
			}
			out, err := r.run(ctx, q)
			if err == nil || strings.Contains(err.Error(), "secret") || out.Summary != nil {
				t.Fatal(out, err)
			}
			if mode == "cancelled_before" && opens != 0 {
				t.Fatal("cancelled opened socket")
			}
			if mode == "local_failure" || mode == "cancelled_between" {
				if !s.closed || len(out.Samples) != 1 {
					t.Fatal(out, s)
				}
			}
		})
	}
}
func TestRequestLimitsRejectBeforeAnyIO(t *testing.T) {
	for _, mutate := range []func(*Request){
		func(q *Request) { q.Count = 0 }, func(q *Request) { q.Count = 21 }, func(q *Request) { q.TimeoutMS = 99 }, func(q *Request) { q.TimeoutMS = 5001 }, func(q *Request) { q.IntervalMS = 0 }, func(q *Request) { q.IntervalMS = 5001 },
		func(q *Request) { q.Count = 20; q.TimeoutMS = 5000 }, func(q *Request) { q.Method = "exec" }, func(q *Request) { q.Port = 0 }, func(q *Request) { q.Port = 65536 }, func(q *Request) { q.Method = "icmp" }, func(q *Request) { q.Family = "auto" },
		func(q *Request) { q.Host = "http://1.1.1.1" }, func(q *Request) { q.Host = "100.100.100.200" }, func(q *Request) { q.Host = "1.1.1.1:443" },
	} {
		q := request()
		mutate(&q)
		r := runner{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			t.Fatal("invalid reached DNS")
			return nil, nil
		}, open: func(context.Context, Request, netip.Addr) (session, error) {
			t.Fatal("invalid opened socket")
			return nil, nil
		}}
		if _, err := r.run(context.Background(), q); err == nil {
			t.Fatal(q)
		}
	}
}
func TestResolutionPinsAddressRejectsMixedAndDoesNotLeakResolverErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []string
		want error
	}{
		{"public", []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"}, nil},
		{"mapped_public", []string{"::ffff:1.1.1.1", "8.8.8.8"}, nil},
		{"mapped_metadata", []string{"::ffff:100.100.100.200"}, ErrTarget},
		{"private4", []string{"1.1.1.1", "10.0.0.1"}, ErrTarget},
		{"private6", []string{"1.1.1.1", "fd00::1"}, ErrTarget},
		{"metadata", []string{"100.100.100.200"}, ErrTarget},
		{"wrong_family", []string{"2606:4700:4700::1111"}, ErrDNS},
		{"empty", nil, ErrDNS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := request()
			q.Host = "Cloudflare.COM."
			q.Count = 2
			dns, opens := 0, 0
			s := &fakeSession{times: []time.Duration{time.Millisecond, time.Millisecond}, errors: []error{nil, nil}}
			r := runner{lookup: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				dns++
				if network != "ip" || host != "cloudflare.com" {
					t.Fatal(network, host)
				}
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("unbounded DNS")
				}
				ips := []netip.Addr{}
				for _, raw := range tc.ips {
					ips = append(ips, netip.MustParseAddr(raw))
				}
				return ips, nil
			}, open: func(_ context.Context, _ Request, ip netip.Addr) (session, error) {
				opens++
				if ip.String() != "1.1.1.1" {
					t.Fatal(ip)
				}
				return s, nil
			}, wait: func(context.Context, time.Duration) error { return nil }}
			result, err := r.run(context.Background(), q)
			if !errors.Is(err, tc.want) || dns != 1 {
				t.Fatal(result, err, dns)
			}
			if tc.want != nil && opens != 0 {
				t.Fatal("rejected address probed")
			}
			if tc.want == nil && (opens != 1 || len(result.ResolvedAddresses) != 2 || result.Address != "1.1.1.1") {
				t.Fatal(result)
			}
		})
	}
	q := request()
	q.Host = "example.com"
	_, err := resolve(context.Background(), q, func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("resolver private details")
	})
	if err != ErrDNS {
		t.Fatal(err)
	}
}
func TestPublicPolicyAndHostSyntax(t *testing.T) {
	for _, raw := range []string{"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.100.100.200", "127.0.0.1", "169.254.169.254", "172.31.0.1", "192.168.1.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "::", "::1", "::ffff:1.1.1.1", "64:ff9b::a00:1", "2001::1", "2001:db8::1", "2002:7f00:1::1", "3fff::1", "fc00::1", "fe80::1", "ff02::1", "2606:4700:4700::1111%eth0"} {
		if PublicAddress(netip.MustParseAddr(raw)) {
			t.Fatal("non-public accepted", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "223.5.5.5", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !PublicAddress(netip.MustParseAddr(raw)) {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"", " ", "example.com/path", "user@example.com", "example.com?token=secret", "[::1]", "a..example", "-a.example", "a-.example", "bad_name.example", "a\n.example", strings.Repeat("a", 64) + ".example"} {
		if _, err := normalizeHost(raw); err == nil {
			t.Fatal(raw)
		}
	}
	if got, err := normalizeHost("例子.测试"); err != nil || got != "xn--fsqu00a.xn--0zwm56d" {
		t.Fatal(got, err)
	}
}

// Package netprobe executes only built-in ICMP echo and TCP connect probes.
// It never invokes a process, sends application data, or accepts a script.
package netprobe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid_probe")
	ErrTarget      = errors.New("probe_target_not_public")
	ErrDNS         = errors.New("probe_dns_failed")
	ErrUnavailable = errors.New("probe_unavailable")
	ErrIO          = errors.New("probe_io_failed")
	ErrTimeout     = errors.New("probe_timeout")
	ErrRefused     = errors.New("probe_connection_refused")
	ErrUnreachable = errors.New("probe_network_unreachable")
	ErrCancelled   = errors.New("probe_cancelled")
)

// Limits bound one task independently of its future scheduling frequency.
// Family is ipv4 or ipv6: different address families never share one series.
type Request struct {
	Host       string `json:"host"`
	Family     string `json:"family"`
	Method     string `json:"method"`
	Port       int    `json:"port"`
	Count      int    `json:"count"`
	TimeoutMS  int    `json:"timeout_ms"`
	IntervalMS int    `json:"interval_ms"`
}

func (r Request) Validate() error {
	if r.Family != "ipv4" && r.Family != "ipv6" || r.Count < 1 || r.Count > 20 || r.TimeoutMS < 100 || r.TimeoutMS > 5000 || r.IntervalMS < 100 || r.IntervalMS > 5000 || r.Count*r.TimeoutMS+(r.Count-1)*r.IntervalMS > 60000 {
		return ErrInvalid
	}
	if r.Method != "icmp" && r.Method != "tcp" || r.Method == "tcp" && (r.Port < 1 || r.Port > 65535) || r.Method == "icmp" && r.Port != 0 {
		return ErrInvalid
	}
	_, err := normalizeHost(r.Host)
	return err
}

type Sample struct {
	Sequence  int       `json:"sequence"`
	StartedAt time.Time `json:"started_at"`
	RTTMS     *float64  `json:"rtt_ms"`
	ErrorCode *string   `json:"error_code"`
}
type Summary struct {
	Attempts    int      `json:"attempts"`
	Succeeded   int      `json:"succeeded"`
	LossPercent float64  `json:"loss_percent"`
	MinimumMS   *float64 `json:"minimum_ms"`
	MaximumMS   *float64 `json:"maximum_ms"`
	AverageMS   *float64 `json:"average_ms"`
	JitterMS    *float64 `json:"jitter_ms"`
}
type Result struct {
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	Address           string    `json:"address"`
	ResolvedAddresses []string  `json:"resolved_addresses"`
	Samples           []Sample  `json:"samples"`
	Summary           *Summary  `json:"summary"`
}

type session interface {
	Probe(context.Context, int, time.Duration) (time.Duration, error)
	Close() error
}
type runner struct {
	lookup func(context.Context, string, string) ([]netip.Addr, error)
	open   func(context.Context, Request, netip.Addr) (session, error)
	wait   func(context.Context, time.Duration) error
}

func Run(ctx context.Context, request Request) (Result, error) {
	return (runner{lookup: net.DefaultResolver.LookupNetIP, open: openSession, wait: wait}).run(ctx, request)
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (r runner) run(ctx context.Context, q Request) (result Result, err error) {
	result = Result{StartedAt: time.Now().UTC(), ResolvedAddresses: []string{}, Samples: []Sample{}}
	defer func() { result.FinishedAt = time.Now().UTC() }()
	if err = q.Validate(); err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, ErrCancelled
	}
	// DNS has its own five-second budget. The complete task also has a hard cap.
	ctx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	addresses, err := resolve(ctx, q, r.lookup)
	if err != nil {
		return result, err
	}
	for _, ip := range addresses {
		result.ResolvedAddresses = append(result.ResolvedAddresses, ip.String())
	}
	ip := addresses[0]
	result.Address = ip.String()
	conn, err := r.open(ctx, q, ip)
	if err != nil {
		if ctx.Err() != nil {
			return result, ErrCancelled
		}
		return result, ErrUnavailable
	}
	defer conn.Close()
	for i := 1; i <= q.Count; i++ {
		if ctx.Err() != nil {
			return result, ErrCancelled
		}
		sample := Sample{Sequence: i, StartedAt: time.Now().UTC()}
		elapsed, err := conn.Probe(ctx, i, time.Duration(q.TimeoutMS)*time.Millisecond)
		if ctx.Err() != nil {
			return result, ErrCancelled
		}
		if err != nil {
			// Only completed network attempts contribute to loss. Local socket/setup
			// failures and cancellation leave the task incomplete, with no summary.
			code := ""
			switch {
			case errors.Is(err, ErrTimeout):
				code = ErrTimeout.Error()
			case errors.Is(err, ErrRefused):
				code = ErrRefused.Error()
			case errors.Is(err, ErrUnreachable):
				code = ErrUnreachable.Error()
			default:
				return result, ErrIO
			}
			sample.ErrorCode = &code
		} else {
			if elapsed < 0 {
				return result, ErrIO
			}
			ms := float64(elapsed) / float64(time.Millisecond)
			sample.RTTMS = &ms
		}
		result.Samples = append(result.Samples, sample)
		if i < q.Count {
			if err := r.wait(ctx, time.Duration(q.IntervalMS)*time.Millisecond); err != nil {
				return result, ErrCancelled
			}
		}
	}
	summary := summarize(result.Samples)
	result.Summary = &summary
	return result, nil
}
func summarize(samples []Sample) Summary {
	s := Summary{Attempts: len(samples)}
	var sum, jitter float64
	pairs := 0
	var previous *float64
	for _, sample := range samples {
		if sample.RTTMS == nil {
			previous = nil
			continue
		}
		value := *sample.RTTMS
		s.Succeeded++
		sum += value
		if s.MinimumMS == nil || value < *s.MinimumMS {
			v := value
			s.MinimumMS = &v
		}
		if s.MaximumMS == nil || value > *s.MaximumMS {
			v := value
			s.MaximumMS = &v
		}
		if previous != nil {
			delta := value - *previous
			if delta < 0 {
				delta = -delta
			}
			jitter += delta
			pairs++
		}
		previous = sample.RTTMS
	}
	if s.Attempts > 0 {
		s.LossPercent = 100 * float64(s.Attempts-s.Succeeded) / float64(s.Attempts)
	}
	if s.Succeeded > 0 {
		mean := sum / float64(s.Succeeded)
		s.AverageMS = &mean
	}
	if pairs > 0 {
		mean := jitter / float64(pairs)
		s.JitterMS = &mean
	}
	return s
}

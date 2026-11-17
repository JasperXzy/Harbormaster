// Package metering calculates billing-object traffic from bounded sample
// intervals. It has no database, network, clock or cloud-action dependencies.
package metering

import (
	"errors"
	"math/big"
	"sort"
	"time"
)

var ErrInput = errors.New("invalid_metering_input")
var ErrOverflow = errors.New("metering_overflow")
var ErrBandwidthUnknown = errors.New("metering_bandwidth_unknown")

const (
	Outbound       = "outbound"
	HourlyDominant = "hourly_dominant"
)

type Rate struct{ RX, TX int64 }

type Interval struct {
	Start, End time.Time
	RX, TX     int64
	DurationMS int64
	Gap        bool
	Projection *Rate
}

// From/Until describe a known membership interval, not the first time a
// resource happened to appear in discovery. Zero means the requested window.
type Member struct {
	ID          string
	From, Until time.Time
	Intervals   []Interval
}

type WindowResult struct {
	Bytes        *int64
	KnownBytes   int64
	WorstCase    int64
	Complete     bool
	Conservative bool
	Reasons      []string
}

type bucket struct{ rx, tx, actualRX, actualTX int64 }

func add(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > int64(^uint64(0)>>1)-b {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// CeilProduct never wraps or rounds a positive safety allowance down.
func CeilProduct(value, numerator, denominator int64) (int64, error) {
	if value < 0 || numerator < 0 || denominator <= 0 {
		return 0, ErrInput
	}
	v := new(big.Int).Mul(big.NewInt(value), big.NewInt(numerator))
	v.Add(v, big.NewInt(denominator-1)).Quo(v, big.NewInt(denominator))
	if !v.IsInt64() {
		return 0, ErrOverflow
	}
	return v.Int64(), nil
}

func validWindow(from, until time.Time) bool {
	return !from.IsZero() && until.After(from) && until.Sub(from) <= 32*24*time.Hour
}

func validInterval(v Interval) bool {
	return !v.Start.IsZero() && v.End.After(v.Start) && v.RX >= 0 && v.TX >= 0 && v.DurationMS >= 0 &&
		(v.Projection == nil || v.RX == 0 && v.TX == 0 && v.Projection.RX >= 0 && v.Projection.TX >= 0)
}

func amount(v Interval, start, end time.Time) (int64, int64, error) {
	if v.Projection == nil {
		return v.RX, v.TX, nil
	}
	rx, err := CeilProduct(v.Projection.RX, end.Sub(start).Nanoseconds(), int64(time.Second))
	if err != nil {
		return 0, 0, err
	}
	tx, err := CeilProduct(v.Projection.TX, end.Sub(start).Nanoseconds(), int64(time.Second))
	return rx, tx, err
}

// Accumulator consumes each member's intervals in chronological order. Its
// memory is bounded by members plus hours, independent of raw sample count.
type Accumulator struct {
	rule          string
	from, until   time.Time
	out           WindowResult
	reasons       map[string]bool
	buckets       map[time.Time]bucket
	members       map[string]*coverage
	total, actual int64
}
type coverage struct{ start, end, cursor, previous time.Time }

func NewAccumulator(rule string, from, until time.Time) (*Accumulator, error) {
	if !validWindow(from, until) || rule != Outbound && rule != HourlyDominant {
		return nil, ErrInput
	}
	a := &Accumulator{rule: rule, from: from, until: until, out: WindowResult{Complete: true, Reasons: []string{}}, reasons: map[string]bool{}, buckets: map[time.Time]bucket{}, members: map[string]*coverage{}}
	if rule == HourlyDominant && !from.Equal(from.UTC().Truncate(time.Hour)) {
		a.out.Conservative = true
		a.reasons["partial_hour_upper"] = true
	}
	return a, nil
}
func (a *Accumulator) AddMember(id string, from, until time.Time) error {
	if id == "" || a.members[id] != nil || !from.IsZero() && !until.IsZero() && until.Before(from) {
		return ErrInput
	}
	start, end := a.from, a.until
	if !from.IsZero() {
		start = later(start, from)
	}
	if !until.IsZero() {
		end = earlier(end, until)
	}
	a.members[id] = &coverage{start: start, end: end, cursor: start}
	return nil
}

// Add includes all observed counts at a clipped cutoff/hour boundary: exact
// allocation is unavailable. Constant-rate projections are split by duration.
func (a *Accumulator) Add(id string, v Interval) error {
	c := a.members[id]
	if c == nil || !validInterval(v) || !c.previous.IsZero() && v.Start.Before(c.previous) {
		return ErrInput
	}
	c.previous = v.End
	start, end := later(c.start, v.Start), earlier(c.end, v.End)
	if !end.After(start) {
		return nil
	}
	if start.After(c.cursor) {
		a.out.Complete = false
		a.reasons["coverage_gap"] = true
	}
	c.cursor = end
	if v.Gap {
		a.reasons["sample_gap"] = true
		a.out.Conservative = true
	}
	if v.Projection != nil {
		a.reasons["worst_case_projection"] = true
	}
	if v.Projection == nil && (!start.Equal(v.Start) || !end.Equal(v.End)) {
		a.out.Conservative = true
		a.reasons["boundary_upper_allocation"] = true
	}
	if a.rule == Outbound {
		_, tx, err := amount(v, start, end)
		if err != nil {
			return err
		}
		if a.total, err = add(a.total, tx); err != nil {
			return err
		}
		if v.Projection == nil {
			a.actual, err = add(a.actual, tx)
		}
		return err
	}
	if v.Projection == nil && !start.UTC().Truncate(time.Hour).Equal(end.Add(-time.Nanosecond).UTC().Truncate(time.Hour)) {
		a.out.Conservative = true
		a.reasons["boundary_upper_allocation"] = true
	}
	for hour := start.UTC().Truncate(time.Hour); hour.Before(end); hour = hour.Add(time.Hour) {
		rx, tx, err := amount(v, later(start, hour), earlier(end, hour.Add(time.Hour)))
		if err != nil {
			return err
		}
		b := a.buckets[hour]
		if b.rx, err = add(b.rx, rx); err != nil {
			return err
		}
		if b.tx, err = add(b.tx, tx); err != nil {
			return err
		}
		if v.Projection == nil {
			if b.actualRX, err = add(b.actualRX, rx); err != nil {
				return err
			}
			if b.actualTX, err = add(b.actualTX, tx); err != nil {
				return err
			}
		}
		a.buckets[hour] = b
	}
	return nil
}
func (a *Accumulator) Result() (WindowResult, error) {
	out := a.out
	if len(a.members) == 0 {
		return out, ErrInput
	}
	total, actual := a.total, a.actual
	for _, c := range a.members {
		if c.cursor.Before(c.end) {
			out.Complete = false
			a.reasons["coverage_gap"] = true
		}
	}
	if a.rule == HourlyDominant {
		for _, b := range a.buckets {
			var err error
			if total, err = add(total, max(b.rx, b.tx)); err != nil {
				return out, err
			}
			if actual, err = add(actual, max(b.actualRX, b.actualTX)); err != nil {
				return out, err
			}
		}
	}
	out.KnownBytes, out.WorstCase = total, total-actual
	if out.Complete {
		out.Bytes = &total
	}
	for reason := range a.reasons {
		out.Reasons = append(out.Reasons, reason)
	}
	sort.Strings(out.Reasons)
	return out, nil
}

// Window is the slice convenience API. Missing coverage keeps Bytes nil rather
// than silently adding zero; shared directions are combined before hourly max.
func Window(rule string, members []Member, from, until time.Time) (WindowResult, error) {
	a, err := NewAccumulator(rule, from, until)
	if err != nil {
		return WindowResult{}, err
	}
	for _, m := range members {
		if err = a.AddMember(m.ID, m.From, m.Until); err != nil {
			return WindowResult{}, err
		}
		items := append([]Interval(nil), m.Intervals...)
		sort.Slice(items, func(i, j int) bool { return items[i].Start.Before(items[j].Start) })
		for _, v := range items {
			if err = a.Add(m.ID, v); err != nil {
				return WindowResult{}, err
			}
		}
	}
	return a.Result()
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

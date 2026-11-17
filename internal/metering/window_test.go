package metering

import (
	"errors"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

var hour = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

func observed(from, until time.Time, rx, tx int64) Interval {
	return Interval{Start: from, End: until, RX: rx, TX: tx}
}

func TestDominantAggregatesMembersBeforeMaxAndSeparatesHours(t *testing.T) {
	members := []Member{
		{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 100, 0), observed(hour.Add(time.Hour), hour.Add(2*time.Hour), 0, 300)}},
		{ID: "b", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 0, 100), observed(hour.Add(time.Hour), hour.Add(2*time.Hour), 0, 0)}},
	}
	for _, tc := range []struct {
		rule string
		want int64
	}{{HourlyDominant, 400}, {Outbound, 400}} {
		got, err := Window(tc.rule, members, hour, hour.Add(2*time.Hour))
		if err != nil || got.Bytes == nil || *got.Bytes != tc.want || !got.Complete || got.Conservative || got.WorstCase != 0 {
			t.Fatal(got, err)
		}
	}
	// A per-member max would be 500; a whole-window max would be 400 here.
	members = []Member{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 100, 0), observed(hour.Add(time.Hour), hour.Add(2*time.Hour), 0, 100)}}}
	got, err := Window(HourlyDominant, members, hour, hour.Add(2*time.Hour))
	if err != nil || got.Bytes == nil || *got.Bytes != 200 {
		t.Fatal(got, err)
	}
}

func TestBoundaryIsAnUpperAllocationNotProrated(t *testing.T) {
	start, end := hour.Add(55*time.Minute), hour.Add(65*time.Minute)
	got, err := Window(HourlyDominant, []Member{{ID: "a", Intervals: []Interval{observed(start, end, 100, 40)}}}, start, end)
	if err != nil || got.Bytes == nil || *got.Bytes != 200 || !got.Conservative || !slices.Contains(got.Reasons, "boundary_upper_allocation") {
		t.Fatal(got, err)
	}
	got, err = Window(Outbound, []Member{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 0, 100)}}}, hour.Add(30*time.Minute), hour.Add(time.Hour))
	if err != nil || got.Bytes == nil || *got.Bytes != 100 || !got.Conservative {
		t.Fatal("cutoff must not invent uniform traffic", got, err)
	}
	// For every feasible allocation to the two hours, the upper value must
	// cover sum(max(hourRX, hourTX)), including opposing direction changes.
	random := rand.New(rand.NewSource(11))
	for range 500 {
		rx, tx := random.Int63n(10000), random.Int63n(10000)
		rxFirst, txFirst := random.Int63n(rx+1), random.Int63n(tx+1)
		got, err := Window(HourlyDominant, []Member{{ID: "a", Intervals: []Interval{observed(start, end, rx, tx)}}}, start, end)
		actual := max(rxFirst, txFirst) + max(rx-rxFirst, tx-txFirst)
		if err != nil || got.Bytes == nil || *got.Bytes < actual {
			t.Fatal(actual, got, err)
		}
	}
}

func TestMissingCoverageIsNotZeroAndRealZeroIsKnown(t *testing.T) {
	members := []Member{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 0, 100)}}, {ID: "b"}}
	got, err := Window(HourlyDominant, members, hour, hour.Add(time.Hour))
	if err != nil || got.Complete || got.Bytes != nil || got.KnownBytes != 100 || !slices.Contains(got.Reasons, "coverage_gap") {
		t.Fatal(got, err)
	}
	got, err = Window(Outbound, []Member{{ID: "zero", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 0, 0)}}}, hour, hour.Add(time.Hour))
	if err != nil || !got.Complete || got.Bytes == nil || *got.Bytes != 0 {
		t.Fatal(got, err)
	}
	got, err = Window(Outbound, []Member{{ID: "joined", From: hour.Add(30 * time.Minute), Intervals: []Interval{observed(hour.Add(30*time.Minute), hour.Add(time.Hour), 0, 50)}}}, hour, hour.Add(time.Hour))
	if err != nil || got.Bytes == nil || *got.Bytes != 50 {
		t.Fatal(got, err)
	}
}

func TestProjectionSplitsByDurationAndRecoveryCanReplaceIt(t *testing.T) {
	start, end := hour.Add(55*time.Minute), hour.Add(65*time.Minute)
	projected := []Member{{ID: "a", Intervals: []Interval{{Start: start, End: end, Projection: &Rate{RX: 10}}}}}
	got, err := Window(HourlyDominant, projected, start, end)
	if err != nil || got.Bytes == nil || *got.Bytes != 6000 || got.WorstCase != 6000 {
		t.Fatal(got, err)
	}
	// Once a real cumulative interval arrives it replaces the projection;
	// it is not added to an ever-growing extrapolation ledger.
	got, err = Window(HourlyDominant, []Member{{ID: "a", Intervals: []Interval{observed(start, end, 100, 40)}}}, start, end)
	if err != nil || got.Bytes == nil || *got.Bytes != 200 || got.WorstCase != 0 {
		t.Fatal(got, err)
	}
	got, err = Window(HourlyDominant, []Member{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 100, 0)}}, {ID: "b", Intervals: []Interval{{Start: hour, End: hour.Add(time.Hour), Projection: &Rate{TX: 1}}}}}, hour, hour.Add(time.Hour))
	if err != nil || got.Bytes == nil || *got.Bytes != 3600 || got.WorstCase != 3500 {
		t.Fatal("projection contribution must respect the shared max", got, err)
	}
}

func TestInvalidOrOverflowingMeteringCannotBecomeAValidEstimate(t *testing.T) {
	for _, members := range [][]Member{
		{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), -1, 0)}}},
		{{ID: "a"}, {ID: "a"}},
		{{ID: "a", From: hour.Add(time.Hour), Until: hour}},
		{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 1, 0), observed(hour.Add(time.Minute), hour.Add(2*time.Hour), 1, 0)}}},
		{{ID: "a", Intervals: []Interval{{Start: hour, End: hour.Add(time.Hour), RX: 1, Projection: &Rate{TX: 1}}}}},
	} {
		if _, err := Window(HourlyDominant, members, hour, hour.Add(time.Hour)); !errors.Is(err, ErrInput) {
			t.Fatal(err)
		}
	}
	_, err := Window(Outbound, []Member{{ID: "a", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 0, math.MaxInt64)}}, {ID: "b", Intervals: []Interval{observed(hour, hour.Add(time.Hour), 0, 1)}}}, hour, hour.Add(time.Hour))
	if !errors.Is(err, ErrOverflow) {
		t.Fatal(err)
	}
	if v, err := CeilProduct(math.MaxInt64, math.MaxInt64, math.MaxInt64); err != nil || v != math.MaxInt64 {
		t.Fatal(v, err)
	}
	if v, err := CeilProduct(1, 1, 3); err != nil || v != 1 {
		t.Fatal(v, err)
	}
}

func TestOfflinePeakNeedsAFullContinuousLookback(t *testing.T) {
	items := []Interval{}
	for i := 0; i < 60; i++ {
		items = append(items, observed(hour.Add(time.Duration(i)*10*time.Second), hour.Add(time.Duration(i+1)*10*time.Second), 101, 149))
	}
	cap := int64(8)
	rate, source, err := OfflineRate(items, hour.Add(10*time.Minute), 10*time.Minute, &cap, false)
	if err != nil || source != "observed_peak" || rate.RX != 11 || rate.TX != 15 {
		t.Fatal(rate, source, err)
	}
	for _, change := range []func([]Interval) []Interval{
		func(v []Interval) []Interval { return v[1:] },
		func(v []Interval) []Interval { v[3].Gap = true; return v },
		func(v []Interval) []Interval {
			v[3] = Interval{Start: v[3].Start, End: v[3].End, Projection: &Rate{RX: 1}}
			return v
		},
	} {
		rate, source, err = OfflineRate(change(append([]Interval(nil), items...)), hour.Add(10*time.Minute), 10*time.Minute, &cap, false)
		if err != nil || source != "public_bandwidth" || rate.RX != 1_000_000 || rate.TX != 1_000_000 {
			t.Fatal(rate, source, err)
		}
	}
	if _, _, err := OfflineRate(items[:1], hour.Add(10*time.Minute), 10*time.Minute, nil, false); !errors.Is(err, ErrBandwidthUnknown) {
		t.Fatal(err)
	}
	rate, source, err = OfflineRate(items, hour.Add(10*time.Minute), 10*time.Minute, &cap, true)
	if err != nil || source != "public_bandwidth" || rate.TX != 1_000_000 {
		t.Fatal(rate, source, err)
	}
	items[3].DurationMS = 20_000
	items = items[3:4]
	rate, source, err = OfflineRate(items, items[0].End, 10*time.Second, nil, false)
	if err != nil || source != "observed_peak" || rate.RX != 6 || rate.TX != 8 {
		t.Fatal("rate must use monotonic duration", rate, source, err)
	}
}

func TestThresholdUsesMaxAndReserveWithoutOverflow(t *testing.T) {
	hit, total, err := Threshold(120, 100, 1, 60, 180)
	if err != nil || !hit || total != 180 {
		t.Fatal(hit, total, err)
	}
	hit, total, err = Threshold(100, 120, 1, 60, 181)
	if err != nil || hit || total != 180 {
		t.Fatal(hit, total, err)
	}
	if _, _, err = Threshold(math.MaxInt64, 0, 1, 1, 1); !errors.Is(err, ErrOverflow) {
		t.Fatal(err)
	}
}

// A long provider outage must not require keeping a month of raw samples in
// memory. Streaming also retains the same coverage and shared-direction rules.
func TestAccumulatorMonth(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	until := from.Add(31 * 24 * time.Hour)
	a, err := NewAccumulator(HourlyDominant, from, until)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"in", "out"} {
		if err = a.AddMember(id, time.Time{}, time.Time{}); err != nil {
			t.Fatal(err)
		}
		for at := from; at.Before(until); at = at.Add(10 * time.Second) {
			v := Interval{Start: at, End: at.Add(10 * time.Second), RX: 100, TX: 10}
			if id == "out" {
				v.RX, v.TX = v.TX, v.RX
			}
			if err = a.Add(id, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := a.Result()
	want := int64(31 * 24 * 360 * 110)
	if err != nil || got.Bytes == nil || *got.Bytes != want || !got.Complete {
		t.Fatalf("%+v %v", got, err)
	}
}

package metering

import (
	"sort"
	"time"
)

// OfflineRate follows PRD 6.2: an uninterrupted full lookback uses its observed
// peak; incomplete history or explicit bandwidth mode uses the public ceiling.
// It is the configured heuristic, not a guarantee about future traffic.
func OfflineRate(intervals []Interval, at time.Time, lookback time.Duration, bandwidthMbps *int64, alwaysBandwidth bool) (Rate, string, error) {
	fallback := func() (Rate, string, error) {
		if bandwidthMbps == nil || *bandwidthMbps <= 0 {
			return Rate{}, "", ErrBandwidthUnknown
		}
		value, err := CeilProduct(*bandwidthMbps, 1_000_000, 8)
		return Rate{value, value}, "public_bandwidth", err
	}
	if at.IsZero() || lookback <= 0 || lookback > 24*time.Hour {
		return Rate{}, "", ErrInput
	}
	if alwaysBandwidth {
		return fallback()
	}
	items := append([]Interval(nil), intervals...)
	sort.Slice(items, func(i, j int) bool { return items[i].Start.Before(items[j].Start) })
	start, cursor := at.Add(-lookback), at.Add(-lookback)
	peak := Rate{}
	var previous time.Time
	for _, v := range items {
		if !validInterval(v) || !previous.IsZero() && v.Start.Before(previous) {
			return Rate{}, "", ErrInput
		}
		previous = v.End
		if !v.End.After(start) || !v.Start.Before(at) {
			continue
		}
		if v.Gap || v.Projection != nil || v.Start.After(cursor) || v.End.Before(cursor) {
			return fallback()
		}
		duration := v.End.Sub(v.Start).Nanoseconds()
		if v.DurationMS > 0 {
			if v.DurationMS > int64((32*24*time.Hour)/time.Millisecond) {
				return Rate{}, "", ErrInput
			}
			duration = v.DurationMS * int64(time.Millisecond)
		}
		rx, err := CeilProduct(v.RX, int64(time.Second), duration)
		if err != nil {
			return Rate{}, "", err
		}
		tx, err := CeilProduct(v.TX, int64(time.Second), duration)
		if err != nil {
			return Rate{}, "", err
		}
		peak.RX, peak.TX = max(peak.RX, rx), max(peak.TX, tx)
		cursor = earlier(v.End, at)
	}
	if cursor.Before(at) {
		return fallback()
	}
	return peak, "observed_peak", nil
}

// Threshold includes a rounded-up lead allowance without overflowing int64.
func Threshold(official, estimate, currentRate, reserveSeconds, threshold int64) (bool, int64, error) {
	if official < 0 || estimate < 0 || currentRate < 0 || reserveSeconds < 0 || threshold <= 0 {
		return false, 0, ErrInput
	}
	lead, err := CeilProduct(currentRate, reserveSeconds, 1)
	if err != nil {
		return false, 0, err
	}
	total, err := add(max(official, estimate), lead)
	return total >= threshold, total, err
}

package store

import (
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

const AggregateAccountID = "all"

func aggregateAdd(dst *int64, v int64) error {
	n, err := addMetered(*dst, v)
	if err == nil {
		*dst = n
	}
	return err
}
func oldest(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

// AggregateQuotaStatuses adds independently computed account values for display.
// It never fills strict aligned fields or creates an executable account/pool.
func AggregateQuotaStatuses(accounts []model.Account, rows []model.QuotaStatus, loc *time.Location, now time.Time) ([]model.QuotaStatus, error) {
	enabled := []model.Account{}
	for _, a := range accounts {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	out := []model.QuotaStatus{}
	if len(enabled) == 0 {
		return out, nil
	}
	start, end := ReferenceCycle(now, loc)
	for _, scope := range []string{"mainland", "overseas"} {
		m := &model.TrafficAggregation{AccountCount: len(enabled)}
		q := model.QuotaStatus{Aggregation: m, AccountID: AggregateAccountID, Scope: scope, CycleStart: start, CycleEnd: end, Thresholds: []int{}, OfficialStatus: "unavailable", OfficialQuality: "inferred", ForecastStatus: "insufficient_data"}
		e := &model.TrafficEstimation{Mode: "unverified", Status: "incomplete", To: now, CalculatedAt: now, Reasons: []string{"account_aggregation", "independent_account_windows"}}
		var fetched, updated, calculated, to time.Time
		var increment, worst int64
		worstKnown := true
		incrementKnown := true
		fs := []*model.TrafficForecast{}
		for _, a := range enabled {
			quota := a.QuotaMainland
			if scope == "overseas" {
				quota = a.QuotaOverseas
			}
			if err := aggregateAdd(&q.QuotaBytes, quota); err != nil {
				return nil, err
			}
			var row *model.QuotaStatus
			for i := range rows {
				if rows[i].AccountID == a.ID && rows[i].Scope == scope && rows[i].CycleStart.Equal(start) && rows[i].CycleEnd.Equal(end) {
					row = &rows[i]
					break
				}
			}
			if row == nil {
				worstKnown = false
				continue
			}
			if row.OfficialBytes != nil {
				m.OfficialAccounts++
				if err := aggregateAdd(&m.KnownOfficial, *row.OfficialBytes); err != nil {
					return nil, err
				}
				if row.FetchedAt != nil {
					fetched = oldest(fetched, *row.FetchedAt)
				}
			}
			if row.UpdatedAt != nil {
				updated = oldest(updated, *row.UpdatedAt)
			}
			v := row.Estimation
			if v != nil {
				if err := aggregateAdd(&e.KnownIncrement, v.KnownIncrement); err != nil {
					return nil, err
				}
			}
			if v != nil && v.Status == "ready" && v.Estimated != nil && !v.CalculatedAt.After(now) && now.Sub(v.CalculatedAt) <= 30*time.Second && !v.To.After(now) {
				m.EstimatedAccounts++
				if err := aggregateAdd(&m.KnownEstimated, *v.Estimated); err != nil {
					return nil, err
				}
				calculated = oldest(calculated, v.CalculatedAt)
				to = oldest(to, v.To)
				if v.Increment != nil {
					if err := aggregateAdd(&increment, *v.Increment); err != nil {
						return nil, err
					}
				} else {
					incrementKnown = false
				}
				if v.WorstCase == nil {
					worstKnown = false
				} else if err := aggregateAdd(&worst, *v.WorstCase); err != nil {
					return nil, err
				}
			} else {
				worstKnown = false
			}
			if f := row.Forecast; f != nil {
				m.ForecastAccounts++
				if err := aggregateAdd(&m.KnownForecast, f.CycleEndBytes); err != nil {
					return nil, err
				}
				if len(fs) == 0 || f.SampleDays < m.ForecastDaysMin {
					m.ForecastDaysMin = f.SampleDays
				}
				m.ForecastDaysMax = max(m.ForecastDaysMax, f.SampleDays)
				if f.ExceedsOn != nil || f.AlreadyExceeded {
					m.ForecastExceeding++
				}
				fs = append(fs, f)
			}
		}
		if m.OfficialAccounts == m.AccountCount {
			q.OfficialBytes = &m.KnownOfficial
			q.OfficialStatus = "cutoff_unknown"
			if !fetched.IsZero() {
				q.FetchedAt = &fetched
			}
		}
		if !updated.IsZero() {
			q.UpdatedAt = &updated
		}
		if m.EstimatedAccounts == m.AccountCount {
			e.Status = "ready"
			e.Estimated = &m.KnownEstimated
			if incrementKnown {
				e.Increment = &increment
			}
			e.CalculatedAt = calculated
			e.To = to
			if worstKnown {
				e.WorstCase = &worst
			}
		} else {
			e.Reasons = append(e.Reasons, "account_data_incomplete")
		}
		q.Estimation = e
		if m.ForecastAccounts == m.AccountCount {
			f, err := aggregateForecasts(fs, loc, now)
			if err != nil {
				return nil, err
			}
			q.Forecast = f
			q.ForecastStatus = "ready"
		}
		out = append(out, q)
	}
	return out, nil
}

// Different sample histories remain independent. A combined exceed date would
// falsely treat the separate free quotas as transferable, so none is returned.
func aggregateForecasts(rows []*model.TrafficForecast, loc *time.Location, now time.Time) (*model.TrafficForecast, error) {
	f := &model.TrafficForecast{SampleDates: []string{}, Source: "mixed", Unverified: true, BaselineSource: "account_forecasts", BaselineAtAssumed: true, CalculatedAt: now, Points: []model.UsagePoint{}, Reasons: []string{"independent_account_forecasts", "quotas_not_transferable"}}
	type value struct {
		point model.UsagePoint
		count int
	}
	points := map[string]value{}
	for i, row := range rows {
		if err := aggregateAdd(&f.DailyAverage, row.DailyAverage); err != nil {
			return nil, err
		}
		if err := aggregateAdd(&f.CycleEndBytes, row.CycleEndBytes); err != nil {
			return nil, err
		}
		if err := aggregateAdd(&f.BaselineBytes, row.BaselineBytes); err != nil {
			return nil, err
		}
		f.BaselineAt = maxTime(f.BaselineAt, row.BaselineAt)
		f.CalculatedAt = oldest(f.CalculatedAt, row.CalculatedAt)
		if i == 0 {
			f.SampleDates = append(f.SampleDates, row.SampleDates...)
		} else if !slices.Equal(f.SampleDates, row.SampleDates) {
			f.SampleDates = []string{}
		}
		seen := map[string]bool{}
		for _, p := range row.Points {
			day, err := time.ParseInLocation("2006-01-02", p.Date, loc)
			if err != nil {
				continue
			}
			if !p.At.Equal(day.AddDate(0, 0, 1)) || seen[p.Date] {
				continue
			}
			seen[p.Date] = true
			x := points[p.Date]
			if err := aggregateAdd(&x.point.Bytes, p.Bytes); err != nil {
				return nil, err
			}
			x.point.At = p.At
			x.point.Date = p.Date
			x.count++
			points[p.Date] = x
		}
	}
	f.SampleDays = len(f.SampleDates)
	if f.BaselineAt.IsZero() {
		f.BaselineAt = now
	}
	f.Points = append(f.Points, model.UsagePoint{At: f.BaselineAt, Date: f.BaselineAt.In(loc).Format("2006-01-02"), Bytes: f.BaselineBytes})
	dates := []string{}
	for date, x := range points {
		if x.count == len(rows) && x.point.At.After(f.BaselineAt) {
			dates = append(dates, date)
		}
	}
	slices.Sort(dates)
	for _, date := range dates {
		f.Points = append(f.Points, points[date].point)
	}
	return f, nil
}

// AggregateDailyTraffic joins dates by business timezone. A missing account or
// scope keeps the full value null while preserving measured known subtotals.
func AggregateDailyTraffic(accounts []model.Account, rows []model.DailyTraffic, loc *time.Location, now time.Time) (model.DailyTraffic, error) {
	start, end := ReferenceCycle(now, loc)
	enabled := []model.Account{}
	for _, a := range accounts {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	v := model.DailyTraffic{Aggregation: &model.AccountAggregation{AccountCount: len(enabled)}, AccountID: AggregateAccountID, CycleStart: start, CycleEnd: end, Timezone: loc.String(), Days: []model.DailyTrafficDay{}, SourceStatus: "unavailable", OfficialPoints: map[string][]model.UsagePoint{"mainland": {}, "overseas": {}}}
	if len(enabled) == 0 {
		return v, nil
	}
	byID := map[string]model.DailyTraffic{}
	var updated time.Time
	allUpdated := true
	for _, a := range enabled {
		for _, r := range rows {
			if r.AccountID == a.ID && r.Timezone == loc.String() && r.CycleStart.Equal(start) && r.CycleEnd.Equal(end) {
				byID[a.ID] = r
				break
			}
		}
		r := byID[a.ID]
		if r.UpdatedAt == nil {
			allUpdated = false
		} else {
			updated = oldest(updated, *r.UpdatedAt)
		}
	}
	if allUpdated && !updated.IsZero() {
		v.UpdatedAt = &updated
	}
	anyData, allComplete := false, true
	for day := start.In(loc); !day.After(now); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		d := model.DailyTrafficDay{Date: date, Scopes: map[string]model.DailyScope{}}
		for _, scope := range []string{"mainland", "overseas"} {
			x := model.DailyScope{From: day.UTC(), Until: earlierTime(day.AddDate(0, 0, 1), now).UTC(), Source: "unavailable", Status: "unavailable", Reasons: []string{"account_aggregation"}}
			complete := true
			source := ""
			for _, a := range enabled {
				var s model.DailyScope
				found := false
				for _, r := range byID[a.ID].Days {
					if r.Date == date {
						s, found = r.Scopes[scope]
						break
					}
				}
				if !found {
					complete = false
					continue
				}
				if s.HasData {
					x.HasData = true
					if err := aggregateAdd(&x.KnownBytes, s.KnownBytes); err != nil {
						return v, err
					}
					if source == "" {
						source = s.Source
					} else if source != s.Source {
						source = "mixed"
					}
				}
				if s.Bytes == nil || s.Status != "complete" || !s.From.Equal(x.From) || !s.Until.Equal(x.Until) {
					complete = false
				}
				if s.ObservedFrom != nil && (x.ObservedFrom == nil || s.ObservedFrom.Before(*x.ObservedFrom)) {
					x.ObservedFrom = s.ObservedFrom
				}
				if s.ObservedUntil != nil && (x.ObservedUntil == nil || s.ObservedUntil.After(*x.ObservedUntil)) {
					x.ObservedUntil = s.ObservedUntil
				}
			}
			if x.HasData {
				x.Status = "partial"
				x.Source = source
			}
			if complete {
				x.Bytes = &x.KnownBytes
				x.Status = "complete"
				x.CompleteDay = !day.AddDate(0, 0, 1).After(now)
			} else {
				x.Reasons = append(x.Reasons, "account_data_incomplete")
			}
			d.Scopes[scope] = x
			anyData = anyData || x.HasData
			allComplete = allComplete && complete
			d.Provisional = d.Provisional || source != "official" || !x.CompleteDay
			if scope == "mainland" {
				d.Mainland = x.Bytes
			} else {
				d.Overseas = x.Bytes
			}
		}
		v.Days = append(v.Days, d)
	}
	if allComplete {
		v.SourceStatus = "available"
	} else if anyData {
		v.SourceStatus = "partial"
	}
	for _, scope := range []string{"mainland", "overseas"} {
		for day := start.In(loc); !day.After(now); day = day.AddDate(0, 0, 1) {
			date := day.Format("2006-01-02")
			p := model.UsagePoint{Date: date}
			count := 0
			for _, a := range enabled {
				for _, x := range byID[a.ID].OfficialPoints[scope] {
					if x.Date == date {
						count++
						p.At = maxTime(p.At, x.At)
						if err := aggregateAdd(&p.Bytes, x.Bytes); err != nil {
							return v, err
						}
						break
					}
				}
			}
			if count == len(enabled) {
				v.OfficialPoints[scope] = append(v.OfficialPoints[scope], p)
			}
		}
	}
	return v, nil
}

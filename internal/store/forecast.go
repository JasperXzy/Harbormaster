package store

import (
	"context"
	"time"

	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func (s *Store) forecastQuota(ctx context.Context, a model.Account, q *model.QuotaStatus, loc *time.Location, now time.Time) error {
	today := now.In(loc)
	midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	rows, _, err := s.dailyRows(ctx, a, loc, midnight.AddDate(0, 0, -7), midnight, true)
	if err != nil {
		return err
	}
	f := model.TrafficForecast{SampleDates: []string{}, Points: []model.UsagePoint{}, Reasons: []string{}, Source: "official", CalculatedAt: now}
	var sum int64
	agent, official := false, false
	for day := midnight.AddDate(0, 0, -7); day.Before(midnight); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		d, ok := rows[date]
		if !ok {
			continue
		}
		x, ok := d.Scopes[q.Scope]
		if !ok || !x.CompleteDay || x.Bytes == nil || x.Status != "complete" {
			continue
		}
		if sum, err = addMetered(sum, *x.Bytes); err != nil {
			return err
		}
		f.SampleDates = append(f.SampleDates, date)
		agent = agent || x.Source == "agent_estimate"
		official = official || x.Source == "official"
	}
	f.SampleDays = len(f.SampleDates)
	q.ForecastStatus = "insufficient_data"
	if f.SampleDays == 0 {
		return nil
	}
	f.DailyAverage, err = metering.CeilProduct(sum, 1, int64(f.SampleDays))
	if err != nil {
		return err
	}
	if agent {
		f.Source = "agent_estimate"
		f.Unverified = true
	}
	if agent && official {
		f.Source = "mixed"
	}
	if f.SampleDays < 7 {
		f.Reasons = append(f.Reasons, "fewer_than_seven_complete_days")
	}
	q.ForecastStatus = "baseline_missing"
	e := q.Estimation
	if e != nil && e.Status == "ready" && e.Estimated != nil && !e.To.After(now) && !e.CalculatedAt.After(now) && now.Sub(e.CalculatedAt) <= 30*time.Second {
		f.BaselineBytes = *e.Estimated
		f.BaselineAt = e.To
		f.BaselineSource = "realtime_estimate"
		f.Unverified = f.Unverified || !e.Qualified || e.Mode != "aligned"
	} else if q.OfficialBytes != nil && q.FetchedAt != nil && !q.FetchedAt.After(now) {
		f.BaselineBytes = *q.OfficialBytes
		f.BaselineSource = "official"
		if q.OfficialAsOf != nil {
			f.BaselineAt = *q.OfficialAsOf
		} else {
			settings, e := s.EstimationSettings(ctx)
			if e != nil {
				return e
			}
			f.BaselineAt = q.FetchedAt.Add(-time.Duration(settings.LagSeconds) * time.Second)
			f.BaselineAt = maxTime(f.BaselineAt, q.CycleStart)
			f.BaselineAtAssumed = true
			f.Unverified = true
			f.Reasons = append(f.Reasons, "official_baseline_cutoff_assumed")
		}
		if q.OfficialQuality != "verified" {
			f.Unverified = true
		}
	} else {
		return nil
	}
	if f.BaselineAt.Before(q.CycleStart) || !f.BaselineAt.Before(q.CycleEnd) || f.BaselineAt.After(now) {
		return nil
	}
	if f.Unverified {
		f.Reasons = append(f.Reasons, "display_only_unverified_forecast")
	}
	if err = projectForecast(&f, q.QuotaBytes, q.CycleEnd, loc); err != nil {
		return err
	}
	q.Forecast = &f
	q.ForecastStatus = "ready"
	return nil
}

// Project the remaining fraction of each business day, including DST, from the
// baseline's actual/explicitly assumed timestamp. Today is never a rate sample.
func projectForecast(f *model.TrafficForecast, quota int64, end time.Time, loc *time.Location) error {
	base := f.BaselineAt.In(loc)
	total := f.BaselineBytes
	f.Points = append(f.Points, model.UsagePoint{At: f.BaselineAt, Date: base.Format("2006-01-02"), Bytes: total})
	if total > quota {
		f.AlreadyExceeded = true
		date := base.Format("2006-01-02")
		f.ExceedsOn = &date
	}
	for day := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, loc); day.Before(end); day = day.AddDate(0, 0, 1) {
		until := earlierTime(day.AddDate(0, 0, 1), end)
		from := maxTime(day, f.BaselineAt)
		inc, err := metering.CeilProduct(f.DailyAverage, until.Sub(from).Nanoseconds(), day.AddDate(0, 0, 1).Sub(day).Nanoseconds())
		if err != nil {
			return err
		}
		if total, err = addMetered(total, inc); err != nil {
			return err
		}
		date := day.Format("2006-01-02")
		f.Points = append(f.Points, model.UsagePoint{At: until, Date: date, Bytes: total})
		if f.ExceedsOn == nil && total > quota {
			x := date
			f.ExceedsOn = &x
		}
	}
	f.CycleEndBytes = total
	return nil
}

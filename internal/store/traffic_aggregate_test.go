package store

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func aggregateFixture() ([]model.Account, []model.QuotaStatus, time.Time) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	start, end := ReferenceCycle(now, time.UTC)
	accounts := []model.Account{{ID: "a", Enabled: true, QuotaOverseas: 200}, {ID: "b", Enabled: true, QuotaOverseas: 200}, {ID: "disabled", Enabled: false, QuotaOverseas: 99999}}
	rows := []model.QuotaStatus{}
	for i, id := range []string{"a", "b", "disabled"} {
		official := int64(i * 10)
		estimate := official + 5
		inc := int64(5)
		zero := int64(0)
		rows = append(rows, model.QuotaStatus{AccountID: id, Scope: "overseas", CycleStart: start, CycleEnd: end, OfficialBytes: &official, FetchedAt: &now, UpdatedAt: &now, Estimation: &model.TrafficEstimation{Mode: "aligned", Status: "ready", CalculatedAt: now, To: now, Estimated: &estimate, Increment: &inc, KnownIncrement: inc, WorstCase: &zero, Qualified: true}})
	}
	return accounts, rows, now
}

func TestTrafficAggregateKnownZeroDisabledAndExecutionIsolation(t *testing.T) {
	a, rows, now := aggregateFixture()
	totals, err := AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	q := totals[1]
	if q.QuotaBytes != 400 || q.OfficialBytes == nil || *q.OfficialBytes != 10 || q.Estimation.Estimated == nil || *q.Estimation.Estimated != 20 || q.Aggregation.AccountCount != 2 || q.Aggregation.EstimatedAccounts != 2 {
		t.Fatal(q)
	}
	if q.Estimation.Qualified || q.EffectiveBytes != nil || q.EstimatedBytes != nil || q.OfficialAsOf != nil || !rows[0].Estimation.Qualified {
		t.Fatal("display sum changed execution evidence")
	}
	for i := range rows {
		if i < 2 {
			rows[i].Estimation.Estimated = new(int64(0))
		}
	}
	totals, err = AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil || totals[1].Estimation.Estimated == nil || *totals[1].Estimation.Estimated != 0 {
		t.Fatal(totals, err)
	}
}

func TestTrafficAggregatePreservesMissingAndStaleAccounts(t *testing.T) {
	a, rows, now := aggregateFixture()
	rows[1].OfficialBytes = nil
	rows[1].Estimation = nil
	totals, err := AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	q := totals[1]
	if q.OfficialBytes != nil || q.Estimation.Estimated != nil || q.Aggregation.EstimatedAccounts != 1 || q.Aggregation.KnownEstimated != 5 || q.Aggregation.OfficialAccounts != 1 {
		t.Fatal(q)
	}
	rows[0].Estimation.CalculatedAt = now.Add(-31 * time.Second)
	totals, err = AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil || totals[1].Aggregation.EstimatedAccounts != 0 || totals[1].Aggregation.KnownEstimated != 0 {
		t.Fatal(totals, err)
	}
	for i := range a {
		a[i].Enabled = false
	}
	totals, err = AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil || len(totals) != 0 {
		t.Fatal(totals, err)
	}
	a[0].Enabled = true
	a[1].Enabled = true
	a[0].QuotaOverseas = math.MaxInt64
	a[1].QuotaOverseas = 1
	if _, err = AggregateQuotaStatuses(a, rows, time.UTC, now); err == nil {
		t.Fatal("aggregate wrapped on overflow")
	}
}

func TestAggregateForecastKeepsIndependentSamplesAndQuotas(t *testing.T) {
	a, rows, now := aggregateFixture()
	date := "2026-11-20"
	for i := 0; i < 2; i++ {
		base := int64((i + 1) * 10)
		end := int64((i + 1) * 100)
		samples := []string{"2026-11-16", "2026-11-17"}
		if i == 1 {
			samples = []string{"2026-11-15", "2026-11-16", "2026-11-17"}
		}
		rows[i].Forecast = &model.TrafficForecast{BaselineBytes: base, BaselineAt: now.Add(-time.Duration(i+1) * time.Second), CalculatedAt: now, DailyAverage: 10, CycleEndBytes: end, SampleDays: len(samples), SampleDates: samples, ExceedsOn: &date, Points: []model.UsagePoint{{At: now, Date: "2026-11-18", Bytes: base}, {At: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC), Date: "2026-11-18", Bytes: base + 10}}}
	}
	totals, err := AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	q := totals[1]
	f := q.Forecast
	if f == nil || f.CycleEndBytes != 300 || f.DailyAverage != 20 || f.SampleDays != 0 || len(f.SampleDates) != 0 || f.ExceedsOn != nil || !f.Unverified || !f.BaselineAtAssumed || q.Aggregation.ForecastDaysMin != 2 || q.Aggregation.ForecastDaysMax != 3 || q.Aggregation.ForecastExceeding != 2 || len(f.Points) != 2 || f.Points[1].Bytes != 50 {
		t.Fatal(q)
	}
	raw, _ := json.Marshal(f)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if _, exists := fields["sample_days"]; exists {
		t.Fatal("invented common sample count")
	}
	rows[1].Forecast = nil
	totals, err = AggregateQuotaStatuses(a, rows, time.UTC, now)
	if err != nil || totals[1].Forecast != nil || totals[1].Aggregation.KnownForecast != 100 || totals[1].Aggregation.ForecastAccounts != 1 {
		t.Fatal(totals, err)
	}
}

func TestDailyAggregateJoinsDatesWithoutFillingCoverage(t *testing.T) {
	a, _, now := aggregateFixture()
	start, end := ReferenceCycle(now, time.UTC)
	yesterday := now.Truncate(24*time.Hour).AddDate(0, 0, -1)
	today := yesterday.AddDate(0, 0, 1)
	data := []model.DailyTraffic{}
	for i, id := range []string{"a", "b", "disabled"} {
		n := int64(i + 5)
		source := "official"
		if i > 0 {
			source = "agent_estimate"
		}
		r := model.DailyTraffic{AccountID: id, CycleStart: start, CycleEnd: end, Timezone: "UTC", UpdatedAt: &now, Days: []model.DailyTrafficDay{{Date: yesterday.Format("2006-01-02"), Scopes: map[string]model.DailyScope{"overseas": {Bytes: &n, KnownBytes: n, HasData: true, Source: source, Status: "complete", CompleteDay: true, From: yesterday, Until: today}}}}, OfficialPoints: map[string][]model.UsagePoint{"overseas": {{At: now, Date: "2026-11-18", Bytes: n}}}}
		if i == 0 {
			r.Days = append(r.Days, model.DailyTrafficDay{Date: today.Format("2006-01-02"), Scopes: map[string]model.DailyScope{"overseas": {KnownBytes: 3, HasData: true, Source: "agent_estimate", Status: "partial", From: today, Until: now}}})
			r.OfficialPoints["overseas"] = append(r.OfficialPoints["overseas"], model.UsagePoint{At: now, Date: "2026-11-17", Bytes: 999})
		}
		data = append(data, r)
	}
	v, err := AggregateDailyTraffic(a, data, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	prior := v.Days[len(v.Days)-2].Scopes["overseas"]
	current := v.Days[len(v.Days)-1].Scopes["overseas"]
	if prior.Bytes == nil || *prior.Bytes != 11 || !prior.CompleteDay || prior.Source != "mixed" || current.Bytes != nil || current.KnownBytes != 3 || current.CompleteDay || v.Aggregation.AccountCount != 2 {
		t.Fatal(prior, current)
	}
	if len(v.OfficialPoints["overseas"]) != 1 || v.OfficialPoints["overseas"][0].Bytes != 11 || v.Days[0].Overseas != nil {
		t.Fatal(v)
	}
}

package devmode

import (
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
	"time"
)

func demoTrafficAccounts(f fixtures) ([]model.Account, error) {
	raw, err := json.Marshal(f.Accounts)
	if err != nil {
		return nil, err
	}
	var accounts []model.Account
	err = json.Unmarshal(raw, &accounts)
	return accounts, err
}
func demoAggregateQuotas(f fixtures, loc *time.Location, now time.Time) ([]model.QuotaStatus, error) {
	accounts, err := demoTrafficAccounts(f)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(f.Quotas)
	if err != nil {
		return nil, err
	}
	var rows []model.QuotaStatus
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	return store.AggregateQuotaStatuses(accounts, rows, loc, now)
}
func demoAggregateDaily(f fixtures, loc *time.Location, now time.Time) (model.DailyTraffic, error) {
	accounts, err := demoTrafficAccounts(f)
	if err != nil {
		return model.DailyTraffic{}, err
	}
	start, end := store.ReferenceCycle(now, loc)
	rows := []model.DailyTraffic{}
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		d := model.DailyTraffic{AccountID: a.ID, CycleStart: start, CycleEnd: end, Timezone: loc.String(), UpdatedAt: &now, OfficialPoints: map[string][]model.UsagePoint{}}
		for day := start.In(loc); !day.After(now); day = day.AddDate(0, 0, 1) {
			values := map[string]model.DailyScope{}
			closed := !day.AddDate(0, 0, 1).After(now)
			for _, scope := range []string{"mainland", "overseas"} {
				value := int64(0)
				if scope == "overseas" {
					value = int64(day.Day()) * 100000000
				}
				var full *int64
				if closed {
					full = &value
				}
				values[scope] = model.DailyScope{Bytes: full, KnownBytes: value, HasData: true, Source: "agent_estimate", Status: map[bool]string{true: "complete", false: "partial"}[closed], CompleteDay: closed, From: day.UTC(), Until: now, ObservedFrom: new(day.UTC()), ObservedUntil: &now, Reasons: []string{"demo_data"}}
				x := values[scope]
				if closed {
					x.Until = day.AddDate(0, 0, 1).UTC()
					x.ObservedUntil = &x.Until
				}
				values[scope] = x
			}
			d.Days = append(d.Days, model.DailyTrafficDay{Date: day.Format("2006-01-02"), Scopes: values, Mainland: values["mainland"].Bytes, Overseas: values["overseas"].Bytes, Provisional: true})
		}
		rows = append(rows, d)
	}
	return store.AggregateDailyTraffic(accounts, rows, loc, now)
}

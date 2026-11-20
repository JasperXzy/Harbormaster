package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func readTrafficPricingTx(ctx context.Context, q queryRower) (model.TrafficPricing, error) {
	p := model.DefaultTrafficPricing()
	var raw string
	var at sql.NullString
	err := q.QueryRowContext(ctx, "SELECT revision,config_json,updated_at FROM traffic_pricing WHERE id=1").Scan(&p.Revision, &raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var cfg struct {
		China         model.SitePricingProfile `json:"china"`
		International model.SitePricingProfile `json:"international"`
	}
	if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
		return p, err
	}
	p.China = cfg.China
	p.International = cfg.International
	p.UpdatedAt = parseAgentTime(at)
	return p, nil
}

func (s *Store) TrafficPricing(ctx context.Context) (model.TrafficPricing, error) {
	return readTrafficPricingTx(ctx, s.DB)
}

func (s *Store) SaveTrafficPricing(ctx context.Context, in model.TrafficPricing, now time.Time) (model.TrafficPricing, error) {
	if fields := in.Validate(); len(fields) > 0 {
		return model.TrafficPricing{}, &SettingsValidationError{Fields: fields}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.TrafficPricing{}, err
	}
	defer tx.Rollback()
	cur, err := readTrafficPricingTx(ctx, tx)
	if err != nil {
		return cur, err
	}
	if cur.Revision != in.Revision {
		return cur, ErrEstimationRevision
	}
	if reflect.DeepEqual(cur.China, in.China) && reflect.DeepEqual(cur.International, in.International) {
		return cur, tx.Commit()
	}
	nextRev := cur.Revision + 1
	at := now.UTC()
	payload, err := json.Marshal(struct {
		China         model.SitePricingProfile `json:"china"`
		International model.SitePricingProfile `json:"international"`
	}{China: in.China, International: in.International})
	if err != nil {
		return cur, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO traffic_pricing(id,revision,config_json,updated_at) VALUES(1,?,?,?)
 ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,config_json=excluded.config_json,updated_at=excluded.updated_at`,
		nextRev, string(payload), timestamp(at)); err != nil {
		return cur, err
	}
	details := auditDiff(
		auditChange{"china", cur.China, in.China},
		auditChange{"international", cur.International, in.International},
	)
	details.Revision = &nextRev
	if err = appendAudit(ctx, tx, auditEvent{Action: "traffic_pricing_updated", TargetType: "settings", TargetID: "traffic_pricing", Details: details}, at); err != nil {
		return cur, err
	}
	in.Revision = nextRev
	in.UpdatedAt = &at
	return in, tx.Commit()
}

func quotaOverageCostForAccount(a model.Account, q model.QuotaStatus, pricing model.TrafficPricing, now time.Time) *model.QuotaOverageCost {
	profile := pricing.China
	if a.Site == "international" {
		profile = pricing.International
	}
	tiers := profile.OverseasTiers
	if q.Scope == "mainland" {
		tiers = profile.MainlandTiers
	}
	out := &model.QuotaOverageCost{
		Currency:   profile.Currency,
		Unverified: q.OfficialQuality != "verified",
	}
	var usable *int64
	if q.Estimation != nil && q.Estimation.Status == "ready" && q.Estimation.Estimated != nil && !q.Estimation.CalculatedAt.After(now) && now.Sub(q.Estimation.CalculatedAt) <= 30*time.Second {
		v := *q.Estimation.Estimated
		if q.OfficialBytes != nil && *q.OfficialBytes > v {
			v = *q.OfficialBytes
		}
		usable = &v
		if q.Estimation.Mode != "aligned" {
			out.Unverified = true
		}
	} else if q.OfficialBytes != nil {
		v := *q.OfficialBytes
		usable = &v
	}
	if usable != nil {
		over := max(int64(0), *usable-q.QuotaBytes)
		cost := model.ComputeTieredCost(over, tiers)
		out.CurrentOverageBytes = &over
		out.EstimatedCost = &cost
	}
	if q.Forecast != nil {
		fOver := max(int64(0), q.Forecast.CycleEndBytes-q.QuotaBytes)
		fCost := model.ComputeTieredCost(fOver, tiers)
		out.ForecastOverageBytes = &fOver
		out.ForecastCost = &fCost
		if q.Forecast.Unverified {
			out.Unverified = true
		}
	}
	return out
}

func aggregateQuotaOverageCost(accounts []model.Account, rows []model.QuotaStatus, scope string, start, end time.Time) *model.QuotaOverageCost {
	var currency string
	var curOver, foreOver int64
	curSum := new(big.Rat)
	foreSum := new(big.Rat)
	allCur, allFore := true, true
	unverified := false
	included := 0
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		included++
		var row *model.QuotaStatus
		for i := range rows {
			if rows[i].AccountID == a.ID && rows[i].Scope == scope && rows[i].CycleStart.Equal(start) && rows[i].CycleEnd.Equal(end) {
				row = &rows[i]
				break
			}
		}
		if row == nil || row.OverageCost == nil {
			return nil
		}
		if currency == "" {
			currency = row.OverageCost.Currency
		} else if currency != row.OverageCost.Currency {
			// Never sum mixed currencies (e.g. CNY + USD).
			return nil
		}
		if row.OverageCost.Unverified {
			unverified = true
		}
		if row.OverageCost.CurrentOverageBytes != nil && row.OverageCost.EstimatedCost != nil {
			curOver += *row.OverageCost.CurrentOverageBytes
			if r, ok := model.DecimalAmount(*row.OverageCost.EstimatedCost); ok {
				curSum.Add(curSum, r)
			}
		} else {
			allCur = false
		}
		if row.OverageCost.ForecastOverageBytes != nil && row.OverageCost.ForecastCost != nil {
			foreOver += *row.OverageCost.ForecastOverageBytes
			if r, ok := model.DecimalAmount(*row.OverageCost.ForecastCost); ok {
				foreSum.Add(foreSum, r)
			}
		} else {
			allFore = false
		}
	}
	if included == 0 || currency == "" {
		return nil
	}
	out := &model.QuotaOverageCost{Currency: currency, Unverified: true || unverified}
	if allCur {
		c := model.FormatMoneyCents(curSum)
		out.CurrentOverageBytes = &curOver
		out.EstimatedCost = &c
	}
	if allFore {
		f := model.FormatMoneyCents(foreSum)
		out.ForecastOverageBytes = &foreOver
		out.ForecastCost = &f
	}
	return out
}

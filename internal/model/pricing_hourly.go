package model

import (
	"math/big"
	"strconv"
	"time"
)

type AgentHourlyBucket struct {
	HourStart     time.Time `json:"hour_start"`
	RXBytes       int64     `json:"rx_bytes"`
	TXBytes       int64     `json:"tx_bytes"`
	RXRate        float64   `json:"rx_bytes_per_second"`
	TXRate        float64   `json:"tx_bytes_per_second"`
	CoveredSecond int       `json:"covered_seconds"`
	SampleCount   int       `json:"sample_count"`
	Estimated     bool      `json:"estimated"`
}

type BandwidthHourlyPeriod struct {
	Start        time.Time `json:"start"`
	RXBytes      int64     `json:"rx_bytes"`
	TXBytes      int64     `json:"tx_bytes"`
	MeteredBytes int64     `json:"metered_bytes"`
}

type PriceTier struct {
	UpToGiB   *int64 `json:"up_to_gib"`
	UnitPrice string `json:"unit_price"`
}

type SitePricingProfile struct {
	Currency      string      `json:"currency"`
	MainlandTiers []PriceTier `json:"mainland_tiers"`
	OverseasTiers []PriceTier `json:"overseas_tiers"`
}

type TrafficPricing struct {
	Revision      int64              `json:"revision"`
	UpdatedAt     *time.Time         `json:"updated_at"`
	China         SitePricingProfile `json:"china"`
	International SitePricingProfile `json:"international"`
}

type QuotaOverageCost struct {
	Currency             string  `json:"currency"`
	CurrentOverageBytes  *int64  `json:"current_overage_bytes"`
	EstimatedCost        *string `json:"estimated_cost"`
	ForecastOverageBytes *int64  `json:"forecast_overage_bytes"`
	ForecastCost         *string `json:"forecast_cost"`
	Unverified           bool    `json:"unverified"`
}

func DefaultTrafficPricing() TrafficPricing {
	g10t := int64(10240)
	g50t := int64(51200)
	return TrafficPricing{
		Revision: 0,
		China: SitePricingProfile{
			Currency:      "CNY",
			MainlandTiers: []PriceTier{{UpToGiB: nil, UnitPrice: "0.80"}},
			OverseasTiers: []PriceTier{{UpToGiB: &g10t, UnitPrice: "0.50"}, {UpToGiB: &g50t, UnitPrice: "0.45"}, {UpToGiB: nil, UnitPrice: "0.40"}},
		},
		International: SitePricingProfile{
			Currency:      "USD",
			MainlandTiers: []PriceTier{{UpToGiB: nil, UnitPrice: "0.125"}},
			OverseasTiers: []PriceTier{{UpToGiB: &g10t, UnitPrice: "0.08"}, {UpToGiB: &g50t, UnitPrice: "0.075"}, {UpToGiB: nil, UnitPrice: "0.07"}},
		},
	}
}

func validateTiers(prefix string, tiers []PriceTier, fields map[string]string) {
	if len(tiers) == 0 || len(tiers) > 10 {
		fields[prefix] = "out_of_range"
		return
	}
	var prev int64
	for i, t := range tiers {
		tp := prefix + "." + strconv.Itoa(i)
		r, ok := DecimalAmount(t.UnitPrice)
		if !ok || r.Sign() < 0 {
			fields[tp+".unit_price"] = "invalid_format"
		}
		if i == len(tiers)-1 {
			if t.UpToGiB != nil {
				fields[tp+".up_to_gib"] = "must_be_null_for_last_tier"
			}
		} else {
			if t.UpToGiB == nil || *t.UpToGiB <= prev || *t.UpToGiB > 10_000_000 {
				fields[tp+".up_to_gib"] = "invalid_tier_bound"
			} else {
				prev = *t.UpToGiB
			}
		}
	}
}

func (p TrafficPricing) Validate() map[string]string {
	fields := map[string]string{}
	if p.Revision < 0 {
		fields["revision"] = "out_of_range"
	}
	if p.China.Currency != "CNY" && p.China.Currency != "USD" {
		fields["china.currency"] = "invalid_value"
	}
	if p.International.Currency != "CNY" && p.International.Currency != "USD" {
		fields["international.currency"] = "invalid_value"
	}
	validateTiers("china.mainland_tiers", p.China.MainlandTiers, fields)
	validateTiers("china.overseas_tiers", p.China.OverseasTiers, fields)
	validateTiers("international.mainland_tiers", p.International.MainlandTiers, fields)
	validateTiers("international.overseas_tiers", p.International.OverseasTiers, fields)
	return fields
}

// ComputeTieredCost calculates exact overage cost for overageBytes (>= 0)
// priced per GiB (1024^3 bytes), formatted to 2 decimal places (half-up).
func ComputeTieredCost(overageBytes int64, tiers []PriceTier) string {
	if overageBytes <= 0 || len(tiers) == 0 {
		return "0.00"
	}
	const gib = int64(1 << 30)
	remaining := big.NewInt(overageBytes)
	total := new(big.Rat)
	var prevBound int64
	for i, tier := range tiers {
		if remaining.Sign() <= 0 {
			break
		}
		rate, ok := DecimalAmount(tier.UnitPrice)
		if !ok || rate.Sign() <= 0 {
			if tier.UpToGiB != nil && i < len(tiers)-1 {
				spanBytes := new(big.Int).Mul(big.NewInt(*tier.UpToGiB-prevBound), big.NewInt(gib))
				if remaining.Cmp(spanBytes) > 0 {
					remaining.Sub(remaining, spanBytes)
				} else {
					remaining.SetInt64(0)
				}
				prevBound = *tier.UpToGiB
			} else {
				remaining.SetInt64(0)
			}
			continue
		}
		useBytes := new(big.Int).Set(remaining)
		if tier.UpToGiB != nil && i < len(tiers)-1 {
			spanBytes := new(big.Int).Mul(big.NewInt(*tier.UpToGiB-prevBound), big.NewInt(gib))
			if useBytes.Cmp(spanBytes) > 0 {
				useBytes.Set(spanBytes)
			}
			prevBound = *tier.UpToGiB
		}
		part := new(big.Rat).SetFrac(useBytes, big.NewInt(gib))
		part.Mul(part, rate)
		total.Add(total, part)
		remaining.Sub(remaining, useBytes)
	}
	return FormatMoneyCents(total)
}

func FormatMoneyCents(v *big.Rat) string {
	if v == nil || v.Sign() <= 0 {
		return "0.00"
	}
	scaled := new(big.Rat).Mul(v, big.NewRat(100, 1))
	half := new(big.Rat).Add(scaled, big.NewRat(1, 2))
	cents := new(big.Int).Quo(half.Num(), half.Denom())
	whole := new(big.Int).Quo(cents, big.NewInt(100))
	rem := new(big.Int).Mod(cents, big.NewInt(100))
	frac := rem.String()
	if len(frac) == 1 {
		frac = "0" + frac
	}
	return whole.String() + "." + frac
}

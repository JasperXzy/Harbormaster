package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func cdtUsageAndForecastAlertsTx(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule, accounts []model.Account, now time.Time) ([]alertObservation, error) {
	usageRule := rules["cdt_usage"]
	forecastRule := rules["cdt_forecast_exceed"]
	if !usageRule.Enabled && !forecastRule.Enabled {
		return nil, nil
	}
	loc := copyLocation(ctx, tx)
	start, end := ReferenceCycle(now, loc)
	today := now.In(loc)
	midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	settings, err := readEstimationSettings(ctx, tx)
	if err != nil {
		return nil, err
	}

	observations := []alertObservation{}
	for _, a := range accounts {
		for _, scope := range a.TrackedScopes() {
			quota := a.QuotaMainland
			column := "mainland_bytes"
			if scope == "overseas" {
				quota = a.QuotaOverseas
				column = "overseas_bytes"
			}
			if quota <= 0 {
				continue
			}

			var official *int64
			var officialAt *time.Time
			var fetchedAt *time.Time
			var est *model.TrafficEstimation
			good, gErr := readObservation(tx.QueryRowContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND fetched_at>=? AND fetched_at<? AND error_code IS NULL AND "+column+" IS NOT NULL ORDER BY id DESC LIMIT 1", a.ID, a.CredentialVersion, timestamp(start), timestamp(end)))
			if gErr != nil && !errors.Is(gErr, sql.ErrNoRows) {
				return nil, gErr
			}
			if gErr == nil {
				if scope == "overseas" {
					official = good.Overseas
				} else {
					official = good.Mainland
				}
				t := good.FetchedAt
				fetchedAt = &t
				if good.Quality == "verified" && good.PeriodStart != nil && good.PeriodEnd != nil && good.PeriodStart.Equal(start) && good.PeriodEnd.Equal(end) {
					officialAt = good.CoveredUntil
				}
				var rawEst, calcStr string
				eErr := tx.QueryRowContext(ctx, "SELECT calculated_at,payload_json FROM traffic_estimate WHERE account_id=? AND scope=? AND credential_version=? AND account_revision=? AND settings_revision=? AND basis_id=?",
					a.ID, scope, a.CredentialVersion, a.Revision, settings.Revision, good.ID).Scan(&calcStr, &rawEst)
				if eErr == nil {
					var parsed model.TrafficEstimation
					if json.Unmarshal([]byte(rawEst), &parsed) == nil && parsed.Status == "ready" && parsed.Estimated != nil && !parsed.CalculatedAt.After(now) && now.Sub(parsed.CalculatedAt) <= 60*time.Second {
						est = &parsed
					}
				}
			}

			var usedBytes *int64
			unverified := false
			if official != nil {
				v := *official
				usedBytes = &v
			}
			if est != nil && est.Estimated != nil {
				if usedBytes == nil || *est.Estimated > *usedBytes {
					v := *est.Estimated
					usedBytes = &v
				}
				if est.Mode != "aligned" {
					unverified = true
				}
			}

			if usageRule.Enabled && usedBytes != nil {
				tiers := []int{80, 90, 95}
				if len(a.Thresholds) > 0 {
					tiers = slices.Clone(a.Thresholds)
				} else if usageRule.Params.ThresholdsPercent != nil && len(*usageRule.Params.ThresholdsPercent) > 0 {
					tiers = slices.Clone(*usageRule.Params.ThresholdsPercent)
				}
				if !slices.Contains(tiers, 100) {
					tiers = append(tiers, 100)
				}
				sort.Ints(tiers)
				for _, tier := range tiers {
					if tier <= 0 {
						continue
					}
					limitBytes, cErr := metering.CeilProduct(quota, int64(tier), 100)
					if cErr != nil || *usedBytes < limitBytes {
						continue
					}
					sev := "serious"
					if tier >= 95 {
						sev = "critical"
					}
					ruleKey := "cdt_usage"
					accID := a.ID
					title, body := cdtUsageCopy(ctx, tx, a.ID, scope, tier, *usedBytes, quota, official, officialAt, unverified, now)
					evKey := fmt.Sprintf("cdt_usage:%s:%s:%s:%d", a.ID, scope, timestamp(start), tier)
					if _, nErr := enqueueNotification(ctx, tx, model.Notification{
						EventKey:  evKey,
						Kind:      "alert",
						Severity:  sev,
						RuleKey:   &ruleKey,
						Title:     title,
						Body:      body,
						AccountID: &accID,
						Urgent:    usageRule.Urgent,
					}, now); nErr != nil {
						return nil, nErr
					}
				}
			}

			if forecastRule.Enabled {
				target := a.ID + ":" + scope
				obs := alertObservation{rule: "cdt_forecast_exceed", target: target, account: a.ID, outcome: "unknown", facts: alertFacts{scope: scope, quotaBytes: quota}}
				dailyAvg, exceedsOn, projected, ok, fErr := evaluateScopeForecastTx(ctx, tx, a, scope, start, end, midnight, official, officialAt, fetchedAt, est, settings.LagSeconds, quota, loc, now)
				if fErr != nil {
					return nil, fErr
				}
				if ok {
					if projected > quota {
						obs.outcome = "firing"
						obs.facts.dailyAvgBytes = dailyAvg
						obs.facts.exceedsOn = exceedsOn
						if usedBytes != nil {
							obs.facts.usedBytes = *usedBytes
							obs.facts.hasUsed = true
						}
					} else {
						obs.outcome = "healthy"
					}
				}
				observations = append(observations, obs)
			}
		}
	}
	return observations, nil
}

func evaluateScopeForecastTx(ctx context.Context, tx *sql.Tx, a model.Account, scope string, cycleStart, cycleEnd, midnight time.Time, official *int64, officialAsOf, fetchedAt *time.Time, est *model.TrafficEstimation, lagSeconds int, quota int64, loc *time.Location, now time.Time) (int64, string, int64, bool, error) {
	fromDate := midnight.AddDate(0, 0, -7).Format("2006-01-02")
	toDate := midnight.Format("2006-01-02")
	rows, err := tx.QueryContext(ctx, `SELECT day,payload_json FROM daily_traffic
 WHERE account_id=? AND timezone=? AND credential_version=? AND account_revision=? AND day>=? AND day<?
 AND NOT EXISTS (SELECT 1 FROM daily_work w WHERE w.account_id=daily_traffic.account_id AND w.timezone=daily_traffic.timezone AND w.day=daily_traffic.day AND w.pending=1)`,
		a.ID, loc.String(), a.CredentialVersion, a.Revision, fromDate, toDate)
	if err != nil {
		return 0, "", 0, false, err
	}
	var sum int64
	sampleDays := 0
	for rows.Next() {
		var day, raw string
		if err = rows.Scan(&day, &raw); err != nil {
			rows.Close()
			return 0, "", 0, false, err
		}
		var d model.DailyTrafficDay
		if json.Unmarshal([]byte(raw), &d) != nil {
			continue
		}
		sc, ok := d.Scopes[scope]
		if !ok || !sc.CompleteDay || sc.Bytes == nil || sc.Status != "complete" {
			continue
		}
		sum, err = addMetered(sum, *sc.Bytes)
		if err != nil {
			rows.Close()
			return 0, "", 0, false, err
		}
		sampleDays++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || sampleDays == 0 {
		return 0, "", 0, false, err
	}
	dailyAvg, err := metering.CeilProduct(sum, 1, int64(sampleDays))
	if err != nil {
		return 0, "", 0, false, err
	}
	var baseBytes int64
	var baseAt time.Time
	if est != nil && est.Estimated != nil && !est.To.After(now) {
		baseBytes = *est.Estimated
		baseAt = est.To
	} else if official != nil && fetchedAt != nil && !fetchedAt.After(now) {
		baseBytes = *official
		if officialAsOf != nil {
			baseAt = *officialAsOf
		} else {
			baseAt = maxTime(fetchedAt.Add(-time.Duration(lagSeconds)*time.Second), cycleStart)
		}
	} else {
		return 0, "", 0, false, nil
	}
	if baseAt.Before(cycleStart) || !baseAt.Before(cycleEnd) {
		return 0, "", 0, false, nil
	}
	remSeconds := int64(cycleEnd.Sub(baseAt) / time.Second)
	if remSeconds < 0 {
		remSeconds = 0
	}
	remBytes, err := metering.CeilProduct(dailyAvg, remSeconds, 86400)
	if err != nil {
		return 0, "", 0, false, err
	}
	projected, err := addMetered(baseBytes, remBytes)
	if err != nil {
		return 0, "", 0, false, err
	}
	exceedsOn := ""
	if projected > quota && dailyAvg > 0 {
		if baseBytes >= quota {
			exceedsOn = baseAt.In(loc).Format("01-02")
		} else {
			need := quota - baseBytes
			secNeeded, _ := metering.CeilProduct(need, 86400, dailyAvg)
			exceedAt := baseAt.Add(time.Duration(secNeeded) * time.Second)
			if !exceedAt.After(cycleEnd) {
				exceedsOn = exceedAt.In(loc).Format("01-02")
			}
		}
	}
	return dailyAvg, exceedsOn, projected, true, nil
}

func cdtUsageCopy(ctx context.Context, q queryRower, accountID, scope string, tier int, usedBytes, quotaBytes int64, official *int64, officialAt *time.Time, unverified bool, now time.Time) (string, string) {
	acc := accountCopyName(ctx, q, accountID)
	sc := copyScope(scope)
	if tier >= 100 {
		title := "🔴 " + acc + " 的" + sc + "免费额度已经用完"
		body := "已用 " + copyGB(usedBytes) + " / " + copyGB(quotaBytes) + "。超出的部分会开始计费。"
		return title, body
	}
	sym := "🟠"
	if tier >= 95 {
		sym = "🔴"
	}
	title := sym + " " + acc + " 的" + sc + "免费额度已用超过 " + strconv.Itoa(tier) + "%"
	body := "已用 " + copyGB(usedBytes) + " / " + copyGB(quotaBytes)
	if official != nil && officialAt != nil {
		body += "（官方截至 " + copyTime(*officialAt, now, copyLocation(ctx, q)) + " 为 " + copyGB(*official) + "）"
	} else if official != nil && unverified {
		body += "（含本地窗口估算，官方读取值为 " + copyGB(*official) + "）"
	}
	body += "。"
	return title, body
}

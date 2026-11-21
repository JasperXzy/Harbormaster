package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func m4AlertObservationsTx(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule, now time.Time) ([]alertObservation, error) {
	out := []alertObservation{}

	swasObs, err := swasTrafficAlertsTx(ctx, tx, rules, now)
	if err != nil {
		return nil, err
	}
	out = append(out, swasObs...)

	cpuObs, err := cpuCreditLowAlertsTx(ctx, tx, rules, now)
	if err != nil {
		return nil, err
	}
	out = append(out, cpuObs...)

	if err = systemEventAlertsTx(ctx, tx, rules, now); err != nil {
		return nil, err
	}
	return out, nil
}

func swasTrafficAlertsTx(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule, now time.Time) ([]alertObservation, error) {
	rule := rules["swas_traffic_usage"]
	if !rule.Enabled {
		return nil, nil
	}
	loc := copyLocation(ctx, tx)
	cycleStart, cycleEnd := ReferenceCycle(now, loc)

	tiers := []int{80, 90, 95}
	if rule.Params.ThresholdsPercent != nil && len(*rule.Params.ThresholdsPercent) > 0 {
		tiers = slices.Clone(*rule.Params.ThresholdsPercent)
	}
	if !slices.Contains(tiers, 100) {
		tiers = append(tiers, 100)
	}
	sort.Ints(tiers)

	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.account_id,c.fetched_at,c.error_code,c.entry_present,c.usage_json
 FROM instance i JOIN account_settings a ON a.account_id=i.account_id AND a.enabled=1
 JOIN resource_snapshot rs ON rs.account_id=i.account_id AND rs.id=i.id AND rs.kind='swas' AND rs.present=1
 LEFT JOIN swas_traffic_cache c ON c.instance_id=i.id AND c.credential_version=a.credential_version AND c.plan_id=coalesce(json_extract(rs.payload_json,'$.spec'),'')
 WHERE i.kind='swas'`)
	if err != nil {
		return nil, err
	}
	type swasCandidate struct {
		instID, acctID string
		fetchedAt      *time.Time
		hasErr         bool
		present        bool
		usage          model.SWASTrafficUsage
	}
	candidates := []swasCandidate{}
	for rows.Next() {
		var instID, acctID string
		var fetched, code, raw sql.NullString
		var present sql.NullBool
		if err = rows.Scan(&instID, &acctID, &fetched, &code, &present, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		c := swasCandidate{
			instID:    instID,
			acctID:    acctID,
			fetchedAt: parseAgentTime(fetched),
			hasErr:    code.Valid,
			present:   present.Valid && present.Bool && raw.Valid,
		}
		if c.present {
			if json.Unmarshal([]byte(raw.String), &c.usage) != nil || !c.usage.Valid() {
				c.present = false
			}
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	observations := []alertObservation{}
	for _, c := range candidates {
		obs := alertObservation{
			rule:     "swas_traffic_usage",
			target:   c.instID + ":forecast",
			account:  c.acctID,
			instance: c.instID,
			outcome:  "unknown",
		}
		if !c.present || c.hasErr || c.fetchedAt == nil || c.fetchedAt.Before(cycleStart) || c.fetchedAt.After(now) || now.Sub(*c.fetchedAt) > model.SWASTrafficFreshness || c.usage.Total <= 0 {
			observations = append(observations, obs)
			continue
		}
		for _, tier := range tiers {
			if tier <= 0 {
				continue
			}
			limitBytes, cErr := metering.CeilProduct(c.usage.Total, int64(tier), 100)
			if cErr != nil || c.usage.Used < limitBytes {
				continue
			}
			sev := "serious"
			if tier >= 95 {
				sev = "critical"
			}
			ruleKey := "swas_traffic_usage"
			accID := c.acctID
			instID := c.instID
			name := instanceCopyName(ctx, tx, instID)
			var title, body string
			if tier >= 100 {
				title = "🔴 轻量服务器 " + name + " 的套餐流量已经用完"
				body = "已用 " + copyGB(c.usage.Used) + " / " + copyGB(c.usage.Total) + "，超出部分将单独计费。"
			} else {
				sym := "🟠"
				if tier >= 95 {
					sym = "🔴"
				}
				title = sym + " 轻量服务器 " + name + " 的套餐流量已用超过 " + strconv.Itoa(tier) + "%"
				body = "已用 " + copyGB(c.usage.Used) + " / " + copyGB(c.usage.Total) + "。"
			}
			evKey := fmt.Sprintf("swas_traffic_usage:%s:%s:%d", instID, timestamp(cycleStart), tier)
			if _, nErr := enqueueNotification(ctx, tx, model.Notification{
				EventKey:   evKey,
				Kind:       "alert",
				Severity:   sev,
				RuleKey:    &ruleKey,
				Title:      title,
				Body:       body,
				AccountID:  &accID,
				InstanceID: &instID,
				Urgent:     true,
			}, now); nErr != nil {
				return nil, nErr
			}
		}

		elapsed := now.Sub(cycleStart)
		totalDur := cycleEnd.Sub(cycleStart)
		if elapsed >= 24*time.Hour && totalDur > 0 {
			projected, pErr := metering.CeilProduct(c.usage.Used, int64(totalDur/time.Second), int64(elapsed/time.Second))
			if pErr == nil {
				if projected > c.usage.Total {
					obs.outcome = "firing"
					obs.facts = alertFacts{
						usedBytes:  c.usage.Used,
						hasUsed:    true,
						quotaBytes: c.usage.Total,
						value:      float64(projected),
					}
				} else {
					obs.outcome = "healthy"
				}
			}
		}
		observations = append(observations, obs)
	}
	return observations, nil
}

func cpuCreditLowAlertsTx(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule, now time.Time) ([]alertObservation, error) {
	rule := rules["cpu_credit_low"]
	if !rule.Enabled {
		return nil, nil
	}
	threshold := 20
	if rule.Params.ThresholdPercent != nil && *rule.Params.ThresholdPercent > 0 {
		threshold = *rule.Params.ThresholdPercent
	}
	rows, err := tx.QueryContext(ctx, `SELECT rs.id,rs.account_id,rs.payload_json
 FROM resource_snapshot rs JOIN account_settings a ON a.account_id=rs.account_id AND a.enabled=1
 WHERE rs.kind='ecs' AND rs.present=1 ORDER BY rs.id`)
	if err != nil {
		return nil, err
	}
	type ecsSnap struct {
		Status string             `json:"status"`
		CPU    *model.InstanceCPU `json:"cpu"`
	}
	observations := []alertObservation{}
	for rows.Next() {
		var instID, acctID, raw string
		if err = rows.Scan(&instID, &acctID, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var snap ecsSnap
		if json.Unmarshal([]byte(raw), &snap) != nil || snap.Status != "running" || snap.CPU == nil || snap.CPU.CreditBalance == nil || snap.CPU.CreditMax24h == nil || *snap.CPU.CreditMax24h <= 0 {
			continue
		}
		cb := *snap.CPU.CreditBalance
		cb.Age(now)
		obs := alertObservation{
			rule:     "cpu_credit_low",
			target:   instID,
			account:  acctID,
			instance: instID,
			outcome:  "unknown",
		}
		if cb.Status == "ok" && cb.Value != nil {
			pct := (*cb.Value / *snap.CPU.CreditMax24h) * 100.0
			if pct < float64(threshold) {
				obs.outcome = "firing"
				obs.facts = alertFacts{
					value:     pct,
					threshold: float64(threshold),
					limit:     copyNumber(*cb.Value) + " / " + copyNumber(*snap.CPU.CreditMax24h),
				}
			} else {
				obs.outcome = "healthy"
			}
		}
		observations = append(observations, obs)
	}
	err = rows.Err()
	rows.Close()
	return observations, err
}

func systemEventAlertsTx(ctx context.Context, tx *sql.Tx, rules map[string]model.AlertRule, now time.Time) error {
	rule := rules["system_event"]
	if !rule.Enabled {
		return nil
	}
	loc := copyLocation(ctx, tx)
	rows, err := tx.QueryContext(ctx, `SELECT e.account_id,e.region_id,e.source,e.payload_json,e.first_seen_at,
 (SELECT i.id FROM instance i WHERE i.account_id=e.account_id AND i.kind='ecs' AND i.region=e.region_id AND i.cloud_id=e.cloud_instance_id)
 FROM system_event e JOIN account_settings a ON a.account_id=e.account_id AND a.enabled=1
 WHERE e.seen_in_latest_scan=1 ORDER BY e.sequence`)
	if err != nil {
		return err
	}
	type evItem struct {
		acctID, regionID, source string
		ev                       model.CloudSystemEvent
		firstSeen                time.Time
		instID                   *string
	}
	items := []evItem{}
	for rows.Next() {
		var acctID, regionID, source, raw, firstStr string
		var instID sql.NullString
		if err = rows.Scan(&acctID, &regionID, &source, &raw, &firstStr, &instID); err != nil {
			rows.Close()
			return err
		}
		var ce model.CloudSystemEvent
		firstAt, tErr := time.Parse(time.RFC3339Nano, firstStr)
		if json.Unmarshal([]byte(raw), &ce) != nil || tErr != nil {
			continue
		}
		var ptr *string
		if instID.Valid && instID.String != "" {
			s := instID.String
			ptr = &s
		}
		items = append(items, evItem{acctID: acctID, regionID: regionID, source: source, ev: ce, firstSeen: firstAt, instID: ptr})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	ruleKey := "system_event"
	for _, it := range items {
		instLabel := it.ev.CloudInstanceID
		if it.instID != nil {
			instLabel = instanceCopyName(ctx, tx, *it.instID)
		}
		accID := it.acctID

		// Cross-source dedup & freshness policy:
		// 1. Maintenance notifications come ONLY from ecs_history in Scheduled/Executing/Inquiring (obeys quiet hours).
		// 2. Spot interruption notifications come ONLY from cms_spot published within the last 5 minutes (urgent, bypasses quiet hours).
		if it.ev.Category == "maintenance" && it.source == "ecs_history" {
			switch it.ev.ProviderStatus {
			case "Scheduled", "Executing", "Inquiring":
			default:
				continue
			}
			title := "🟡 实例 " + instLabel + " 有阿里云计划维护事件（" + it.ev.EventType + "）"
			body := "当前状态：" + it.ev.ProviderStatus + "。"
			if it.ev.NotBefore != nil {
				body += "计划开始时间：" + copyTime(*it.ev.NotBefore, now, loc) + "。"
			}
			evKey := "system_event:ecs_history:" + it.acctID + ":" + it.regionID + ":" + it.ev.CloudEventID
			if _, nErr := enqueueNotification(ctx, tx, model.Notification{
				EventKey:   evKey,
				Kind:       "alert",
				Severity:   "warning",
				RuleKey:    &ruleKey,
				Title:      title,
				Body:       body,
				AccountID:  &accID,
				InstanceID: it.instID,
				Urgent:     false,
			}, now); nErr != nil {
				return nErr
			}
		} else if it.ev.Category == "spot_interruption" && it.source == "cms_spot" {
			ref := it.firstSeen
			if it.ev.PublishedAt != nil {
				ref = *it.ev.PublishedAt
			}
			if ref.After(now) || now.Sub(ref) > 5*time.Minute {
				continue
			}
			title := "🔴 抢占式实例 " + instLabel + " 收到回收中断预警！"
			body := "事件类型：" + it.ev.EventType + "（状态 " + it.ev.ProviderStatus + "），阿里云即将回收该抢占式实例。"
			evKey := "system_event:cms_spot:" + it.acctID + ":" + it.regionID + ":" + it.ev.CloudEventID
			if _, nErr := enqueueNotification(ctx, tx, model.Notification{
				EventKey:   evKey,
				Kind:       "alert",
				Severity:   "critical",
				RuleKey:    &ruleKey,
				Title:      title,
				Body:       body,
				AccountID:  &accID,
				InstanceID: it.instID,
				Urgent:     true,
			}, now); nErr != nil {
				return nErr
			}
		}
	}
	return nil
}

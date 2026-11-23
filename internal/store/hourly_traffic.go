package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func prorateBytes(total int64, overlapMS, totalMS int64) int64 {
	if total <= 0 || overlapMS <= 0 || totalMS <= 0 {
		return 0
	}
	if overlapMS >= totalMS {
		return total
	}
	// Exact integer rounding: round(total * overlapMS / totalMS)
	num := new(big.Int).Mul(big.NewInt(total), big.NewInt(overlapMS))
	num.Mul(num, big.NewInt(2))
	num.Add(num, big.NewInt(totalMS))
	den := new(big.Int).Mul(big.NewInt(totalMS), big.NewInt(2))
	return new(big.Int).Quo(num, den).Int64()
}

func (s *Store) RefreshHourly(ctx context.Context, now time.Time) error {
	if err := s.drainHourlyEvents(ctx); err != nil {
		return err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT agent_id,hour_start FROM hourly_work WHERE pending=1 ORDER BY hour_start DESC LIMIT 96")
	if err != nil {
		return err
	}
	type workItem struct{ agentID, hour string }
	items := []workItem{}
	for rows.Next() {
		var w workItem
		if err = rows.Scan(&w.agentID, &w.hour); err != nil {
			rows.Close()
			return err
		}
		items = append(items, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, w := range items {
		if err = s.recomputeHour(ctx, w.agentID, w.hour, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) drainHourlyEvents(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,agent_id,counter_epoch,sequence FROM hourly_event ORDER BY id LIMIT 2000")
	if err != nil {
		return err
	}
	type ev struct {
		id        int64
		agent, ep string
		seq       int64
	}
	events := []ev{}
	for rows.Next() {
		var e ev
		if err = rows.Scan(&e.id, &e.agent, &e.ep, &e.seq); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	var maxID int64
	dirty := map[[2]string]bool{}
	for _, e := range events {
		if e.id > maxID {
			maxID = e.id
		}
		var curStr string
		var prevStr sql.NullString
		qErr := tx.QueryRowContext(ctx, `SELECT c.sampled_at,
 (SELECT p.sampled_at FROM traffic_sample p WHERE p.agent_id=c.agent_id AND p.counter_epoch=c.counter_epoch AND p.sequence=(SELECT max(sequence) FROM traffic_sample WHERE agent_id=c.agent_id AND counter_epoch=c.counter_epoch AND sequence<c.sequence))
 FROM traffic_sample c WHERE c.agent_id=? AND c.counter_epoch=? AND c.sequence=?`, e.agent, e.ep, e.seq).Scan(&curStr, &prevStr)
		if qErr != nil {
			continue
		}
		cur, err := time.Parse(time.RFC3339Nano, curStr)
		if err != nil {
			continue
		}
		start := cur.UTC().Truncate(time.Hour)
		end := start
		if prev := parseAgentTime(prevStr); prev != nil && prev.Before(cur) && cur.Sub(*prev) <= 48*time.Hour {
			start = prev.UTC().Truncate(time.Hour)
		}
		for h := start; !h.After(end); h = h.Add(time.Hour) {
			dirty[[2]string{e.agent, h.UTC().Format(time.RFC3339)}] = true
		}
	}
	for k := range dirty {
		if _, err = tx.ExecContext(ctx, "INSERT INTO hourly_work(agent_id,hour_start,pending) VALUES(?,?,1) ON CONFLICT(agent_id,hour_start) DO UPDATE SET pending=1", k[0], k[1]); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM hourly_event WHERE id<=?", maxID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) recomputeHour(ctx context.Context, agentID, hourStr string, now time.Time) error {
	hourStart, err := time.Parse(time.RFC3339, hourStr)
	if err != nil {
		hourStart, err = time.Parse(time.RFC3339Nano, hourStr)
		if err != nil {
			return err
		}
	}
	hourStart = hourStart.UTC().Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var instanceID string
	err = tx.QueryRowContext(ctx, "SELECT instance_id FROM agent WHERE id=?", agentID).Scan(&instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, "DELETE FROM hourly_work WHERE agent_id=? AND hour_start=?", agentID, hourStr); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var rxTotal, txTotal, coveredMS int64
	var samples int
	prorated := 0
	err = walkDailyIntervals(ctx, tx, agentID, hourStart, hourEnd, now.Add(time.Hour), func(iv metering.Interval) error {
		if !iv.End.After(hourStart) || !iv.Start.Before(hourEnd) {
			return nil
		}
		totalMS := iv.End.Sub(iv.Start).Milliseconds()
		if totalMS <= 0 {
			return nil
		}
		ovStart := maxTime(iv.Start, hourStart)
		ovEnd := earlierTime(iv.End, hourEnd)
		ovMS := ovEnd.Sub(ovStart).Milliseconds()
		if ovMS <= 0 {
			return nil
		}
		samples++
		coveredMS += ovMS
		if iv.Start.Before(hourStart) || iv.End.After(hourEnd) || iv.Gap {
			prorated = 1
		}
		rxTotal += prorateBytes(iv.RX, ovMS, totalMS)
		txTotal += prorateBytes(iv.TX, ovMS, totalMS)
		return nil
	})
	if err != nil {
		return err
	}
	if coveredMS > 3600_000 {
		coveredMS = 3600_000
	}
	if samples > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_hourly_traffic(agent_id,instance_id,hour_start,rx_bytes,tx_bytes,covered_ms,sample_count,prorated,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?)
 ON CONFLICT(agent_id,hour_start) DO UPDATE SET instance_id=excluded.instance_id,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes,covered_ms=excluded.covered_ms,sample_count=excluded.sample_count,prorated=excluded.prorated,updated_at=excluded.updated_at`,
			agentID, instanceID, hourStart.Format(time.RFC3339), rxTotal, txTotal, coveredMS, samples, prorated, timestamp(now)); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, "DELETE FROM agent_hourly_traffic WHERE agent_id=? AND hour_start=?", agentID, hourStart.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE hourly_work SET pending=0 WHERE agent_id=? AND hour_start=?", agentID, hourStr); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentHourlyHistory(ctx context.Context, instanceID string, from, to time.Time) ([]model.AgentHourlyBucket, error) {
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", instanceID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	fromH := from.UTC().Truncate(time.Hour)
	toH := to.UTC()
	rows, err := s.DB.QueryContext(ctx, `SELECT hour_start, sum(rx_bytes), sum(tx_bytes), max(covered_ms), sum(sample_count), max(prorated)
 FROM agent_hourly_traffic WHERE instance_id=? AND hour_start>=? AND hour_start<?
 GROUP BY hour_start ORDER BY hour_start ASC`, instanceID, fromH.Format(time.RFC3339), toH.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AgentHourlyBucket{}
	for rows.Next() {
		var hStr string
		var rx, tx, covMS int64
		var count, prorated int
		if err = rows.Scan(&hStr, &rx, &tx, &covMS, &count, &prorated); err != nil {
			return nil, err
		}
		h, err := time.Parse(time.RFC3339, hStr)
		if err != nil {
			continue
		}
		sec := int((covMS + 500) / 1000)
		if sec <= 0 {
			sec = 1
		}
		if sec > 3600 {
			sec = 3600
		}
		out = append(out, model.AgentHourlyBucket{
			HourStart:     h.UTC(),
			RXBytes:       rx,
			TXBytes:       tx,
			RXRate:        math.Round(float64(rx)/float64(sec)*100) / 100,
			TXRate:        math.Round(float64(tx)/float64(sec)*100) / 100,
			CoveredSecond: sec,
			SampleCount:   count,
			Estimated:     prorated == 1 || covMS < 3540_000,
		})
	}
	return out, rows.Err()
}

type hourPair struct{ rx, tx int64 }

// instanceHourlyWindow returns per-instance UTC-hour RX/TX maps over [from, to),
// combining rolled-up agent_hourly_traffic with live un-rolled-up samples in the
// current open/pending hours so UI attribution is always up-to-the-second.
func (s *Store) instanceHourlyWindow(ctx context.Context, instanceIDs []string, from, to, now time.Time) (map[string]map[time.Time]hourPair, *time.Time, error) {
	result := map[string]map[time.Time]hourPair{}
	if len(instanceIDs) == 0 || !to.After(from) {
		return result, nil, nil
	}
	marks := make([]string, len(instanceIDs))
	args := make([]any, 0, len(instanceIDs)+2)
	for i, id := range instanceIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	fromUTC := from.UTC().Truncate(time.Hour)
	toUTC := to.UTC()
	args = append(args, fromUTC.Format(time.RFC3339), toUTC.Format(time.RFC3339))
	rows, err := s.DB.QueryContext(ctx, `SELECT instance_id,hour_start,sum(rx_bytes),sum(tx_bytes),max(updated_at)
 FROM agent_hourly_traffic WHERE instance_id IN (`+strings.Join(marks, ",")+`) AND hour_start>=? AND hour_start<?
 GROUP BY instance_id,hour_start`, args...)
	if err != nil {
		return nil, nil, err
	}
	var latest *time.Time
	for rows.Next() {
		var instID, hStr, upStr string
		var rx, tx int64
		if err = rows.Scan(&instID, &hStr, &rx, &tx, &upStr); err != nil {
			rows.Close()
			return nil, nil, err
		}
		h, err := time.Parse(time.RFC3339, hStr)
		if err != nil {
			continue
		}
		if up, err := time.Parse(time.RFC3339Nano, upStr); err == nil && (latest == nil || up.After(*latest)) {
			t := up
			latest = &t
		}
		if result[instID] == nil {
			result[instID] = map[time.Time]hourPair{}
		}
		result[instID][h.UTC()] = hourPair{rx: rx, tx: tx}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}

	// Overlay live tail for the last 2 UTC hours directly from traffic_sample so
	// fresh 10s reports appear immediately before the 30s background rollup runs.
	tailStart := maxTime(fromUTC, now.UTC().Truncate(time.Hour).Add(-time.Hour))
	if tailStart.Before(toUTC) {
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err == nil {
			defer tx.Rollback()
			for _, instID := range instanceIDs {
				var agentID string
				if qErr := tx.QueryRowContext(ctx, "SELECT id FROM agent WHERE instance_id=? AND revoked_at IS NULL", instID).Scan(&agentID); qErr != nil {
					continue
				}
				for h := tailStart; h.Before(toUTC); h = h.Add(time.Hour) {
					if result[instID] != nil {
						delete(result[instID], h)
					}
				}
				_ = walkDailyIntervals(ctx, tx, agentID, tailStart, toUTC, now.Add(time.Minute), func(iv metering.Interval) error {
					if !iv.End.After(tailStart) || !iv.Start.Before(toUTC) {
						return nil
					}
					totalMS := iv.End.Sub(iv.Start).Milliseconds()
					if totalMS <= 0 {
						return nil
					}
					if latest == nil || iv.End.After(*latest) {
						end := iv.End
						latest = &end
					}
					for h := iv.Start.UTC().Truncate(time.Hour); h.Before(iv.End) && h.Before(toUTC); h = h.Add(time.Hour) {
						if h.Before(tailStart) {
							continue
						}
						hEnd := h.Add(time.Hour)
						ovS := maxTime(iv.Start, h)
						ovE := earlierTime(iv.End, earlierTime(hEnd, toUTC))
						ovMS := ovE.Sub(ovS).Milliseconds()
						if ovMS <= 0 {
							continue
						}
						if result[instID] == nil {
							result[instID] = map[time.Time]hourPair{}
						}
						cur := result[instID][h]
						cur.rx += prorateBytes(iv.RX, ovMS, totalMS)
						cur.tx += prorateBytes(iv.TX, ovMS, totalMS)
						result[instID][h] = cur
					}
					return nil
				})
			}
		}
	}
	return result, latest, nil
}

func subjectMemberIDs(item map[string]any, kind string) []string {
	ids := []string{}
	if kind == "subject" {
		switch raw := item["instance_ids"].(type) {
		case []any:
			for _, v := range raw {
				if s, ok := v.(string); ok && s != "" {
					ids = append(ids, s)
				}
			}
		case []string:
			ids = append(ids, raw...)
		}
	} else if kind == "package" {
		if members, ok := item["members"].([]any); ok {
			for _, m := range members {
				if mp, ok := m.(map[string]any); ok {
					if s, ok := mp["instance_id"].(string); ok && s != "" {
						ids = append(ids, s)
					}
				}
			}
		}
	}
	return ids
}

func aggregateSubjectRange(members []string, hourly map[string]map[time.Time]hourPair, from, to time.Time, rule string) (*int64, *int64, *int64) {
	byHour := map[time.Time]hourPair{}
	hasAny := false
	for _, id := range members {
		m := hourly[id]
		for h, p := range m {
			if !h.Before(from) && h.Before(to) {
				hasAny = true
				cur := byHour[h]
				cur.rx += p.rx
				cur.tx += p.tx
				byHour[h] = cur
			}
		}
	}
	if !hasAny {
		return nil, nil, nil
	}
	var rx, tx, metered int64
	for _, p := range byHour {
		rx += p.rx
		tx += p.tx
		switch rule {
		case "outbound_only":
			metered += p.tx
		case "max_in_out_per_period":
			metered += max(p.rx, p.tx)
		}
	}
	if rule != "outbound_only" && rule != "max_in_out_per_period" {
		return &rx, &tx, nil
	}
	return &rx, &tx, &metered
}

func (s *Store) OverlaySubjectAndPackageTraffic(ctx context.Context, items []map[string]any, kind string, loc *time.Location, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	memberSet := map[string]bool{}
	for _, item := range items {
		for _, id := range subjectMemberIDs(item, kind) {
			memberSet[id] = true
		}
	}
	allMembers := make([]string, 0, len(memberSet))
	for id := range memberSet {
		allMembers = append(allMembers, id)
	}
	cycleStart, _ := ReferenceCycle(now, loc)
	localNow := now.In(loc)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).UTC()
	hourly, _, err := s.instanceHourlyWindow(ctx, allMembers, cycleStart, now, now)
	if err != nil {
		return err
	}

	// Track per-(account, cdt_scope) cycle metered sum for share_percent
	scopeTotals := map[[2]string]int64{}
	for _, item := range items {
		members := subjectMemberIDs(item, kind)
		rule, _ := item["metering_rule"].(string)
		if kind == "package" {
			pkgKind, _ := item["kind"].(string)
			if pkgKind == "cbwp_dominant_traffic" {
				rule = "max_in_out_per_period"
			} else {
				rule = "not_metered_by_traffic"
			}
		}
		tRX, tTX, tMet := aggregateSubjectRange(members, hourly, todayStart, now, rule)
		cRX, cTX, cMet := aggregateSubjectRange(members, hourly, cycleStart, now, rule)
		item["today_rx_bytes"] = tRX
		item["today_tx_bytes"] = tTX
		item["today_metered_bytes"] = tMet
		item["cycle_rx_bytes"] = cRX
		item["cycle_tx_bytes"] = cTX
		item["cycle_metered_bytes"] = cMet
		if kind == "subject" {
			if cRX != nil || tRX != nil {
				item["data_source"] = "agent"
			}
			accID, _ := item["account_id"].(string)
			scope, _ := item["cdt_scope"].(string)
			eligible, _ := item["counts_toward_cdt"].(bool)
			if eligible && scope != "" && cMet != nil && *cMet > 0 {
				scopeTotals[[2]string{accID, scope}] += *cMet
			}
			item["cycle_share_percent"] = nil
		} else if kind == "package" {
			if tRX != nil && tTX != nil && (*tRX > 0 || *tTX > 0) {
				if *tRX >= *tTX {
					item["dominant_direction"] = "inbound"
				} else {
					item["dominant_direction"] = "outbound"
				}
			}
		}
	}
	if kind == "subject" {
		for _, item := range items {
			accID, _ := item["account_id"].(string)
			scope, _ := item["cdt_scope"].(string)
			eligible, _ := item["counts_toward_cdt"].(bool)
			cMet, _ := item["cycle_metered_bytes"].(*int64)
			if eligible && scope != "" && cMet != nil {
				denom := scopeTotals[[2]string{accID, scope}]
				if denom > 0 {
					pct := math.Round(float64(*cMet)/float64(denom)*1000) / 10
					item["cycle_share_percent"] = pct
				} else if *cMet == 0 {
					zero := 0.0
					item["cycle_share_percent"] = zero
				}
			}
		}
	}
	return nil
}

func (s *Store) BandwidthPackageHourly(ctx context.Context, pkg map[string]any, loc *time.Location, now time.Time) ([]model.BandwidthHourlyPeriod, *time.Time, error) {
	members := subjectMemberIDs(pkg, "package")
	localNow := now.In(loc)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).UTC()
	hourly, latest, err := s.instanceHourlyWindow(ctx, members, todayStart, now, now)
	if err != nil {
		return nil, nil, err
	}
	pkgKind, _ := pkg["kind"].(string)
	byHour := map[time.Time]hourPair{}
	for _, id := range members {
		for h, p := range hourly[id] {
			if !h.Before(todayStart) && h.Before(now) {
				cur := byHour[h]
				cur.rx += p.rx
				cur.tx += p.tx
				byHour[h] = cur
			}
		}
	}
	hours := make([]time.Time, 0, len(byHour))
	for h := range byHour {
		hours = append(hours, h)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	periods := make([]model.BandwidthHourlyPeriod, 0, len(hours))
	for _, h := range hours {
		p := byHour[h]
		met := max(p.rx, p.tx)
		if pkgKind != "cbwp_dominant_traffic" {
			met = p.tx
		}
		periods = append(periods, model.BandwidthHourlyPeriod{
			Start:        h,
			RXBytes:      p.rx,
			TXBytes:      p.tx,
			MeteredBytes: met,
		})
	}
	return periods, latest, nil
}

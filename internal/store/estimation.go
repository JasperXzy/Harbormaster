package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
)

type meteringSubject struct {
	ID        string   `json:"id"`
	Region    string   `json:"-"`
	Rule      string   `json:"metering_rule"`
	Eligible  *bool    `json:"counts_toward_cdt"`
	Scope     *string  `json:"cdt_scope"`
	Members   []string `json:"instance_ids"`
	Bandwidth *int64   `json:"bandwidth_mbps"`
	Empty     bool     `json:"-"`
}
type meteringAgent struct {
	ID, Status         string
	At, Received       time.Time
	Available, Offline bool
}

// RefreshEstimates is local-only. A transaction gives each result one coherent
// account, basis, topology, settings and sample view; no GET performs this work.
func (s *Store) RefreshEstimates(ctx context.Context, loc *time.Location, now time.Time) error {
	accounts, err := s.Accounts(ctx)
	if err != nil {
		return err
	}
	var firstError error
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = func() error {
			defer tx.Rollback()
			current, e := loadAccount(ctx, tx, a.ID)
			if e != nil {
				return e
			}
			if !current.Enabled {
				return nil
			}
			settings, e := readEstimationSettings(ctx, tx)
			if e != nil {
				return e
			}
			for _, scope := range []string{"mainland", "overseas"} {
				if e = reconcileCDT(ctx, tx, current, scope, settings, loc, now); e != nil {
					return e
				}
				v, e := calculateEstimate(ctx, tx, current, scope, settings, loc, now)
				if e != nil {
					return e
				}
				raw, e := json.Marshal(v)
				if e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `INSERT INTO traffic_estimate(account_id,scope,credential_version,account_revision,settings_revision,basis_id,calculated_at,payload_json) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(account_id,scope) DO UPDATE SET credential_version=excluded.credential_version,account_revision=excluded.account_revision,settings_revision=excluded.settings_revision,basis_id=excluded.basis_id,calculated_at=excluded.calculated_at,payload_json=excluded.payload_json`, a.ID, scope, current.CredentialVersion, current.Revision, settings.Revision, v.BasisID, timestamp(now), string(raw)); e != nil {
					return e
				}
			}
			return tx.Commit()
		}()
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}
func calculateEstimate(ctx context.Context, tx *sql.Tx, a model.Account, scope string, settings model.EstimationSettings, loc *time.Location, now time.Time) (model.TrafficEstimation, error) {
	return calculateEstimateAt(ctx, tx, a, scope, settings, loc, now, nil, time.Time{}, false)
}

// Historical reconciliation supplies a trusted earlier basis and a fixed
// cutoff. It forbids projections and checks exact coverage at that cutoff.
func calculateEstimateAt(ctx context.Context, tx *sql.Tx, a model.Account, scope string, settings model.EstimationSettings, loc *time.Location, now time.Time, supplied *model.CDTObservation, cutoff time.Time, actualOnly bool) (model.TrafficEstimation, error) {
	v := model.TrafficEstimation{Mode: "unverified", Status: "unavailable", To: now, CalculatedAt: now, LagSeconds: settings.LagSeconds, Reasons: []string{}}
	if !cutoff.IsZero() {
		v.To = cutoff
	}
	reasons := map[string]bool{}
	qualified := true
	complete := true
	rateKnown := true
	reason := func(s string) { reasons[s] = true }
	finish := func() (model.TrafficEstimation, error) {
		for r := range reasons {
			v.Reasons = append(v.Reasons, r)
		}
		sort.Strings(v.Reasons)
		v.Qualified = qualified && complete && rateKnown && v.Mode == "aligned" && v.Status == "ready"
		return v, nil
	}
	start, end := ReferenceCycle(now, loc)
	column := "mainland_bytes"
	if scope == "overseas" {
		column = "overseas_bytes"
	}
	var basis model.CDTObservation
	var err error
	if supplied != nil {
		basis = *supplied
	} else {
		basis, err = readObservation(tx.QueryRowContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND fetched_at>=? AND fetched_at<? AND fetched_at<=? AND error_code IS NULL AND "+column+" IS NOT NULL ORDER BY id DESC LIMIT 1", a.ID, a.CredentialVersion, timestamp(start), timestamp(end), timestamp(now)))
		if errors.Is(err, sql.ErrNoRows) {
			reason("official_basis_missing")
			return finish()
		}
		if err != nil {
			return v, err
		}
	}

	v.BasisID = basis.ID
	base := basis.Mainland
	if scope == "overseas" {
		base = basis.Overseas
	}
	if base == nil {
		reason("official_basis_missing")
		return finish()
	}
	from := basis.FetchedAt.Add(-time.Duration(settings.LagSeconds) * time.Second)
	if from.Before(start) {
		from = start
	}
	if basis.Quality == "verified" && basis.CoveredUntil != nil && basis.PeriodStart != nil && basis.PeriodEnd != nil && basis.PeriodStart.Equal(start) && basis.PeriodEnd.Equal(end) && !basis.CoveredUntil.After(now) {
		v.Mode = "aligned"
		from = *basis.CoveredUntil
	} else {
		qualified = false
		reason("official_cutoff_or_unit_unverified")
		reason("lag_window_assumption")
	}
	v.From = &from
	if !v.To.After(from) || v.To.Sub(from) > 32*24*time.Hour {
		reason("invalid_estimation_window")
		return finish()
	}
	// Every configured region in this CDT scope needs complete topology, including regions
	// with no metered objects. Observation stability never verifies historical membership.
	for _, region := range a.RegionIDs {
		if model.CDTRegionScope(region) != scope {
			continue
		}
		var since, last, quality string
		var ok bool
		var credential int64
		err = tx.QueryRowContext(ctx, "SELECT observed_since,last_seen,complete,credential_version,quality FROM metering_topology WHERE account_id=? AND region=?", a.ID, region).Scan(&since, &last, &ok, &credential, &quality)
		if errors.Is(err, sql.ErrNoRows) {
			complete = false
			qualified = false
			reason("topology_unknown")
			continue
		}
		if err != nil {
			return v, err
		}
		at, e := time.Parse(time.RFC3339Nano, last)
		if e != nil {
			return v, e
		}
		first, e := time.Parse(time.RFC3339Nano, since)
		if e != nil {
			return v, e
		}
		if !ok || credential != a.CredentialVersion || at.After(now) || now.Sub(at) > meteringTopologyFreshness || first.After(from) {
			complete = false
			reason("topology_history_incomplete")
		}
		if quality != "verified" {
			qualified = false
			reason("topology_observed_only")
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT region,payload_json FROM resource_snapshot WHERE account_id=? AND kind='subject' AND present=1 ORDER BY id", a.ID)
	if err != nil {
		return v, err
	}
	subjects := []meteringSubject{}
	counts := map[string]int{}
	for rows.Next() {
		var region, raw string
		if err = rows.Scan(&region, &raw); err != nil {
			rows.Close()
			return v, err
		}
		if !slices.Contains(a.RegionIDs, region) || model.CDTRegionScope(region) != scope {
			continue
		}
		var observed dailySubject
		if err = json.Unmarshal([]byte(raw), &observed); err != nil {
			rows.Close()
			return v, err
		}
		sub := observed.meteringSubject
		sub.Empty = observed.knownEmpty()
		if sub.Eligible != nil && !*sub.Eligible {
			continue
		}
		if sub.Scope == nil {
			complete = false
			qualified = false
			reason("scope_unknown")
			continue
		}
		if *sub.Scope != scope {
			continue
		}
		sub.Region = region
		subjects = append(subjects, sub)
		for _, id := range sub.Members {
			counts[id]++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	agents := map[string]meteringAgent{}
	for id := range counts {
		var m meteringAgent
		var at, received, revoked, registered, quality sql.NullString
		var agentID sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT a.id,i.status,c.sampled_at,c.received_at,a.revoked_at,a.registered_at,c.quality FROM instance i LEFT JOIN agent a ON a.instance_id=i.id LEFT JOIN agent_counter_state c ON c.agent_id=a.id WHERE i.id=? AND i.account_id=?`, id, a.ID).Scan(&agentID, &m.Status, &at, &received, &revoked, &registered, &quality)
		if errors.Is(err, sql.ErrNoRows) {
			complete = false
			reason("member_missing")
			continue
		}
		if err != nil {
			return v, err
		}
		m.ID = agentID.String

		if parsed := parseAgentTime(at); parsed != nil {
			m.At = *parsed
		}
		if parsed := parseAgentTime(received); parsed != nil {
			m.Received = *parsed
		}
		m.Available = m.ID != "" && !revoked.Valid && !m.At.IsZero() && !m.Received.IsZero() && !m.At.After(now) && !m.Received.After(now) && quality.Valid && quality.String != "clock_skew" && (parseAgentTime(registered) == nil || !m.Received.Before(*parseAgentTime(registered)))
		m.Offline = m.Available && (now.Sub(m.At) >= 30*time.Second || now.Sub(m.Received) >= 30*time.Second)
		if !actualOnly && m.Available && !m.Offline && m.At.Before(v.To) {
			v.To = m.At
		}
		agents[id] = m
	}
	if !v.To.After(from) {
		reason("samples_not_past_basis")
		return finish()
	}
	var total, worst, current int64
	for _, sub := range subjects {
		rule := ""
		switch sub.Rule {
		case "outbound_only":
			rule = metering.Outbound
		case "max_in_out_per_period":
			rule = metering.HourlyDominant
		default:
			complete = false
			qualified = false
			reason("metering_rule_unknown")
			continue
		}
		if sub.Eligible == nil {
			qualified = false
			reason("cdt_eligibility_unknown")
		}
		if len(sub.Members) == 0 {
			if sub.Empty {
				known, e := emptySubjectWindow(ctx, tx, a, sub, from, now)
				if e != nil {
					return v, e
				}
				if known {
					qualified = false
					reason("topology_observed_only")
					reason("empty_bandwidth_package_observed")
					continue
				}
			}
			complete = false
			rateKnown = false
			reason("unattributed_subject")
			continue
		}
		acc, e := metering.NewAccumulator(rule, from, v.To)
		if e != nil {
			return v, e
		}
		rates := metering.Rate{}
		for _, id := range sub.Members {
			if e = acc.AddMember(id, time.Time{}, time.Time{}); e != nil {
				return v, e
			}
			m := agents[id]
			if counts[id] > 1 {
				complete = false
				qualified = false
				rateKnown = false
				reason("traffic_attribution_ambiguous")
				continue
			}
			if !m.Available {
				complete = false
				rateKnown = false
				reason("agent_data_unavailable")
				continue
			}
			recent := []metering.Interval{}
			var last *metering.Interval
			walkFrom, walkUntil := earlierTime(from, m.At.Add(-10*time.Minute)), m.At
			if actualOnly {
				walkFrom = from
				walkUntil = v.To
			}
			e = walkMeteringIntervals(ctx, tx, m.ID, walkFrom, walkUntil, func(interval metering.Interval) error {
				// Boundary-crossing current samples may end after the common watermark.
				copy := interval
				last = &copy
				if interval.End.After(m.At.Add(-10 * time.Minute)) {
					recent = append(recent, interval)
				}
				return acc.Add(id, interval)
			})
			if e != nil {
				if errors.Is(e, metering.ErrInput) {
					complete = false
					rateKnown = false
					reason("invalid_sample_timing")
					continue
				}
				return v, e
			}
			if actualOnly {
				continue
			}
			var r metering.Rate
			if m.Offline && m.Status == "running" {
				r, _, e = metering.OfflineRate(recent, m.At, 10*time.Minute, sub.Bandwidth, slices.Contains(settings.BandwidthIDs, id))
				if e != nil {
					if errors.Is(e, metering.ErrBandwidthUnknown) {
						complete = false
						rateKnown = false
						reason("public_bandwidth_unknown")
						continue
					}
					return v, e
				}
				if m.At.Before(v.To) {
					if e = acc.Add(id, metering.Interval{Start: m.At, End: v.To, Projection: &r}); e != nil {
						return v, e
					}
				}
			} else if last != nil && !last.Gap && last.End.Equal(m.At) {
				duration := last.DurationMS
				if duration <= 0 {
					duration = last.End.Sub(last.Start).Milliseconds()
				}
				r.RX, e = metering.CeilProduct(last.RX, 1000, duration)
				if e != nil {
					return v, e
				}
				r.TX, e = metering.CeilProduct(last.TX, 1000, duration)
				if e != nil {
					return v, e
				}
			} else {
				rateKnown = false
				reason("current_rate_unknown")
			}
			if rates.RX, e = addMetered(rates.RX, r.RX); e != nil {
				return v, e
			}
			if rates.TX, e = addMetered(rates.TX, r.TX); e != nil {
				return v, e
			}
		}
		result, e := acc.Result()
		if e != nil {
			return v, e
		}
		if !result.Complete {
			complete = false
		}
		for _, r := range result.Reasons {
			reason(r)
		}
		if total, e = addMetered(total, result.KnownBytes); e != nil {
			return v, e
		}
		if worst, e = addMetered(worst, result.WorstCase); e != nil {
			return v, e
		}
		rate := rates.TX
		if rule == metering.HourlyDominant {
			rate = max(rates.RX, rates.TX)
		}
		if current, e = addMetered(current, rate); e != nil {
			return v, e
		}
	}
	v.KnownIncrement = total
	v.Status = "incomplete"
	if rateKnown {
		v.CurrentRate = &current
	}
	if complete {
		v.Status = "ready"
		v.Increment = &total
		v.WorstCase = &worst
		estimated, e := addMetered(*base, total)
		if e != nil {
			return v, e
		}
		v.Estimated = &estimated
	}
	return finish()
}
func earlierTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func addMetered(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > int64(^uint64(0)>>1)-b {
		return 0, metering.ErrOverflow
	}
	return a + b, nil
}

// Stream raw deltas, using indexed same-epoch predecessors even before from.
// No raw month-sized slice is retained by the estimator.
func walkMeteringIntervals(ctx context.Context, tx *sql.Tx, id string, from, until time.Time, visit func(metering.Interval) error) error {
	rows, err := tx.QueryContext(ctx, `SELECT c.counter_epoch,c.sequence,c.sampled_at,c.uptime_ms,c.rx_bytes,c.tx_bytes,d.quality,d.rules_version,d.report_interval_seconds,d.dropped_samples,
 p.sequence,p.sampled_at,p.uptime_ms,p.rx_bytes,p.tx_bytes,pd.quality,pd.rules_version,
 (SELECT sampled_at FROM traffic_sample pp WHERE pp.agent_id=p.agent_id AND pp.counter_epoch=p.counter_epoch AND pp.sequence=(SELECT max(sequence) FROM traffic_sample WHERE agent_id=p.agent_id AND counter_epoch=p.counter_epoch AND sequence<p.sequence))
 FROM traffic_sample c LEFT JOIN traffic_sample_detail d ON d.agent_id=c.agent_id AND d.counter_epoch=c.counter_epoch AND d.sequence=c.sequence
 LEFT JOIN traffic_sample p ON p.agent_id=c.agent_id AND p.counter_epoch=c.counter_epoch AND p.sequence=(SELECT max(sequence) FROM traffic_sample WHERE agent_id=c.agent_id AND counter_epoch=c.counter_epoch AND sequence<c.sequence)
 LEFT JOIN traffic_sample_detail pd ON pd.agent_id=p.agent_id AND pd.counter_epoch=p.counter_epoch AND pd.sequence=p.sequence
 WHERE c.agent_id=? AND c.sampled_at>? AND c.sampled_at<=? ORDER BY c.sampled_at,c.counter_epoch,c.sequence`, id, timestamp(from), timestamp(until))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v model.AgentNetworkSample
		var at string
		var q, rules, pat, pq, pr, ppa sql.NullString
		var cadence, dropped, seq, uptime, rx, txb sql.NullInt64
		if err = rows.Scan(&v.CounterEpoch, &v.Sequence, &at, &v.UptimeMS, &v.RXBytes, &v.TXBytes, &q, &rules, &cadence, &dropped, &seq, &pat, &uptime, &rx, &txb, &pq, &pr, &ppa); err != nil {
			return err
		}
		if v.SampledAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return err
		}
		v.Quality = q.String
		if rules.Valid {
			v.RulesVersion = &rules.String
		}
		if cadence.Valid {
			n := int(cadence.Int64)
			v.ReportIntervalSeconds = &n
		}
		if dropped.Valid {
			v.DroppedSamples = &dropped.Int64
		}
		deriveAgentHistoryInterval(&v, agentHistoryPrevious{Sequence: seq, Uptime: uptime, RX: rx, TX: txb, At: parseAgentTime(pat), Quality: pq, Rules: pr, PreviousAt: parseAgentTime(ppa)})
		if v.Interval != nil {
			x := v.Interval
			if err = visit(metering.Interval{Start: x.StartAt, End: x.EndAt, RX: x.RXBytes, TX: x.TXBytes, DurationMS: x.DurationMS, Gap: v.IntervalStatus == "gap"}); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

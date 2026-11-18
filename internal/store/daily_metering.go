package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func pendingDailyScope(from, until time.Time) model.DailyScope {
	return model.DailyScope{Source: "unavailable", Status: "pending", From: from, Until: until, Reasons: []string{"aggregation_pending"}}
}

// The first successor can close the last interval across midnight. Counts are
// conservative upper allocations, never interpolated uniformly across a day.
func walkDailyIntervals(ctx context.Context, tx *sql.Tx, id string, from, until, available time.Time, visit func(metering.Interval) error) error {
	var next sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT min(sampled_at) FROM traffic_sample WHERE agent_id=? AND sampled_at>? AND sampled_at<=?", id, timestamp(until), timestamp(available)).Scan(&next); err != nil {
		return err
	}
	end := until
	if at := parseAgentTime(next); at != nil {
		end = *at
	}
	return walkMeteringIntervals(ctx, tx, id, from, end, func(v metering.Interval) error {
		if !v.Start.Before(until) {
			return nil
		}
		return visit(v)
	})
}

func officialDaily(ctx context.Context, tx *sql.Tx, a model.Account, scope string, from, until time.Time, loc *time.Location) (*int64, error) {
	start, end := ReferenceCycle(from, loc)
	column := "mainland_bytes"
	if scope == "overseas" {
		column = "overseas_bytes"
	}
	// Exact verified cutoffs, equal confirmed period/source and monotonic values.
	var value int64
	err := tx.QueryRowContext(ctx, `SELECT e.`+column+`-b.`+column+` FROM cdt_observation b JOIN cdt_observation e ON e.account_id=b.account_id AND e.credential_version=b.credential_version AND e.source=b.source AND e.period_start=b.period_start AND e.period_end=b.period_end
 WHERE b.account_id=? AND b.credential_version=? AND b.source_quality='verified' AND e.source_quality='verified' AND b.error_code IS NULL AND e.error_code IS NULL AND b.covered_until=? AND e.covered_until=? AND b.period_start=? AND b.period_end=? AND e.`+column+`>=b.`+column+` ORDER BY e.id DESC,b.id DESC LIMIT 1`, a.ID, a.CredentialVersion, timestamp(from), timestamp(until), timestamp(start), timestamp(end)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

type dailyGraphWindow struct {
	from, until time.Time
	subjects    []dailySubject
}

func calculateDailyScope(ctx context.Context, tx *sql.Tx, a model.Account, scope string, day, end, now time.Time, loc *time.Location) (model.DailyScope, error) {
	until := earlierTime(end, now)
	v := pendingDailyScope(day, until)
	v.Status = "unavailable"
	v.Reasons = []string{}
	if !until.After(day) {
		return v, nil
	}
	if !end.After(now) {
		o, err := officialDaily(ctx, tx, a, scope, day, end, loc)
		if err != nil {
			return v, err
		}
		if o != nil {
			v.Bytes = o
			v.KnownBytes = *o
			v.HasData = true
			v.Source = "official"
			v.Status = "complete"
			v.CompleteDay = true
			v.ObservedFrom = &day
			v.ObservedUntil = &end
			return v, nil
		}
	}
	reasons := map[string]bool{"topology_observed_only": true}
	complete := len(a.RegionIDs) > 0
	for _, region := range a.RegionIDs {
		if model.CDTRegionScope(region) != scope {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT observed_from,last_seen,closed_at,subjects_json FROM daily_topology WHERE account_id=? AND credential_version=? AND region=? AND observed_from<? AND (closed_at IS NULL OR closed_at>?) ORDER BY observed_from,id`, a.ID, a.CredentialVersion, region, timestamp(until), timestamp(day))
		if err != nil {
			return v, err
		}
		windows := []dailyGraphWindow{}
		for rows.Next() {
			var from, last, raw string
			var closed sql.NullString
			if err = rows.Scan(&from, &last, &closed, &raw); err != nil {
				rows.Close()
				return v, err
			}
			f, _ := time.Parse(time.RFC3339Nano, from)
			to, _ := time.Parse(time.RFC3339Nano, last)
			if closed.Valid {
				to, _ = time.Parse(time.RFC3339Nano, closed.String)
			} else {
				to = earlierTime(to.Add(meteringTopologyFreshness), now)
			}
			f = maxTime(f, day)
			to = earlierTime(to, until)
			if !to.After(f) {
				continue
			}
			w := dailyGraphWindow{from: f, until: to}
			if err = json.Unmarshal([]byte(raw), &w.subjects); err != nil {
				rows.Close()
				return v, err
			}
			windows = append(windows, w)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return v, err
		}
		cursor := day
		for _, w := range windows {
			if w.from.After(cursor) {
				complete = false
				reasons["topology_history_incomplete"] = true
			}
			cursor = maxTime(cursor, w.until)
			counts := map[string]int{}
			for _, sub := range w.subjects {
				if sub.Eligible != nil && !*sub.Eligible {
					continue
				}
				if sub.Scope == nil {
					complete = false
					reasons["scope_unknown"] = true
					continue
				}
				if *sub.Scope == scope {
					for _, id := range sub.Members {
						counts[id]++
					}
				}
			}
			for _, sub := range w.subjects {
				if sub.Eligible != nil && !*sub.Eligible || sub.Scope == nil || *sub.Scope != scope {
					continue
				}
				if sub.Eligible == nil {
					reasons["cdt_eligibility_unknown"] = true
				}
				if len(sub.Members) == 0 {
					if sub.knownEmpty() {
						v.HasData = true
						continue
					}
					complete = false
					reasons["unattributed_subject"] = true
					continue
				}
				rule := ""
				switch sub.Rule {
				case "outbound_only":
					rule = metering.Outbound
				case "max_in_out_per_period":
					rule = metering.HourlyDominant
				default:
					complete = false
					reasons["metering_rule_unknown"] = true
					continue
				}
				acc, err := metering.NewAccumulator(rule, w.from, w.until)
				if err != nil {
					return v, err
				}
				for _, id := range sub.Members {
					if err = acc.AddMember(id, time.Time{}, time.Time{}); err != nil {
						return v, err
					}
					if counts[id] > 1 {
						complete = false
						reasons["member_ambiguous"] = true
						continue
					}
					var agent string
					err = tx.QueryRowContext(ctx, "SELECT ag.id FROM agent ag JOIN instance i ON i.id=ag.instance_id WHERE i.id=? AND i.account_id=?", id, a.ID).Scan(&agent)
					if errors.Is(err, sql.ErrNoRows) {
						complete = false
						reasons["member_missing"] = true
						continue
					}
					if err != nil {
						return v, err
					}
					err = walkDailyIntervals(ctx, tx, agent, w.from, w.until, now, func(x metering.Interval) error {
						if !x.End.After(w.from) || !x.Start.Before(w.until) {
							return nil
						}
						f, to := maxTime(x.Start, w.from), earlierTime(x.End, w.until)
						v.HasData = true
						if v.ObservedFrom == nil || f.Before(*v.ObservedFrom) {
							v.ObservedFrom = &f
						}
						if v.ObservedUntil == nil || to.After(*v.ObservedUntil) {
							v.ObservedUntil = &to
						}
						return acc.Add(id, x)
					})
					if err != nil {
						return v, err
					}
				}
				result, err := acc.Result()
				if err != nil {
					return v, err
				}
				complete = complete && result.Complete
				for _, r := range result.Reasons {
					reasons[r] = true
				}
				if v.KnownBytes, err = addMetered(v.KnownBytes, result.KnownBytes); err != nil {
					return v, err
				}
			}
			// A fully observed graph with no eligible subjects is a known zero.
			v.HasData = v.HasData || complete
		}
		if cursor.Before(until) {
			complete = false
			reasons["topology_history_incomplete"] = true
		}
	}
	if len(a.RegionIDs) == 0 {
		reasons["topology_history_incomplete"] = true
	}
	if v.HasData {
		v.Source = "agent_estimate"
		v.Status = "partial"
	}
	if complete {
		v.Bytes = &v.KnownBytes
		v.Status = "complete"
		v.Source = "agent_estimate"
		v.HasData = true
		v.CompleteDay = !end.After(now)
	}
	if !v.CompleteDay && end.After(now) {
		reasons["day_in_progress"] = true
	}
	for r := range reasons {
		v.Reasons = append(v.Reasons, r)
	}
	sort.Strings(v.Reasons)
	return v, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func calculateDaily(ctx context.Context, tx *sql.Tx, a model.Account, date string, loc *time.Location, now time.Time) (model.DailyTrafficDay, error) {
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return model.DailyTrafficDay{}, err
	}
	end := day.AddDate(0, 0, 1)
	v := model.DailyTrafficDay{Date: date, Scopes: map[string]model.DailyScope{}}
	for _, scope := range []string{"mainland", "overseas"} {
		x, err := calculateDailyScope(ctx, tx, a, scope, day, end, now, loc)
		if err != nil {
			return v, err
		}
		v.Scopes[scope] = x
		v.Provisional = v.Provisional || x.Source == "agent_estimate" || end.After(now)
		if scope == "mainland" {
			v.Mainland = x.Bytes
		} else {
			v.Overseas = x.Bytes
		}
	}
	return v, nil
}

// Normalize a cached legacy payload or unknown date without claiming zero.
func missingDaily(date string, loc *time.Location, now time.Time) model.DailyTrafficDay {
	day, _ := time.ParseInLocation("2006-01-02", date, loc)
	v := model.DailyTrafficDay{Date: date, Scopes: map[string]model.DailyScope{}}
	for _, scope := range []string{"mainland", "overseas"} {
		v.Scopes[scope] = pendingDailyScope(day, earlierTime(day.AddDate(0, 0, 1), now))
	}
	return v
}

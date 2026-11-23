package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/JasperXzy/harbormaster/internal/metering"
	"github.com/JasperXzy/harbormaster/internal/model"
	"slices"
	"time"
)

func protectionSource(ctx context.Context, tx *sql.Tx, r model.ProtectionRule, a model.Account, settings model.EstimationSettings, loc *time.Location, now time.Time) (model.TrafficEstimation, *int64, int64, error) {
	if r.Scope == "swas" {
		return swasProtectionSource(ctx, tx, r, a, settings, loc, now)
	}
	quota := a.QuotaMainland
	if r.Scope == "overseas" {
		quota = a.QuotaOverseas
	}
	if !a.TracksScope(r.Scope) {
		return model.TrafficEstimation{Mode: "unverified", Status: "unavailable", To: now, CalculatedAt: now, LagSeconds: settings.LagSeconds, Reasons: []string{"scope_disabled"}}, nil, quota, nil
	}
	v, err := calculateEstimate(ctx, tx, a, r.Scope, settings, loc, now)
	if err != nil {
		return v, nil, quota, err
	}
	var official *int64
	if v.BasisID != "" {
		column := "mainland_bytes"
		if r.Scope == "overseas" {
			column = "overseas_bytes"
		}
		var n sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT "+column+" FROM cdt_observation WHERE id=? AND account_id=? AND credential_version=?", v.BasisID, a.ID, a.CredentialVersion).Scan(&n)
		if err == nil && n.Valid {
			official = &n.Int64
		}
	}
	return v, official, quota, err
}

// SWAS has official byte units but still no trustworthy cutoff/reset timestamp.
// It supports the same explicitly unverified display/rehearsal window, never
// real execution from a guessed official watermark.
func swasProtectionSource(ctx context.Context, tx *sql.Tx, r model.ProtectionRule, a model.Account, settings model.EstimationSettings, loc *time.Location, now time.Time) (model.TrafficEstimation, *int64, int64, error) {
	v := model.TrafficEstimation{Mode: "unverified", Status: "unavailable", To: now, CalculatedAt: now, LagSeconds: settings.LagSeconds, Reasons: []string{"official_cutoff_unverified", "lag_window_assumption"}}
	var usageText, fetchedText sql.NullString
	var plan string
	var present sql.NullBool
	err := tx.QueryRowContext(ctx, `SELECT c.usage_json,c.fetched_at,c.entry_present,c.plan_id FROM swas_traffic_cache c JOIN resource_snapshot s ON s.id=c.instance_id AND s.kind='swas' AND s.present=1
 WHERE c.instance_id=? AND s.account_id=? AND c.credential_version=? AND c.plan_id=json_extract(s.payload_json,'$.spec')`, r.SourceInstanceID, a.ID, a.CredentialVersion).Scan(&usageText, &fetchedText, &present, &plan)
	if errors.Is(err, sql.ErrNoRows) {
		v.Reasons = append(v.Reasons, "official_basis_missing")
		return v, nil, 0, nil
	}
	if err != nil {
		return v, nil, 0, err
	}
	if !present.Valid || !present.Bool || !usageText.Valid || !fetchedText.Valid {
		v.Reasons = append(v.Reasons, "official_entry_missing")
		return v, nil, 0, nil
	}
	usageRaw, fetched := usageText.String, fetchedText.String
	var u model.SWASTrafficUsage
	if err = json.Unmarshal([]byte(usageRaw), &u); err != nil {
		return v, nil, 0, err
	}
	at, err := time.Parse(time.RFC3339Nano, fetched)
	if err != nil {
		return v, nil, 0, err
	}
	start, _ := ReferenceCycle(now, loc)
	if at.Before(start) || at.After(now) {
		v.Reasons = append(v.Reasons, "official_cycle_unverified")
		return v, nil, u.Total, nil
	}
	v.BasisID = plan + ":" + fetched
	from := at.Add(-time.Duration(settings.LagSeconds) * time.Second)
	if from.Before(start) {
		from = start
	}
	v.From = &from
	var id, status string
	var quality sql.NullString
	var sampled, received, revoked sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT a.id,c.sampled_at,c.received_at,a.revoked_at,c.quality,i.status FROM agent a JOIN instance i ON i.id=a.instance_id LEFT JOIN agent_counter_state c ON c.agent_id=a.id WHERE i.id=?`, r.SourceInstanceID).Scan(&id, &sampled, &received, &revoked, &quality, &status)
	if errors.Is(err, sql.ErrNoRows) {
		v.Reasons = append(v.Reasons, "agent_data_unavailable")
		return v, &u.Used, u.Total, nil
	}
	if err != nil {
		return v, nil, 0, err
	}
	sample, receipt := parseAgentTime(sampled), parseAgentTime(received)
	if revoked.Valid || sample == nil || receipt == nil || sample.After(now) || receipt.After(now) || !quality.Valid || quality.String == "clock_skew" {
		v.Reasons = append(v.Reasons, "agent_data_unavailable")
		return v, &u.Used, u.Total, nil
	}
	offline := now.Sub(*sample) >= 30*time.Second || now.Sub(*receipt) >= 30*time.Second
	if !offline {
		v.To = *sample
	}
	if !v.To.After(from) {
		v.Reasons = append(v.Reasons, "samples_not_past_basis")
		return v, &u.Used, u.Total, nil
	}
	acc, err := metering.NewAccumulator(metering.Outbound, from, v.To)
	if err != nil {
		return v, nil, 0, err
	}
	if err = acc.AddMember(id, time.Time{}, time.Time{}); err != nil {
		return v, nil, 0, err
	}
	recent := []metering.Interval{}
	var last *metering.Interval
	err = walkMeteringIntervals(ctx, tx, id, earlierTime(from, sample.Add(-10*time.Minute)), *sample, func(x metering.Interval) error {
		copy := x
		last = &copy
		if x.End.After(sample.Add(-10 * time.Minute)) {
			recent = append(recent, x)
		}
		return acc.Add(id, x)
	})
	if err != nil {
		return v, nil, 0, err
	}
	if offline && status == "running" {
		var bandwidth sql.NullInt64
		if err = tx.QueryRowContext(ctx, "SELECT json_extract(payload_json,'$.bandwidth_mbps') FROM resource_snapshot WHERE id=? AND kind='swas'", r.SourceInstanceID).Scan(&bandwidth); err != nil {
			return v, nil, 0, err
		}
		var cap *int64
		if bandwidth.Valid {
			cap = &bandwidth.Int64
		}
		rate, _, e := metering.OfflineRate(recent, *sample, 10*time.Minute, cap, slices.Contains(settings.BandwidthIDs, r.SourceInstanceID))
		if e != nil {
			v.Reasons = append(v.Reasons, "public_bandwidth_unknown")
			return v, &u.Used, u.Total, nil
		}
		v.CurrentRate = &rate.TX
		if sample.Before(v.To) {
			if err = acc.Add(id, metering.Interval{Start: *sample, End: v.To, Projection: &rate}); err != nil {
				return v, nil, 0, err
			}
		}
	} else if last != nil && !last.Gap {
		rate, e := metering.CeilProduct(last.TX, 1000, last.DurationMS)
		if e != nil {
			return v, nil, 0, e
		}
		v.CurrentRate = &rate
	}
	result, err := acc.Result()
	if err != nil {
		return v, nil, 0, err
	}
	v.KnownIncrement = result.KnownBytes
	v.Reasons = append(v.Reasons, result.Reasons...)
	v.Status = "incomplete"
	if result.Complete {
		v.Status = "ready"
		v.Increment = result.Bytes
		v.WorstCase = &result.WorstCase
		total, e := addMetered(u.Used, *result.Bytes)
		if e != nil {
			return v, nil, 0, e
		}
		v.Estimated = &total
	}
	return v, &u.Used, u.Total, nil
}

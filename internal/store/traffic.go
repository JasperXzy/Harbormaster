package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func saveObservation(ctx context.Context, tx *sql.Tx, j model.Job, a model.Account, o model.CDTObservation) error {
	var code any
	if o.Error != nil {
		code = o.Error.Code
	}
	quality := o.Quality
	if quality != "verified" && quality != "inferred" && quality != "unknown" {
		quality = "inferred"
	}
	if quality == "verified" && (o.CoveredUntil == nil || o.PeriodStart == nil || o.PeriodEnd == nil || o.Source == "" || !o.PeriodEnd.After(*o.PeriodStart) || o.CoveredUntil.Before(*o.PeriodStart) || o.CoveredUntil.After(*o.PeriodEnd) || o.CoveredUntil.After(o.FetchedAt)) {
		return errors.New("invalid_verified_cdt_basis")
	}
	source := o.Source
	if source == "" {
		source = "ListCdtInternetTraffic"
	}
	raw, err := json.Marshal(o.Details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,overseas_bytes,details_json,error_code,covered_until,period_start,period_end,source_quality,source)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,job_id,attempt) DO NOTHING`, a.ID, a.CredentialVersion, j.ID, j.Attempts, timestamp(o.FetchedAt), o.Mainland, o.Overseas, string(raw), code, nullableMeteringTime(o.CoveredUntil), nullableMeteringTime(o.PeriodStart), nullableMeteringTime(o.PeriodEnd), quality, source)
	return err
}
func (s *Store) SaveCDT(ctx context.Context, j model.Job, a model.Account, o model.CDTObservation, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM account_settings a,job j WHERE a.account_id=? AND a.enabled=1 AND a.revision=? AND a.lease_owner=? AND a.lease_until>?
 AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, a.ID, a.Revision, j.Owner, now.Unix(), j.ID, j.Owner, now.Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccountChanged
	}
	if err = saveObservation(ctx, tx, j, a, o); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ScheduleCDT(ctx context.Context, interval time.Duration, now time.Time) error {
	ctx = model.WithJobTrigger(ctx, "schedule")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT account_id FROM account_settings a WHERE enabled=1 AND next_cdt_at<=?
 AND NOT EXISTS(SELECT 1 FROM cdt_observation o WHERE o.account_id=a.account_id AND o.credential_version=a.credential_version AND o.fetched_at>?)
 ORDER BY account_id`, now.Unix(), timestamp(now.Add(-interval)))
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = enqueueJob(ctx, tx, "refresh", "cdt:"+id, []string{id}, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET next_cdt_at=? WHERE account_id=?", now.Add(interval).Unix(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const observationColumns = "id,account_id,fetched_at,mainland_bytes,overseas_bytes,error_code,covered_until,period_start,period_end,source_quality,source"

func readObservation(row rowScanner) (model.CDTObservation, error) {
	var o model.CDTObservation
	var id int64
	var fetched string
	var mainland, overseas sql.NullInt64
	var code, cutoff, start, end sql.NullString
	if err := row.Scan(&id, &o.AccountID, &fetched, &mainland, &overseas, &code, &cutoff, &start, &end, &o.Quality, &o.Source); err != nil {
		return o, err
	}
	o.ID = strconv.FormatInt(id, 10)
	t, err := time.Parse(time.RFC3339Nano, fetched)
	if err != nil {
		return o, err
	}
	o.FetchedAt = t
	if mainland.Valid {
		x := mainland.Int64
		o.Mainland = &x
	}
	if overseas.Valid {
		x := overseas.Int64
		o.Overseas = &x
	}
	o.CoveredUntil = parseAgentTime(cutoff)
	o.PeriodStart = parseAgentTime(start)
	o.PeriodEnd = parseAgentTime(end)
	if code.Valid {
		o.Error = model.Error(code.String)
		o.Quality = "unknown"
	}
	return o, nil
}
func (s *Store) Observations(ctx context.Context, account string, before int64, limit int) ([]model.CDTObservation, bool, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?", account, before, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []model.CDTObservation{}
	for rows.Next() {
		o, err := readObservation(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, o)
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, rows.Err()
}

func ReferenceCycle(now time.Time, loc *time.Location) (time.Time, time.Time) {
	t := now.In(loc)
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	return start.UTC(), start.AddDate(0, 1, 0).UTC()
}

// Quotas never derives a watermarked estimate or daily usage from fetch times.
// A local reference cycle prevents a previous month's record becoming this
// month's value; it does not assert that the provider confirmed that cycle.
func (s *Store) Quotas(ctx context.Context, accounts []model.Account, loc *time.Location, now time.Time) ([]model.QuotaStatus, error) {
	start, end := ReferenceCycle(now, loc)
	pricing, err := s.TrafficPricing(ctx)
	if err != nil {
		return nil, err
	}
	items := []model.QuotaStatus{}
	for _, a := range accounts {
		latest, err := readObservation(s.DB.QueryRowContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? ORDER BY id DESC LIMIT 1", a.ID, a.CredentialVersion))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		hasLatest := err == nil
		for _, scope := range []string{"mainland", "overseas"} {
			quota := a.QuotaMainland
			column := "mainland_bytes"
			if scope == "overseas" {
				quota = a.QuotaOverseas
				column = "overseas_bytes"
			}
			q := model.QuotaStatus{AccountID: a.ID, Scope: scope, CycleStart: start, CycleEnd: end, QuotaBytes: quota, Thresholds: a.Thresholds, OfficialStatus: "never_fetched", OfficialQuality: "unknown"}
			if hasLatest {
				q.UpdatedAt = &latest.FetchedAt
				q.OfficialStatus = "cutoff_unknown"
				if latest.Error != nil {
					q.OfficialStatus = "unavailable"
				}
			}
			good, err := readObservation(s.DB.QueryRowContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND fetched_at>=? AND fetched_at<? AND error_code IS NULL AND "+column+" IS NOT NULL ORDER BY id DESC LIMIT 1", a.ID, a.CredentialVersion, timestamp(start), timestamp(end)))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				q.OfficialBytes = good.Mainland
				if scope == "overseas" {
					q.OfficialBytes = good.Overseas
				}
				q.FetchedAt = &good.FetchedAt
				q.OfficialQuality = good.Quality
				if good.Quality == "verified" && good.PeriodStart != nil && good.PeriodEnd != nil && good.PeriodStart.Equal(start) && good.PeriodEnd.Equal(end) {
					q.OfficialAsOf = good.CoveredUntil
					q.OfficialStatus = "ok"
				}
				q.Estimation, err = s.cachedEstimate(ctx, a, scope, good.ID, now)
				if err != nil {
					return nil, err
				}
				if q.Estimation != nil && q.Estimation.Mode == "aligned" && q.Estimation.Status == "ready" && q.Estimation.Qualified {
					q.RealtimeIncrement = q.Estimation.Increment
					q.WorstCaseBytes = q.Estimation.WorstCase
					q.EstimatedBytes = q.Estimation.Estimated
					q.EffectiveBytes = q.Estimation.Estimated
				}
				if hasLatest && latest.ID != good.ID {
					q.OfficialStatus = "unavailable"
				}
			}
			q.Reconciliation, err = s.latestReconciliation(ctx, a, scope, start)
			if err != nil {
				return nil, err
			}
			if err = s.forecastQuota(ctx, a, &q, loc, now); err != nil {
				return nil, err
			}
			q.OverageCost = quotaOverageCostForAccount(a, q, pricing, now)
			items = append(items, q)
		}
	}
	return items, nil
}

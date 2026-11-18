package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func markDaily(ctx context.Context, tx *sql.Tx, account, zone, date string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO daily_work(account_id,timezone,day) VALUES(?,?,?) ON CONFLICT(account_id,timezone,day) DO UPDATE SET generation=generation+1,pending=1`, account, zone, date)
	return err
}

// Bound invalidation work per tick. No raw traffic is loaded on HTTP reads.
func consumeDailyEvents(ctx context.Context, tx *sql.Tx, loc *time.Location, now time.Time) error {
	type event struct {
		id       int64
		account  string
		from, to time.Time
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.account_id,e.cutoff,c.sampled_at,
 (SELECT p.sampled_at FROM traffic_sample p WHERE p.agent_id=c.agent_id AND p.counter_epoch=c.counter_epoch AND p.sequence<c.sequence ORDER BY p.sequence DESC LIMIT 1)
 FROM daily_event e LEFT JOIN traffic_sample c ON c.agent_id=e.agent_id AND c.counter_epoch=e.counter_epoch AND c.sequence=e.sequence ORDER BY e.id LIMIT 1000`)
	if err != nil {
		return err
	}
	events := []event{}
	for rows.Next() {
		var e event
		var cutoff, to, from sql.NullString
		if err = rows.Scan(&e.id, &e.account, &cutoff, &to, &from); err != nil {
			rows.Close()
			return err
		}
		if at := parseAgentTime(cutoff); at != nil {
			e.to = *at
			e.from = at.In(loc).AddDate(0, 0, -1)
		} else if at := parseAgentTime(to); at != nil {
			e.to = *at
			e.from = *at
			if p := parseAgentTime(from); p != nil && p.Before(*at) {
				e.from = *p
			}
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	start, _ := ReferenceCycle(now, loc)
	floor := start.In(loc).AddDate(0, 0, -7)
	dirty := map[[2]string]bool{}
	for _, e := range events {
		if !e.to.IsZero() && !e.to.After(now) {
			from := maxTime(e.from, floor).In(loc)
			to := e.to.In(loc)
			for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); !day.After(to); day = day.AddDate(0, 0, 1) {
				dirty[[2]string{e.account, day.Format("2006-01-02")}] = true
			}
		}
	}
	for key := range dirty {
		if err = markDaily(ctx, tx, key[0], loc.String(), key[1]); err != nil {
			return err
		}
	}
	if len(events) > 0 {
		_, err = tx.ExecContext(ctx, "DELETE FROM daily_event WHERE id<=?", events[len(events)-1].id)
	}
	if err != nil {
		return err
	}
	return nil
}

// RefreshDaily processes a bounded number of dates per account. Completed
// cached days survive raw retention and restarts; late receipts enqueue repairs.
func (s *Store) RefreshDaily(ctx context.Context, loc *time.Location, now time.Time) error {
	accounts, err := s.Accounts(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	err = func() error {
		defer tx.Rollback()
		if e := consumeDailyEvents(ctx, tx, loc, now); e != nil {
			return e
		}
		start, _ := ReferenceCycle(now, loc)
		today := now.In(loc)
		midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
		first := start.In(loc)
		if x := midnight.AddDate(0, 0, -7); x.Before(first) {
			first = x
		}
		for _, a := range accounts {
			if !a.Enabled {
				continue
			}
			if e := seedDailyTopology(ctx, tx, a); e != nil {
				return e
			}
			for day := first; !day.After(midnight); day = day.AddDate(0, 0, 1) {
				date := day.Format("2006-01-02")
				if _, e := tx.ExecContext(ctx, `INSERT INTO daily_work(account_id,timezone,day) VALUES(?,?,?) ON CONFLICT(account_id,timezone,day) DO UPDATE SET pending=CASE WHEN NOT EXISTS(SELECT 1 FROM daily_traffic d WHERE d.account_id=excluded.account_id AND d.timezone=excluded.timezone AND d.day=excluded.day AND d.credential_version=? AND d.account_revision=?) THEN 1 ELSE pending END`, a.ID, loc.String(), date, a.CredentialVersion, a.Revision); e != nil {
					return e
				}
			}
			if e := markDaily(ctx, tx, a.ID, loc.String(), midnight.Format("2006-01-02")); e != nil {
				return e
			}
		}
		return tx.Commit()
	}()
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		rows, e := s.DB.QueryContext(ctx, "SELECT day FROM daily_work WHERE account_id=? AND timezone=? AND pending=1 ORDER BY day DESC LIMIT 6", a.ID, loc.String())
		if e != nil {
			return e
		}
		dates := []string{}
		for rows.Next() {
			var date string
			if e = rows.Scan(&date); e != nil {
				rows.Close()
				return e
			}
			dates = append(dates, date)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, date := range dates {
			if e = s.refreshDailyDate(ctx, a.ID, date, loc, now); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Store) refreshDailyDate(ctx context.Context, id, date string, loc *time.Location, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := loadAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return nil
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM daily_work WHERE account_id=? AND timezone=? AND day=?", id, loc.String(), date).Scan(&generation); err != nil {
		return err
	}
	v, err := calculateDaily(ctx, tx, a, date, loc, now)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO daily_traffic VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(account_id,timezone,day) DO UPDATE SET credential_version=excluded.credential_version,account_revision=excluded.account_revision,generation=excluded.generation,calculated_at=excluded.calculated_at,payload_json=excluded.payload_json`, id, loc.String(), date, a.CredentialVersion, a.Revision, generation, timestamp(now), string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE daily_work SET pending=0 WHERE account_id=? AND timezone=? AND day=? AND generation=?", id, loc.String(), date, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) dailyRows(ctx context.Context, a model.Account, loc *time.Location, from, to time.Time, forecast bool) (map[string]model.DailyTrafficDay, *time.Time, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.day,d.calculated_at,d.payload_json,w.pending FROM daily_traffic d JOIN daily_work w ON w.account_id=d.account_id AND w.timezone=d.timezone AND w.day=d.day WHERE d.account_id=? AND d.credential_version=? AND d.account_revision=? AND d.timezone=? AND d.day>=? AND d.day<? ORDER BY d.day`, a.ID, a.CredentialVersion, a.Revision, loc.String(), from.In(loc).Format("2006-01-02"), to.In(loc).Format("2006-01-02"))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := map[string]model.DailyTrafficDay{}
	var updated *time.Time
	for rows.Next() {
		var date, at, raw string
		var pending bool
		if err = rows.Scan(&date, &at, &raw, &pending); err != nil {
			return nil, nil, err
		}
		if forecast && pending {
			continue
		}
		var v model.DailyTrafficDay
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, nil, err
		}
		out[date] = v
		t, _ := time.Parse(time.RFC3339Nano, at)
		if updated == nil || t.After(*updated) {
			updated = &t
		}
	}
	return out, updated, rows.Err()
}

func (s *Store) DailyTraffic(ctx context.Context, a model.Account, loc *time.Location, now time.Time) (model.DailyTraffic, error) {
	start, end := ReferenceCycle(now, loc)
	today := now.In(loc)
	midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	v := model.DailyTraffic{AccountID: a.ID, CycleStart: start, CycleEnd: end, Timezone: loc.String(), Days: []model.DailyTrafficDay{}, SourceStatus: "unavailable", OfficialPoints: map[string][]model.UsagePoint{"mainland": {}, "overseas": {}}}
	rows, updated, err := s.dailyRows(ctx, a, loc, start, midnight.AddDate(0, 0, 1), false)
	if err != nil {
		return v, err
	}
	v.UpdatedAt = updated
	anyData, allComplete := false, true
	for day := start.In(loc); !day.After(midnight); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		d, ok := rows[date]
		if !ok {
			d = missingDaily(date, loc, now)
		}
		for _, x := range d.Scopes {
			anyData = anyData || x.HasData
			allComplete = allComplete && x.Status == "complete"
		}
		v.Days = append(v.Days, d)
	}
	if allComplete {
		v.SourceStatus = "available"
	} else if anyData {
		v.SourceStatus = "partial"
	}
	// One capture-time point per date, not a difference of cumulative readings.
	r, err := s.DB.QueryContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND fetched_at>=? AND fetched_at<? AND error_code IS NULL ORDER BY fetched_at,id", a.ID, a.CredentialVersion, timestamp(start), timestamp(end))
	if err != nil {
		return v, err
	}
	defer r.Close()
	for r.Next() {
		o, e := readObservation(r)
		if e != nil {
			return v, e
		}
		date := o.FetchedAt.In(loc).Format("2006-01-02")
		for scope, value := range map[string]*int64{"mainland": o.Mainland, "overseas": o.Overseas} {
			if value == nil {
				continue
			}
			p := model.UsagePoint{At: o.FetchedAt, Date: date, Bytes: *value}
			points := v.OfficialPoints[scope]
			if len(points) > 0 && points[len(points)-1].Date == date {
				points[len(points)-1] = p
			} else {
				points = append(points, p)
			}
			v.OfficialPoints[scope] = points
		}
	}
	return v, r.Err()
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (s *Store) SaveSystemEvents(ctx context.Context, j model.Job, a model.Account, observations []model.SystemEventObservation, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM account_settings a,job j WHERE a.account_id=? AND a.enabled=1 AND a.revision=? AND a.lease_owner=? AND a.lease_until>? AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, a.ID, a.Revision, j.Owner, now.Unix(), j.ID, j.Owner, now.Unix()).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return ErrAccountChanged
	}
	if err = saveSystemEvents(ctx, tx, a, observations, now); err != nil {
		return err
	}
	return tx.Commit()
}

func saveSystemEvents(ctx context.Context, tx *sql.Tx, a model.Account, observations []model.SystemEventObservation, now time.Time) error {
	seen := map[string]bool{}
	for _, o := range observations {
		scope := o.RegionID + ":" + o.Source
		if o.AccountID != a.ID || !slices.Contains(a.RegionIDs, o.RegionID) || (o.Source != "ecs_history" && o.Source != "cms_spot") || o.FetchedAt.IsZero() || o.FetchedAt.After(now) || seen[scope] {
			return errors.New("invalid_system_event_observation")
		}
		seen[scope] = true
		var previous string
		err := tx.QueryRowContext(ctx, "SELECT checked_at FROM system_event_source WHERE account_id=? AND region_id=? AND source=?", a.ID, o.RegionID, o.Source).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			at, e := time.Parse(time.RFC3339Nano, previous)
			if e != nil {
				return e
			}
			if o.FetchedAt.Before(at) {
				continue
			}
		}
		var code, lastSuccess any
		if o.Error != nil {
			code = o.Error.Code
		} else {
			lastSuccess = timestamp(o.FetchedAt)
			if o.Events == nil {
				return errors.New("incomplete_system_event_observation")
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO system_event_source(account_id,region_id,source,revision,checked_at,last_success_at,error_code) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(account_id,region_id,source) DO UPDATE SET revision=excluded.revision,checked_at=excluded.checked_at,last_success_at=coalesce(excluded.last_success_at,system_event_source.last_success_at),error_code=excluded.error_code`, a.ID, o.RegionID, o.Source, a.Revision, timestamp(o.FetchedAt), lastSuccess, code)
		if err != nil {
			return err
		}
		if o.Error != nil {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE system_event SET seen_in_latest_scan=0 WHERE account_id=? AND region_id=? AND source=?", a.ID, o.RegionID, o.Source); err != nil {
			return err
		}
		ids := map[string]bool{}
		for _, event := range o.Events {
			if event.CloudEventID == "" || event.CloudInstanceID == "" || event.EventType == "" || event.ProviderStatus == "" || ids[event.CloudEventID] || (event.Category != "maintenance" && event.Category != "spot_interruption" && event.Category != "other") {
				return errors.New("invalid_system_event")
			}
			ids[event.CloudEventID] = true
			raw, err := json.Marshal(event)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO system_event(id,account_id,region_id,source,cloud_event_id,cloud_instance_id,category,payload_json,first_seen_at,observed_at,seen_in_latest_scan) VALUES(?,?,?,?,?,?,?,?,?,?,1)
 ON CONFLICT(account_id,region_id,source,cloud_event_id) DO UPDATE SET cloud_instance_id=excluded.cloud_instance_id,category=excluded.category,payload_json=excluded.payload_json,observed_at=excluded.observed_at,seen_in_latest_scan=1 WHERE system_event.cloud_instance_id=excluded.cloud_instance_id`, model.NewID("ce_"), a.ID, o.RegionID, o.Source, event.CloudEventID, event.CloudInstanceID, event.Category, string(raw), timestamp(o.FetchedAt), timestamp(o.FetchedAt))
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return errors.New("system_event_identity_changed")
			}
		}
	}
	return nil
}

func (s *Store) ScheduleSystemEvents(ctx context.Context, interval time.Duration, now time.Time) error {
	ctx = model.WithJobTrigger(ctx, "schedule")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT account_id FROM account_settings WHERE enabled=1 AND next_system_event_at<=? ORDER BY account_id", now.Unix())
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
		if _, err = enqueueJob(ctx, tx, "refresh", "system-events:"+id, []string{id}, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET next_system_event_at=? WHERE account_id=?", now.Add(interval).Unix(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func systemEventWhere(f model.SystemEventFilter, alias string, source bool) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	for _, field := range []struct{ column, value string }{{"account_id", f.AccountID}, {"region_id", f.RegionID}, {"source", f.Source}} {
		if field.value != "" {
			clauses = append(clauses, alias+"."+field.column+"=?")
			args = append(args, field.value)
		}
	}
	if f.InstanceID != "" {
		clause := "EXISTS (SELECT 1 FROM instance i WHERE i.id=? AND i.account_id=" + alias + ".account_id AND i.region=" + alias + ".region_id AND i.kind='ecs'"
		if !source {
			clause += " AND i.cloud_id=" + alias + ".cloud_instance_id"
		}
		clauses = append(clauses, clause+")")
		args = append(args, f.InstanceID)
	}

	if !source && f.Category != "" {
		clauses = append(clauses, alias+".category=?")
		args = append(args, f.Category)
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) SystemEvents(ctx context.Context, f model.SystemEventFilter, before int64, limit int) ([]model.SystemEvent, error) {
	if !f.Validate() || before < 0 || limit < 1 || limit > 201 {
		return nil, ErrNotFound
	}
	where, args := systemEventWhere(f, "e", false)
	if before > 0 {
		where += " AND e.sequence<?"
		args = append(args, before)
	}
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, `SELECT e.sequence,e.id,e.account_id,e.region_id,e.source,e.payload_json,e.first_seen_at,e.observed_at,e.seen_in_latest_scan,
 (SELECT i.id FROM instance i WHERE i.account_id=e.account_id AND i.kind='ecs' AND i.region=e.region_id AND i.cloud_id=e.cloud_instance_id)
 FROM system_event e WHERE `+where+` ORDER BY e.sequence DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.SystemEvent{}
	for rows.Next() {
		var event model.SystemEvent
		var raw, first, observed string
		if err = rows.Scan(&event.Sequence, &event.ID, &event.AccountID, &event.RegionID, &event.Source, &raw, &first, &observed, &event.SeenInLatestScan, &event.InstanceID); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &event.CloudSystemEvent); err != nil {
			return nil, err
		}
		if event.FirstSeenAt, err = time.Parse(time.RFC3339Nano, first); err != nil {
			return nil, err
		}
		if event.ObservedAt, err = time.Parse(time.RFC3339Nano, observed); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) SystemEventSources(ctx context.Context, f model.SystemEventFilter, now time.Time, maxAge time.Duration) ([]model.SystemEventSource, error) {
	if !f.Validate() {
		return nil, ErrNotFound
	}
	where, args := systemEventWhere(f, "e", true)
	rows, err := s.DB.QueryContext(ctx, `SELECT e.account_id,e.region_id,e.source,e.revision,e.checked_at,e.last_success_at,e.error_code,a.enabled,a.revision,a.config_json
 FROM system_event_source e JOIN account_settings a ON a.account_id=e.account_id WHERE `+where+` ORDER BY e.account_id,e.region_id,e.source`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.SystemEventSource{}
	for rows.Next() {
		var source model.SystemEventSource
		var revision, current int64
		var checked, config string
		var success, code sql.NullString
		var enabled bool
		if err = rows.Scan(&source.AccountID, &source.RegionID, &source.Source, &revision, &checked, &success, &code, &enabled, &current, &config); err != nil {
			return nil, err
		}
		if source.CheckedAt, err = time.Parse(time.RFC3339Nano, checked); err != nil {
			return nil, err
		}
		if success.Valid {
			v, e := time.Parse(time.RFC3339Nano, success.String)
			if e != nil {
				return nil, e
			}
			source.LastSuccessAt = &v
		}
		if code.Valid {
			source.LastError = model.Error(code.String)
		}
		var account model.Account
		if err = json.Unmarshal([]byte(config), &account); err != nil {
			return nil, err
		}
		source.Status = "ok"
		switch {
		case !enabled || !slices.Contains(account.RegionIDs, source.RegionID):
			source.Status = "paused"
		case revision != current || source.CheckedAt.After(now) || source.CheckedAt.Add(maxAge).Before(now):
			source.Status = "stale"
		case source.LastError != nil:
			source.Status = "error"
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

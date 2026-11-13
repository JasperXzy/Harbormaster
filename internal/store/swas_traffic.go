package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrSWASTraffic = errors.New("invalid_swas_traffic_observation")

// Called after instance snapshots are saved, inside SaveScan's fenced transaction.
func saveSWASTraffic(ctx context.Context, tx *sql.Tx, account model.Account, o model.SWASTrafficObservation) error {
	if o.InstanceID == "" || o.QueriedAt.IsZero() || account.CredentialVersion < 1 {
		return ErrSWASTraffic
	}
	var fetched, present, raw, code any
	if o.Error != nil {
		if o.Error.Code == "" || o.EntryPresent != nil || o.Usage != nil {
			return ErrSWASTraffic
		}
		code = o.Error.Code
	} else {
		if o.EntryPresent == nil || *o.EntryPresent && (o.Usage == nil || !o.Usage.Valid()) || !*o.EntryPresent && o.Usage != nil {
			return ErrSWASTraffic
		}
		fetched, present = timestamp(o.QueriedAt), *o.EntryPresent
		if o.Usage != nil {
			encoded, err := json.Marshal(o.Usage)
			if err != nil {
				return err
			}
			raw = string(encoded)
		}
	}
	var matches int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM instance i JOIN resource_snapshot r
 ON r.account_id=i.account_id AND r.id=i.id AND r.kind='swas'
 WHERE i.id=? AND i.account_id=? AND i.kind='swas' AND coalesce(json_extract(r.payload_json,'$.spec'),'')=?`, o.InstanceID, account.ID, o.PlanID).Scan(&matches)
	if err != nil {
		return err
	}
	if matches != 1 {
		return ErrSWASTraffic
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO swas_traffic_cache(instance_id,credential_version,plan_id,fetched_at,last_attempt_at,error_code,entry_present,usage_json)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(instance_id,credential_version,plan_id) DO UPDATE SET
 last_attempt_at=excluded.last_attempt_at,error_code=excluded.error_code,
 fetched_at=CASE WHEN excluded.error_code IS NULL THEN excluded.fetched_at WHEN excluded.plan_id='' THEN NULL ELSE swas_traffic_cache.fetched_at END,
 entry_present=CASE WHEN excluded.error_code IS NULL THEN excluded.entry_present WHEN excluded.plan_id='' THEN NULL ELSE swas_traffic_cache.entry_present END,
 usage_json=CASE WHEN excluded.error_code IS NULL THEN excluded.usage_json WHEN excluded.plan_id='' THEN NULL ELSE swas_traffic_cache.usage_json END
 WHERE excluded.last_attempt_at>=swas_traffic_cache.last_attempt_at`, o.InstanceID, account.CredentialVersion, o.PlanID, fetched, timestamp(o.QueriedAt), code, present, raw)
	return err
}

// One query per displayed instance page; targets bind values to the plan IDs in
// that page rather than a newer concurrently refreshed inventory snapshot.
func (s *Store) SWASTrafficPackages(ctx context.Context, targets []model.SWASTrafficTarget, now time.Time) (map[string]model.SWASTrafficPackage, error) {
	out := map[string]model.SWASTrafficPackage{}
	if len(targets) == 0 {
		return out, nil
	}
	if len(targets) > 200 {
		return nil, ErrSWASTraffic
	}
	marks := make([]string, len(targets))
	args := make([]any, 0, len(targets)*2)
	for i, target := range targets {
		marks[i] = "(?,?)"
		args = append(args, target.InstanceID, target.PlanID)
	}
	rows, err := s.DB.QueryContext(ctx, `WITH requested(instance_id,plan_id) AS (VALUES `+strings.Join(marks, ",")+`)
 SELECT i.id,a.enabled AND EXISTS(SELECT 1 FROM json_each(a.config_json,'$.region_ids') WHERE value=i.region),
 c.fetched_at,c.last_attempt_at,c.error_code,c.entry_present,c.usage_json
 FROM requested r JOIN instance i ON i.id=r.instance_id AND i.kind='swas'
 JOIN account_settings a ON a.account_id=i.account_id
 LEFT JOIN swas_traffic_cache c ON c.instance_id=i.id AND c.credential_version=a.credential_version AND c.plan_id=r.plan_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var enabled bool
		var fetched, attempt, code, raw sql.NullString
		var present sql.NullBool
		if err = rows.Scan(&id, &enabled, &fetched, &attempt, &code, &present, &raw); err != nil {
			return nil, err
		}
		v := model.EmptySWASTrafficPackage()
		v.FetchedAt, v.LastAttemptAt = parseAgentTime(fetched), parseAgentTime(attempt)
		v.UpdatedAt = v.LastAttemptAt
		if attempt.Valid {
			v.Status = "ok"
		}
		if present.Valid {
			v.EntryPresent = &present.Bool
		}
		if raw.Valid {
			v.Usage = new(model.SWASTrafficUsage)
			if json.Unmarshal([]byte(raw.String), v.Usage) != nil || !v.Usage.Valid() {
				return nil, ErrSWASTraffic
			}
		}
		if code.Valid {
			v.Status, v.LastError = "unavailable", model.Error(code.String)
		}
		v.Age(now)
		if !enabled {
			v.Status = "disabled"
		}
		out[id] = v
	}
	return out, rows.Err()
}

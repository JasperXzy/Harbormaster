package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrIdleObservation = errors.New("invalid_idle_resource_observation")
var ErrIdleFilter = errors.New("invalid_idle_resource_filter")

type idleScope struct{ account, region, kind string }

func saveIdle(ctx context.Context, tx *sql.Tx, a model.Account, o model.IdleObservation, now time.Time) error {
	if !slices.Contains(a.RegionIDs, o.Region) || model.IdleSourceName(o.Kind) == "" || o.QueriedAt.IsZero() || o.QueriedAt.After(now) || (o.Error == nil) != (o.Data != nil) || (o.Error != nil && o.Error.Code == "") {
		return ErrIdleObservation
	}
	if o.Data != nil && !o.Data.Valid() {
		return ErrIdleObservation
	}
	if o.Data != nil {
		for _, v := range o.Data.Candidates {
			if v.AccountID != a.ID || v.RegionID != o.Region || v.Kind != o.Kind || v.ID != a.ID+"_"+o.Kind+"_"+o.Region+"_"+v.CloudID || !v.ObservedAt.Equal(o.QueriedAt) {
				return ErrIdleObservation
			}
		}
	}
	var fetched, code, scanned, unknown, candidates any
	if o.Error != nil {
		code = o.Error.Code
	} else {
		fetched = timestamp(o.QueriedAt)
		scanned = o.Data.Scanned
		unknown = len(o.Data.UnclassifiedIDs)
		candidates = 0
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO idle_resource_source(account_id,credential_version,region,kind,fetched_at,last_attempt_at,error_code,scanned_count,unclassified_count,candidate_count)
 VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,credential_version,region,kind) DO UPDATE SET
 fetched_at=coalesce(excluded.fetched_at,idle_resource_source.fetched_at),last_attempt_at=excluded.last_attempt_at,error_code=excluded.error_code,
 scanned_count=coalesce(excluded.scanned_count,idle_resource_source.scanned_count),
 unclassified_count=coalesce(excluded.unclassified_count,idle_resource_source.unclassified_count),
 candidate_count=coalesce(excluded.candidate_count,idle_resource_source.candidate_count)
 WHERE idle_resource_source.last_attempt_at<=excluded.last_attempt_at`, a.ID, a.CredentialVersion, o.Region, o.Kind, fetched, timestamp(o.QueriedAt), code, scanned, unknown, candidates)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 || o.Error != nil {
		return err
	}
	ids, _ := json.Marshal(o.Data.UnclassifiedIDs)
	if _, err := tx.ExecContext(ctx, `DELETE FROM idle_resource_candidate WHERE account_id=? AND credential_version=? AND region=? AND kind=?
 AND cloud_id NOT IN (SELECT value FROM json_each(?))`, a.ID, a.CredentialVersion, o.Region, o.Kind, string(ids)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE idle_resource_candidate SET retained=1 WHERE account_id=? AND credential_version=? AND region=? AND kind=?`, a.ID, a.CredentialVersion, o.Region, o.Kind); err != nil {
		return err
	}
	for _, v := range o.Data.Candidates {
		v.SourceStatus, v.EvidenceStatus = "", ""
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO idle_resource_candidate(account_id,credential_version,region,kind,id,cloud_id,payload_json,retained)
 VALUES(?,?,?,?,?,?,?,0) ON CONFLICT(account_id,credential_version,region,kind,cloud_id) DO UPDATE SET id=excluded.id,payload_json=excluded.payload_json,retained=0`, a.ID, a.CredentialVersion, o.Region, o.Kind, v.ID, v.CloudID, string(raw)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE idle_resource_source SET candidate_count=(SELECT count(*) FROM idle_resource_candidate c
 WHERE c.account_id=idle_resource_source.account_id AND c.credential_version=idle_resource_source.credential_version AND c.region=idle_resource_source.region AND c.kind=idle_resource_source.kind)
 WHERE account_id=? AND credential_version=? AND region=? AND kind=?`, a.ID, a.CredentialVersion, o.Region, o.Kind)
	if err != nil {
		return err
	}
	return checkIdleObservation(ctx, tx, a, o, now)
}

func sourceMatches(f model.IdleFilter, region, kind string) bool {
	return (f.RegionID == "" || f.RegionID == region) && (f.Kind == "" || f.Kind == kind)
}

// IdleResources reads candidates and coverage under one SQLite snapshot. It
// never performs classification, pruning, cloud queries or resource actions.
func (s *Store) IdleResources(ctx context.Context, f model.IdleFilter, now time.Time) ([]model.IdleCandidate, []model.IdleSource, error) {
	if !f.Valid() {
		return nil, nil, ErrIdleFilter
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	accounts := map[string]model.Account{}
	rows, err := tx.QueryContext(ctx, `SELECT account_id,config_json,enabled,credential_version FROM account_settings WHERE (?='' OR account_id=?)`, f.AccountID, f.AccountID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, raw string
		var enabled bool
		var version int64
		if err := rows.Scan(&id, &raw, &enabled, &version); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var a model.Account
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			rows.Close()
			return nil, nil, err
		}
		a.ID, a.Enabled, a.CredentialVersion = id, enabled, version
		accounts[id] = a
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	sources := map[idleScope]model.IdleSource{}
	for _, a := range accounts {
		for _, region := range a.RegionIDs {
			for _, kind := range model.IdleResourceKinds {
				if !sourceMatches(f, region, kind) {
					continue
				}
				v := model.IdleSource{AccountID: a.ID, RegionID: region, Kind: kind, Source: model.IdleSourceName(kind), Enabled: a.Enabled}
				v.Age(now)
				sources[idleScope{a.ID, region, kind}] = v
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT x.account_id,x.region,x.kind,x.fetched_at,x.last_attempt_at,x.error_code,x.scanned_count,x.unclassified_count,x.candidate_count
 FROM idle_resource_source x JOIN account_settings a ON a.account_id=x.account_id AND a.credential_version=x.credential_version
 WHERE (?='' OR x.account_id=?) AND (?='' OR x.region=?) AND (?='' OR x.kind=?)`, f.AccountID, f.AccountID, f.RegionID, f.RegionID, f.Kind, f.Kind)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var v model.IdleSource
		var fetched, attempted, code sql.NullString
		var scanned, unknown, count sql.NullInt64
		if err := rows.Scan(&v.AccountID, &v.RegionID, &v.Kind, &fetched, &attempted, &code, &scanned, &unknown, &count); err != nil {
			rows.Close()
			return nil, nil, err
		}
		a := accounts[v.AccountID]
		v.Source = model.IdleSourceName(v.Kind)
		v.Enabled = a.Enabled && slices.Contains(a.RegionIDs, v.RegionID)
		v.FetchedAt, v.LastAttemptAt = parseAgentTime(fetched), parseAgentTime(attempted)
		if code.Valid {
			v.LastError = model.Error(code.String)
		}
		if scanned.Valid {
			n := int(scanned.Int64)
			v.Scanned = &n
		}
		if unknown.Valid {
			n := int(unknown.Int64)
			v.Unclassified = &n
		}
		if count.Valid {
			n := int(count.Int64)
			v.Candidates = &n
		}
		v.Age(now)
		sources[idleScope{v.AccountID, v.RegionID, v.Kind}] = v
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	items := []model.IdleCandidate{}
	rows, err = tx.QueryContext(ctx, `SELECT c.payload_json,c.retained FROM idle_resource_candidate c
 JOIN account_settings a ON a.account_id=c.account_id AND a.credential_version=c.credential_version
 WHERE (?='' OR c.account_id=?) AND (?='' OR c.region=?) AND (?='' OR c.kind=?) AND (?='' OR c.id>?) ORDER BY c.id LIMIT ?`, f.AccountID, f.AccountID, f.RegionID, f.RegionID, f.Kind, f.Kind, f.After, f.After, f.Limit)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var raw string
		var retained bool
		if err := rows.Scan(&raw, &retained); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var v model.IdleCandidate
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			rows.Close()
			return nil, nil, err
		}
		source, ok := sources[idleScope{v.AccountID, v.RegionID, v.Kind}]
		if !ok || !v.Valid() {
			rows.Close()
			return nil, nil, ErrIdleObservation
		}
		v.SourceStatus, v.EvidenceStatus = source.Status, "current"
		if retained || (source.Status != "ok" && source.Status != "partial") {
			v.EvidenceStatus = "retained"
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	coverage := make([]model.IdleSource, 0, len(sources))
	for _, v := range sources {
		coverage = append(coverage, v)
	}
	sort.Slice(coverage, func(i, j int) bool {
		a, b := coverage[i], coverage[j]
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		if a.RegionID != b.RegionID {
			return a.RegionID < b.RegionID
		}
		return a.Kind < b.Kind
	})
	return items, coverage, tx.Commit()
}

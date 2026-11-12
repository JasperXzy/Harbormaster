package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// SaveScan checks both job and account leases in the same transaction as the
// snapshot update. Disabled/edited accounts and expired workers cannot publish.
func (s *Store) SaveScan(ctx context.Context, j model.Job, a model.Account, result model.ScanResult, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM account_settings a, job j
 WHERE a.account_id=? AND a.enabled=1 AND a.revision=? AND a.lease_owner=? AND a.lease_until>?
 AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, a.ID, a.Revision, j.Owner, now.Unix(), j.ID, j.Owner, now.Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccountChanged
	}
	for _, observation := range result.Finance {
		if err = saveFinance(ctx, tx, a, observation); err != nil {
			return err
		}
	}
	if result.CDT != nil {
		if err = saveObservation(ctx, tx, j, a, *result.CDT); err != nil {
			return err
		}
	}
	if err = recordInstanceEvents(ctx, tx, a.ID, result, now); err != nil {
		return err
	}
	for scope, complete := range result.CompleteScopes {
		if !complete {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE resource_snapshot SET present=0 WHERE account_id=? AND kind||':'||region=?", a.ID, scope); err != nil {
			return err
		}
	}
	for _, v := range result.Snapshots {
		if err = retainCPU(ctx, tx, a.ID, v); err != nil {
			return err
		}
		if err = retainIPv6(ctx, tx, a.ID, v); err != nil {
			return err
		}
		raw, err := json.Marshal(v.Payload)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO resource_snapshot(account_id,kind,id,region,payload_json,present,updated_at) VALUES(?,?,?,?,?,1,?)
 ON CONFLICT(account_id,kind,id) DO UPDATE SET region=excluded.region,payload_json=excluded.payload_json,present=1,updated_at=excluded.updated_at`, a.ID, v.Kind, v.ID, v.Region, string(raw), timestamp(now)); err != nil {
			return err
		}
		if v.Kind == "ecs" || v.Kind == "swas" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO instance(id,account_id,cloud_id,kind,name,region,status,desired_state,updated_at) VALUES(?,?,?,?,?,?,?,'stopped',?)
 ON CONFLICT(id) DO UPDATE SET name=excluded.name,status=excluded.status,updated_at=excluded.updated_at`, v.ID, a.ID, v.Payload["cloud_instance_id"], v.Kind, v.Payload["name"], v.Region, v.Payload["status"], timestamp(now)); err != nil {
				return err
			}
		}
	}
	if err = checkExpirationSnapshots(ctx, tx, a.ID, result.Snapshots, now); err != nil {
		return err
	}
	if err = checkBandwidthSnapshots(ctx, tx, a.ID, result.Snapshots, now); err != nil {
		return err
	}
	a.UpdatedAt = now
	if result.Permissions != nil {
		a.PermissionCheck = result.Permissions
	}
	if result.Failure != "" {
		a.SyncStatus = "failing"
		a.ConsecutiveFailures++
		a.LastError = model.Error(result.Failure)
	} else {
		a.SyncStatus = "ok"
		a.ConsecutiveFailures = 0
		a.LastError = nil
		a.LastSyncAt = &now
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET config_json=?,lease_owner=NULL,lease_until=0 WHERE account_id=?", string(raw), a.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE account SET updated_at=? WHERE id=?", timestamp(now), a.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// Snapshots reads stored data only. The caller applies cursor pagination and
// schema-specific filters; all queries remain bounded by the configured accounts.
func (s *Store) Snapshots(ctx context.Context, kinds []string, accountID string) ([]map[string]any, error) {
	// A small fixed set prevents arbitrary SQL fragments from request parameters.
	allowed := map[string]bool{"ecs": true, "swas": true, "subject": true, "package": true, "regions": true}
	items := []map[string]any{}
	for _, kind := range kinds {
		if !allowed[kind] {
			return nil, ErrNotFound
		}
		rows, err := s.DB.QueryContext(ctx, "SELECT payload_json FROM resource_snapshot WHERE present=1 AND kind=? AND (?='' OR account_id=?) ORDER BY id", kind, accountID, accountID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, err
			}
			var item map[string]any
			if err = json.Unmarshal([]byte(raw), &item); err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

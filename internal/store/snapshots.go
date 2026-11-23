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
	seenReleaseRegions := map[string]bool{}
	for _, observation := range result.ReleaseInventory {
		if seenReleaseRegions[observation.Region] {
			return ErrReleaseInventory
		}
		seenReleaseRegions[observation.Region] = true
		if err = saveReleaseInventory(ctx, tx, a, observation); err != nil {
			return err
		}
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
	if err = saveMeteringTopology(ctx, tx, a, result, now); err != nil {
		return err
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
	if _, err = tx.ExecContext(ctx, "UPDATE instance_probe_address SET ipv4_json=NULL,ipv6_json=NULL WHERE instance_id IN (SELECT id FROM instance WHERE account_id=?)", a.ID); err != nil {
		return err
	}
	for _, v := range result.Snapshots {
		if err = retainNetworkRates(ctx, tx, a.ID, v); err != nil {
			return err
		}
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
			if err = saveProbeAddresses(ctx, tx, a, v, now); err != nil {
				return err
			}
			if err = saveNetworkHistory(ctx, tx, v); err != nil {
				return err
			}
		}
	}
	seenIdleScopes := map[string]bool{}
	for _, observation := range result.Idle {
		key := model.Scope(observation.Kind, observation.Region)
		if seenIdleScopes[key] {
			return ErrIdleObservation
		}
		seenIdleScopes[key] = true
		if err := saveIdle(ctx, tx, a, observation, now); err != nil {
			return err
		}
	}
	for _, observation := range result.SWASTraffic {
		if err = saveSWASTraffic(ctx, tx, a, observation); err != nil {
			return err
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
	var accounts map[string]model.Account
	needsAccountCDT := false
	for _, kind := range kinds {
		if !allowed[kind] {
			return nil, ErrNotFound
		}
		if kind == "ecs" || kind == "subject" || kind == "package" {
			needsAccountCDT = true
		}
	}
	if needsAccountCDT {
		list, err := s.Accounts(ctx)
		if err != nil {
			return nil, err
		}
		accounts = make(map[string]model.Account, len(list))
		for _, a := range list {
			accounts[a.ID] = a
		}
	}
	items := []map[string]any{}
	for _, kind := range kinds {
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
			if accounts != nil {
				accID, _ := item["account_id"].(string)
				if a, ok := accounts[accID]; ok {
					applySnapshotCDT(a, kind, item)
				}
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

func snapshotBoolPtr(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

func snapshotBoolValue(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

func applySnapshotCDT(a model.Account, kind string, item map[string]any) {
	switch kind {
	case "subject":
		id, _ := item["id"].(string)
		subKind, _ := item["kind"].(string)
		rule, _ := item["metering_rule"].(string)
		resolved := a.ResolveSubjectCDT(id, subKind, rule, snapshotBoolPtr(item["counts_toward_cdt"]))
		item["counts_toward_cdt"] = snapshotBoolValue(resolved)
	case "package":
		id, _ := item["id"].(string)
		pkgKind, _ := item["kind"].(string)
		rule := "unknown"
		switch pkgKind {
		case "cbwp_dominant_traffic":
			rule = "max_in_out_per_period"
		case "cbwp_bandwidth", "cbwp_95":
			rule = "not_metered"
		}
		resolved := a.ResolveSubjectCDT(id, pkgKind, rule, snapshotBoolPtr(item["settled_by_cdt"]))
		item["settled_by_cdt"] = snapshotBoolValue(resolved)
	case "ecs":
		subID, _ := item["billing_subject_id"].(string)
		if subID == "" {
			return
		}
		subKind, _ := item["billing_subject_kind"].(string)
		rule, _ := item["metering_rule"].(string)
		resolved := a.ResolveSubjectCDT(subID, subKind, rule, snapshotBoolPtr(item["counts_toward_cdt"]))
		item["counts_toward_cdt"] = snapshotBoolValue(resolved)
		if resolved != nil && !*resolved {
			item["realtime_protection"] = "not_applicable"
		} else if item["realtime_protection"] == "not_applicable" {
			item["realtime_protection"] = "unprotected"
		}
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrReleaseInventory = errors.New("invalid_release_inventory")

// saveReleaseInventory runs inside SaveScan's account/job-fenced transaction.
// A failed refresh retains the prior payload and successful observation time.
func saveReleaseInventory(ctx context.Context, tx *sql.Tx, a model.Account, o model.ReleaseInventoryObservation) error {
	allowed := false
	for _, region := range a.RegionIDs {
		allowed = allowed || region == o.Region
	}
	if !allowed || o.QueriedAt.IsZero() || (o.Error == nil) != (o.Data != nil) || (o.Error != nil && o.Error.Code == "") {
		return ErrReleaseInventory
	}
	var payload, fetched, code any
	if o.Error != nil {
		code = o.Error.Code
	} else {
		if !o.Data.Valid() {
			return ErrReleaseInventory
		}
		raw, err := json.Marshal(o.Data)
		if err != nil {
			return err
		}
		payload, fetched = string(raw), timestamp(o.QueriedAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO release_inventory(account_id,credential_version,region,fetched_at,last_attempt_at,error_code,payload_json)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(account_id,credential_version,region) DO UPDATE SET
 last_attempt_at=excluded.last_attempt_at,error_code=excluded.error_code,
 fetched_at=coalesce(excluded.fetched_at,release_inventory.fetched_at),
 payload_json=coalesce(excluded.payload_json,release_inventory.payload_json)
 WHERE release_inventory.last_attempt_at<=excluded.last_attempt_at`, a.ID, a.CredentialVersion, o.Region, fetched, timestamp(o.QueriedAt), code, payload)
	return err
}

// ReleasePreview reads one SQLite snapshot and never requests a cloud action.
func (s *Store) ReleasePreview(ctx context.Context, instanceID string, now time.Time) (model.ReleasePreview, error) {
	v := model.ReleasePreview{InstanceID: instanceID, Status: "never_fetched", BlockReasons: []string{}}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	var cloudID, kind, region string
	var present int
	err = tx.QueryRowContext(ctx, `SELECT i.account_id,i.cloud_id,i.kind,i.region,coalesce(r.present,0) FROM instance i
 LEFT JOIN resource_snapshot r ON r.account_id=i.account_id AND r.kind=i.kind AND r.id=i.id WHERE i.id=?`, instanceID).Scan(&v.AccountID, &cloudID, &kind, &region, &present)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	a, err := loadAccount(ctx, tx, v.AccountID)
	if err != nil {
		return v, err
	}
	if kind != "ecs" {
		v.Status, v.BlockReasons = "blocked", []string{"unsupported_instance_kind"}
		return v, tx.Commit()
	}
	if !a.Enabled {
		v.BlockReasons = append(v.BlockReasons, "account_disabled")
	}
	allowed := false
	for _, configured := range a.RegionIDs {
		allowed = allowed || configured == region
	}
	if !allowed {
		v.BlockReasons = append(v.BlockReasons, "region_not_enabled")
	}
	if present != 1 {
		v.BlockReasons = append(v.BlockReasons, "instance_not_present")
	}
	scopeBlocked := len(v.BlockReasons) != 0
	var fetched, attempted, code, raw sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT fetched_at,last_attempt_at,error_code,payload_json FROM release_inventory
 WHERE account_id=? AND credential_version=? AND region=?`, a.ID, a.CredentialVersion, region).Scan(&fetched, &attempted, &code, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v.FetchedAt, v.LastAttemptAt = parseAgentTime(fetched), parseAgentTime(attempted)
	if v.FetchedAt != nil {
		expiry := v.FetchedAt.Add(model.ReleasePreviewLifetime)
		v.ExpiresAt = &expiry
	}
	if raw.Valid {
		var data model.ReleaseInventory
		if err := json.Unmarshal([]byte(raw.String), &data); err != nil {
			return v, err
		}
		if !data.Valid() {
			return v, ErrReleaseInventory
		}
		var reasons []string
		v.Impact, reasons = data.ImpactFor(cloudID)
		v.BlockReasons = append(v.BlockReasons, reasons...)
	}
	switch {
	case code.Valid:
		v.Status, v.LastError = "unavailable", model.Error(code.String)
		v.BlockReasons = append(v.BlockReasons, "release_information_unavailable")
	case !raw.Valid || v.FetchedAt == nil:
		v.BlockReasons = append(v.BlockReasons, "release_information_unavailable")
	case now.Before(*v.FetchedAt) || !now.Before(*v.ExpiresAt):
		v.Status = "stale"
		v.BlockReasons = append(v.BlockReasons, "release_information_stale")
	case len(v.BlockReasons) != 0:
		v.Status = "blocked"
	default:
		v.Status = "eligible"
		token := model.ReleasePreviewToken(instanceID, a.Revision, a.CredentialVersion, *v.FetchedAt, *v.Impact)
		v.Token = &token
	}
	if scopeBlocked {
		v.Status, v.Token = "blocked", nil
	}
	return v, tx.Commit()
}

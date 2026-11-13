package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrNotFound = errors.New("not_found")
var ErrNameTaken = errors.New("account_name_taken")
var ErrAccountDisabled = errors.New("account_disabled")
var ErrLeaseLost = errors.New("lease_lost")
var ErrAccountChanged = errors.New("account_changed")
var ErrAccountBusy = errors.New("account_busy")

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAccount(ctx context.Context, q queryRower, id string) (model.Account, error) {
	var a model.Account
	var raw string
	var revision int64
	var count int
	var credentialVersion int64
	err := q.QueryRowContext(ctx, "SELECT config_json,revision,(SELECT count(*) FROM resource_snapshot WHERE account_id=? AND kind IN ('ecs','swas') AND present=1),credential_version FROM account_settings WHERE account_id=?", id, id).Scan(&raw, &revision, &count, &credentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal([]byte(raw), &a); err != nil {
		return a, err
	}
	a.Revision = revision
	a.InstanceCount = count
	a.CredentialVersion = credentialVersion
	return a, nil
}
func (s *Store) Account(ctx context.Context, id string) (model.Account, error) {
	return loadAccount(ctx, s.DB, id)
}
func (s *Store) Accounts(ctx context.Context) ([]model.Account, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT a.config_json,a.revision,a.credential_version,
 (SELECT count(*) FROM resource_snapshot r WHERE r.account_id=a.account_id AND r.kind IN ('ecs','swas') AND r.present=1)
 FROM account_settings a ORDER BY a.account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.Account{}
	for rows.Next() {
		var raw string
		var revision int64
		var count int
		var credentialVersion int64
		if err = rows.Scan(&raw, &revision, &credentialVersion, &count); err != nil {
			return nil, err
		}
		var a model.Account
		if err = json.Unmarshal([]byte(raw), &a); err != nil {
			return nil, err
		}
		a.Revision = revision
		a.CredentialVersion = credentialVersion
		a.InstanceCount = count
		items = append(items, a)
	}
	return items, rows.Err()
}
func nameAvailable(ctx context.Context, q queryRower, name, id string) error {
	var count int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM account WHERE name=? AND id<>?", name, id).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrNameTaken
	}
	return nil
}
func (s *Store) CreateAccount(ctx context.Context, a model.Account, cipher []byte, interval time.Duration) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = nameAvailable(ctx, tx, a.Name, a.ID); err != nil {
		return model.Job{}, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO account(id,name,site,updated_at) VALUES(?,?,?,?)", a.ID, a.Name, a.Site, timestamp(a.UpdatedAt)); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO account_settings(account_id,config_json,enabled,next_scan_at) VALUES(?,?,?,?)", a.ID, string(raw), a.Enabled, a.UpdatedAt.Add(interval).Unix()); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO credential(owner_field,ciphertext,updated_at) VALUES(?,?,?)", model.CredentialOwner(a.ID), cipher, timestamp(a.UpdatedAt)); err != nil {
		return model.Job{}, err
	}
	j, err := enqueueJob(ctx, tx, "permission_check", a.ID, []string{a.ID}, a.UpdatedAt)
	if err != nil {
		return j, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "account_created", TargetType: "account", TargetID: a.ID, AccountID: a.ID, Details: model.AuditDetails{ChangedFields: []string{"name", "site", "region_ids", "enabled", "quota_mainland_bytes", "quota_overseas_bytes", "reset_day", "alert_thresholds_percent"}, CredentialsChanged: true, Revision: new(int64(1))}}, a.UpdatedAt); err != nil {
		return j, err
	}
	return j, tx.Commit()
}

// The caller supplies a short mutation which validates the combined record.
// Configuration and credential rotation commit together and invalidate leases.
func (s *Store) UpdateAccount(ctx context.Context, id string, change func(*model.Account) ([]byte, error), now time.Time, interval time.Duration) (model.Account, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Account{}, err
	}
	defer tx.Rollback()
	a, err := loadAccount(ctx, tx, id)
	if err != nil {
		return a, err
	}
	previous := a
	previous.RegionIDs = slices.Clone(a.RegionIDs)
	previous.Thresholds = slices.Clone(a.Thresholds)
	cipher, err := change(&a)
	if err != nil {
		return a, err
	}
	if err = nameAvailable(ctx, tx, a.Name, id); err != nil {
		return a, err
	}
	a.UpdatedAt = now
	a.Revision++
	raw, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE account SET name=?,updated_at=? WHERE id=?", a.Name, timestamp(now), id); err != nil {
		return a, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET config_json=?,enabled=?,revision=?,lease_owner=NULL,lease_until=0,next_scan_at=? WHERE account_id=?", string(raw), a.Enabled, a.Revision, now.Add(interval).Unix(), id); err != nil {
		return a, err
	}
	if cipher != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET credential_version=credential_version+1 WHERE account_id=?", id); err != nil {
			return a, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE credential SET ciphertext=?,updated_at=? WHERE owner_field=?", cipher, timestamp(now), model.CredentialOwner(id)); err != nil {
			return a, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code='account_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE scope IN (?,?,?,?) AND status IN ('queued','running')", timestamp(now), id, "cdt:"+id, "inventory:"+id, "system-events:"+id); err != nil {
		return a, err
	}
	// A newly enabled account is discovered even if its prior refresh was queued.
	if !a.Enabled {
		if _, err = tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE account_id=? AND active=1", timestamp(now), id); err != nil {
			return a, err
		}
		if err = cancelPendingNotifications(ctx, tx, "account_id=?", []any{id}, "account_disabled", now); err != nil {
			return a, err
		}
	}
	if a.Enabled {
		if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET next_cdt_at=0,next_system_event_at=0 WHERE account_id=?", id); err != nil {
			return a, err
		}
		kind := "refresh"
		if cipher != nil || a.PermissionCheck == nil {
			kind = "permission_check"
		}
		if _, err = enqueueJob(ctx, tx, kind, id, []string{id}, now); err != nil {
			return a, err
		}
	}
	action := "account_updated"
	if previous.Enabled && !a.Enabled {
		action = "account_disabled"
	}
	details := model.AuditDetails{ChangedFields: auditChanges(
		auditChange{"name", previous.Name, a.Name}, auditChange{"region_ids", previous.RegionIDs, a.RegionIDs},
		auditChange{"enabled", previous.Enabled, a.Enabled}, auditChange{"quota_mainland_bytes", previous.QuotaMainland, a.QuotaMainland},
		auditChange{"quota_overseas_bytes", previous.QuotaOverseas, a.QuotaOverseas}, auditChange{"reset_day", previous.ResetDay, a.ResetDay},
		auditChange{"alert_thresholds_percent", previous.Thresholds, a.Thresholds}), CredentialsChanged: cipher != nil, Revision: &a.Revision}
	if err = appendAudit(ctx, tx, auditEvent{Action: action, TargetType: "account", TargetID: id, AccountID: id, Details: details}, now); err != nil {
		return a, err
	}
	return a, tx.Commit()
}
func (s *Store) AccountCipher(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	err := s.DB.QueryRowContext(ctx, "SELECT ciphertext FROM credential WHERE owner_field=?", model.CredentialOwner(id)).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) BeginAccountScan(ctx context.Context, id, owner string, now time.Time) (model.Account, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Account{}, err
	}
	defer tx.Rollback()
	a, err := loadAccount(ctx, tx, id)
	if err != nil {
		return a, err
	}
	if !a.Enabled {
		return a, ErrAccountDisabled
	}
	res, err := tx.ExecContext(ctx, "UPDATE account_settings SET lease_owner=?,lease_until=? WHERE account_id=? AND lease_until<=?", owner, now.Add(time.Minute).Unix(), id, now.Unix())
	if err != nil {
		return a, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return a, ErrAccountBusy
	}
	return a, tx.Commit()
}
func (s *Store) CheckAccountScan(ctx context.Context, id, owner string, revision int64, now time.Time) error {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM account_settings WHERE account_id=? AND enabled=1 AND revision=? AND lease_owner=? AND lease_until>?", id, revision, owner, now.Unix()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAccountChanged
	}
	return nil
}
func (s *Store) RenewAccountScan(ctx context.Context, id, owner string, revision int64, now time.Time) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE account_settings SET lease_until=? WHERE account_id=? AND enabled=1 AND revision=? AND lease_owner=? AND lease_until>?", now.Add(time.Minute).Unix(), id, revision, owner, now.Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrAccountChanged
	}
	return nil
}
func (s *Store) ReleaseAccountScan(ctx context.Context, id, owner string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE account_settings SET lease_owner=NULL,lease_until=0 WHERE account_id=? AND lease_owner=?", id, owner)
	return err
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

// SnapshotTo creates a standalone, consistent SQLite file, including committed
// WAL data. The caller supplies a new path inside a private temporary directory.
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Use a separate read-only connection so a large backup does not occupy
	// the server's one pooled connection needed for reports and state queries.
	var source string
	if err := s.DB.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&source); err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(source)}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	reader, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = reader.ExecContext(ctx, "VACUUM main INTO ?", path)
	return err
}

type RestoreSummary struct {
	CancelledJobs          int64 `json:"cancelled_jobs"`
	CancelledNotifications int64 `json:"cancelled_notifications"`
}

// PrepareRestoredCopy applies the user's restore policy only to a quarantined
// copy. Past completion/delivery history is retained; unfinished work is not
// replayed. Pending notifications may have been delivered since the backup.
func (s *Store) PrepareRestoredCopy(ctx context.Context, now time.Time) (RestoreSummary, error) {
	var result RestoreSummary
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// A backup captured between the dispatch marker and response cannot tell
	// whether that cloud operation completed. Keep that uncertainty in history
	// while cancelling the work, never turning it into a queued retry.
	if _, err = tx.ExecContext(ctx, "UPDATE instance_power_job SET dispatch_outcome='unknown' WHERE dispatch_outcome='dispatching' AND job_id IN (SELECT id FROM job WHERE status IN ('queued','running'))"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+powerResultSQL+" WHERE type='instance_power' AND status IN ('queued','running')"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance_release_job SET dispatch_outcome='unknown' WHERE dispatch_outcome='dispatching' AND job_id IN (SELECT id FROM job WHERE status IN ('queued','running'))"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+releaseResultSQL+" WHERE type='instance_release' AND status IN ('queued','running')"); err != nil {
		return result, err
	}
	jobs, err := tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code='backup_restored',finished_at=?,lease_owner=NULL,lease_until=0 WHERE status IN ('queued','running')", timestamp(now))
	if err != nil {
		return result, err
	}
	result.CancelledJobs, err = jobs.RowsAffected()
	if err != nil {
		return result, err
	}
	notifications, err := tx.ExecContext(ctx, "UPDATE notification SET delivery_status='failed',last_error_code='backup_restored',uncertain=1,lease_owner=NULL,lease_until=0 WHERE delivery_status='pending'")
	if err != nil {
		return result, err
	}
	result.CancelledNotifications, err = notifications.RowsAffected()
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE trace_enrichment SET state='failed',updated_at=?,error_code='backup_restored',result_json=NULL,lease_owner=NULL,lease_until=0 WHERE state IN ('queued','running')", timestamp(now)); err != nil {
		return result, err
	}
	comparison := model.PendingTraceComparison()
	comparison.Status, comparison.Reason = "not_comparable", new("backup_restored")
	rawComparison, err := json.Marshal(comparison)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE trace_snapshot_run SET comparison_json=?,updated_at=? WHERE comparison_json IS NULL", string(rawComparison), timestamp(now)); err != nil {
		return result, err
	}
	for _, query := range []string{
		"DELETE FROM session",
		"DELETE FROM setup_credential",
		"DELETE FROM passkey_ceremony",
		"UPDATE passkey_state SET epoch=epoch+1 WHERE id=1",
		"DELETE FROM agent_registration",
		"UPDATE job SET lease_owner=NULL,lease_until=0",
		"UPDATE heartbeat_settings SET lease_owner=NULL,lease_until=0,next_due_at=0",
		"UPDATE notification SET lease_owner=NULL,lease_until=0",
		"UPDATE account_settings SET lease_owner=NULL,lease_until=0,next_scan_at=0,next_cdt_at=0,next_system_event_at=0",
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE quota_power_job SET final_notified=1"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE automation_power_job SET final_notified=1"); err != nil {
		return result, err
	}

	// An old backup cannot know operations sent after it was captured. Preserve
	// its action ledger, invalidate source caches, and wait one budget window.
	if _, err = tx.ExecContext(ctx, "INSERT INTO app_meta(key,value) VALUES('protection_restore_until',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", timestamp(now.Add(10*time.Minute))); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM traffic_estimate"); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metering_topology SET complete=0,quality='observed'"); err != nil {
		return result, err
	}

	return result, tx.Commit()
}

func (s *Store) CheckBackupIntegrity(ctx context.Context) error {
	if err := s.Ready(ctx); err != nil {
		return err
	}
	var check string
	if err := s.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return errors.New("backup_database_integrity_failed")
	}
	rows, err := s.DB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("backup_database_integrity_failed")
	}
	return rows.Err()
}

// CheckBackupFile validates the source mode and migration lineage without
// initializing an empty file or applying migrations to an unknown database.
func CheckBackupFile(ctx context.Context, path string) error {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='mode'").Scan(&mode); err != nil {
		return err
	}
	if mode != "production" {
		return errors.New("backup_requires_production_database")
	}
	rows, err := db.QueryContext(ctx, "SELECT name FROM schema_migration ORDER BY name")
	if err != nil {
		return err
	}
	defer rows.Close()
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if count >= len(files) || name != files[count].Name() {
			return errors.New("unsupported_backup_schema")
		}
		count++
	}
	if count == 0 {
		return errors.New("unsupported_backup_schema")
	}
	return rows.Err()
}

// ReencryptCopy is only for isolated backup/restore copies, never a running
// database. It rewraps every current reversible credential with its original
// owner AAD, including the master-key verifier, and removes login sessions.
func (s *Store) ReencryptCopy(ctx context.Context, from, to *security.Box) error {
	if from == nil || to == nil {
		return ErrMasterKey
	}
	if err := s.Ready(ctx); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT owner_field,ciphertext FROM credential ORDER BY owner_field")
	if err != nil {
		return err
	}
	type replacement struct {
		owner  string
		sealed []byte
	}
	items := []replacement{}
	for rows.Next() {
		var owner string
		var sealed []byte
		if err = rows.Scan(&owner, &sealed); err != nil {
			break
		}
		plain, openErr := from.Decrypt(sealed, owner)
		if openErr != nil {
			err = ErrMasterKey
			break
		}
		rewrapped, sealErr := to.Encrypt(plain, owner)
		clear(plain)
		if sealErr != nil {
			err = sealErr
			break
		}
		items = append(items, replacement{owner, rewrapped})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, "UPDATE credential SET ciphertext=? WHERE owner_field=?", item.sealed, item.owner); err != nil {
			return err
		}
	}
	var sealed []byte
	err = tx.QueryRowContext(ctx, "SELECT ciphertext FROM telegram_channel WHERE id=1").Scan(&sealed)
	if err == nil {
		plain, openErr := from.Decrypt(sealed, model.TelegramCredentialOwner)
		if openErr != nil {
			return ErrMasterKey
		}
		rewrapped, sealErr := to.Encrypt(plain, model.TelegramCredentialOwner)
		clear(plain)
		if sealErr != nil {
			return sealErr
		}
		if _, err = tx.ExecContext(ctx, "UPDATE telegram_channel SET ciphertext=? WHERE id=1", rewrapped); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM session"); err != nil {
		return err
	}
	return tx.Commit()
}

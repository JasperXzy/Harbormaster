package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/JasperXzy/harbormaster/internal/model"
	"time"
)

func readQuotaSimulation(ctx context.Context, q queryRower, account, scope, source string) (model.QuotaSimulation, error) {
	v := model.QuotaSimulation{AccountID: account, Scope: scope, SourceInstanceID: source}
	var expires string
	err := q.QueryRowContext(ctx, "SELECT revision,usage_bytes,expires_at FROM quota_simulation WHERE account_id=? AND scope=? AND source_instance_id=?", account, scope, source).Scan(&v.Revision, &v.Usage, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	return v, err
}
func (s *Store) QuotaSimulations(ctx context.Context, now time.Time) ([]model.QuotaSimulation, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT account_id,scope,source_instance_id,revision,usage_bytes,expires_at FROM quota_simulation ORDER BY account_id,scope,source_instance_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.QuotaSimulation{}
	for rows.Next() {
		var v model.QuotaSimulation
		var expires string
		if err = rows.Scan(&v.AccountID, &v.Scope, &v.SourceInstanceID, &v.Revision, &v.Usage, &expires); err != nil {
			return nil, err
		}
		v.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, err
		}
		if v.ExpiresAt.After(now) {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
func (s *Store) SaveQuotaSimulation(ctx context.Context, v model.QuotaSimulation, now time.Time) (model.QuotaSimulation, error) {
	if v.Revision < 0 || v.Usage < 0 || !v.ExpiresAt.After(now) || v.ExpiresAt.Sub(now) > 24*time.Hour || v.Scope != "mainland" && v.Scope != "overseas" && v.Scope != "swas" || v.Scope != "swas" && v.SourceInstanceID != "" || v.Scope == "swas" && v.SourceInstanceID == "" {
		return v, ErrProtectionRule
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	a, err := loadAccount(ctx, tx, v.AccountID)
	if err != nil {
		return v, err
	}
	if !a.Enabled {
		return v, ErrAccountDisabled
	}
	if v.Scope == "swas" {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=? AND account_id=? AND kind='swas'", v.SourceInstanceID, v.AccountID).Scan(&n); err != nil {
			return v, err
		}
		if n != 1 {
			return v, ErrProtectionRule
		}
	}
	old, err := readQuotaSimulation(ctx, tx, v.AccountID, v.Scope, v.SourceInstanceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return v, err
	}
	if old.Revision != v.Revision && !(v.Revision == 0 && !old.ExpiresAt.After(now)) {
		return v, ErrEstimationRevision
	}
	v.Revision = old.Revision + 1
	if _, err = tx.ExecContext(ctx, `INSERT INTO quota_simulation VALUES(?,?,?,?,?,?) ON CONFLICT(account_id,scope,source_instance_id) DO UPDATE SET revision=excluded.revision,usage_bytes=excluded.usage_bytes,expires_at=excluded.expires_at`, v.AccountID, v.Scope, v.SourceInstanceID, v.Revision, v.Usage, timestamp(v.ExpiresAt)); err != nil {
		return v, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "quota_simulation_saved", TargetType: "account", TargetID: v.AccountID, AccountID: v.AccountID, Details: model.AuditDetails{ChangedFields: []string{"simulation"}, Revision: &v.Revision}}, now); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Store) ClearQuotaSimulation(ctx context.Context, v model.QuotaSimulation, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := readQuotaSimulation(ctx, tx, v.AccountID, v.Scope, v.SourceInstanceID)
	if err != nil {
		return err
	}
	if old.Revision != v.Revision {
		return ErrEstimationRevision
	}
	// Keep the revision tombstone to prevent ABA and stale editor replacement.
	if _, err = tx.ExecContext(ctx, "UPDATE quota_simulation SET revision=revision+1,expires_at=? WHERE account_id=? AND scope=? AND source_instance_id=?", timestamp(now), v.AccountID, v.Scope, v.SourceInstanceID); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "quota_simulation_cleared", TargetType: "account", TargetID: v.AccountID, AccountID: v.AccountID}, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) SetQuotaExemption(ctx context.Context, id string, seconds int, now time.Time) (time.Time, error) {
	if seconds < 1 || seconds > 86400 {
		return time.Time{}, ErrProtectionRule
	}
	expires := now.Add(time.Duration(seconds) * time.Second)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return expires, err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", id).Scan(&n); err != nil {
		return expires, err
	}
	if n != 1 {
		return expires, ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO quota_exemption VALUES(?,?,'admin') ON CONFLICT(instance_id) DO UPDATE SET expires_at=excluded.expires_at,source=excluded.source", id, timestamp(expires)); err != nil {
		return expires, err
	}
	if err = rearmQuotaExemption(ctx, tx, id); err != nil {
		return expires, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "quota_exemption_saved", TargetType: "instance", TargetID: id}, now); err != nil {
		return expires, err
	}
	return expires, tx.Commit()
}
func (s *Store) ClearQuotaExemption(ctx context.Context, id string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM quota_exemption WHERE instance_id=?", id); err != nil {
		return err
	}
	if err = rearmQuotaExemption(ctx, tx, id); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "quota_exemption_cleared", TargetType: "instance", TargetID: id}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func rearmQuotaExemption(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE quota_rule_state SET active=0,signature='' WHERE rule_id IN (SELECT q.id FROM quota_rule q WHERE q.deleted=0 AND EXISTS(SELECT 1 FROM json_each(q.config_json,'$.target_instance_ids') WHERE value=?))`, id)
	return err
}

func (s *Store) QuotaExemptions(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT instance_id,expires_at FROM quota_exemption")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		at, e := time.Parse(time.RFC3339Nano, raw)
		if e != nil {
			return nil, e
		}
		if at.After(now) {
			out[id] = at
		}
	}
	return out, rows.Err()
}

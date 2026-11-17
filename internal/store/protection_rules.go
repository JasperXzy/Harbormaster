package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrProtectionRule = errors.New("invalid_protection_rule")
var ErrProtectionGuard = errors.New("automatic_power_guard_blocked")

func readProtectionRule(ctx context.Context, q queryRower, id string) (model.ProtectionRule, error) {
	var r model.ProtectionRule
	var raw, created, updated string
	err := q.QueryRowContext(ctx, "SELECT id,revision,config_json,created_at,updated_at FROM quota_rule WHERE id=? AND deleted=0", id).Scan(&r.ID, &r.Revision, &raw, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	revision := r.Revision
	if err = json.Unmarshal([]byte(raw), &r.ProtectionRuleInput); err != nil {
		return r, err
	}
	r.Revision = revision
	if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return r, err
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return r, err
}
func (s *Store) ProtectionRules(ctx context.Context) ([]model.ProtectionRule, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM quota_rule WHERE deleted=0 ORDER BY created_at,id")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.ProtectionRule{}
	for _, id := range ids {
		r, e := readProtectionRule(ctx, s.DB, id)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func validateProtectionRule(ctx context.Context, tx *sql.Tx, in model.ProtectionRuleInput) error {
	if in.Revision < 0 || !utf8.ValidString(in.Name) || strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n") || in.Threshold <= 0 || in.ThresholdKind != "bytes" && in.ThresholdKind != "percent" || in.ThresholdKind == "percent" && in.Threshold > 100 || in.ReserveSeconds < 0 || in.ReserveSeconds > 3600 || in.Action != "notify" && in.Action != "stop_notify" || in.Targets == nil || len(in.Targets) > 1000 || in.Action == "stop_notify" && len(in.Targets) == 0 {
		return ErrProtectionRule
	}
	if in.Scope != "mainland" && in.Scope != "overseas" && in.Scope != "swas" || in.Scope != "swas" && in.SourceInstanceID != "" || in.Scope == "swas" && in.SourceInstanceID == "" {
		return ErrProtectionRule
	}
	a, err := loadAccount(ctx, tx, in.AccountID)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return ErrAccountDisabled
	}
	if in.Scope == "swas" {
		var n int
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=? AND account_id=? AND kind='swas'", in.SourceInstanceID, in.AccountID).Scan(&n)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrProtectionRule
		}
	}
	for i, id := range in.Targets {
		if id == "" || i > 0 && id == in.Targets[i-1] {
			return ErrProtectionRule
		}
		var kind, region, raw string
		err = tx.QueryRowContext(ctx, `SELECT i.kind,i.region,r.payload_json FROM instance i JOIN resource_snapshot r ON r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1 WHERE i.id=? AND i.account_id=?`, id, in.AccountID).Scan(&kind, &region, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProtectionRule
		}
		if err != nil {
			return err
		}
		if !slices.Contains(a.RegionIDs, region) {
			return ErrProtectionRule
		}
		if in.Scope == "swas" && (kind != "swas" || id != in.SourceInstanceID) || in.Scope != "swas" && kind != "ecs" {
			return ErrProtectionRule
		}
		if in.Action == "stop_notify" && !model.ValidPowerAction(kind, "stop", in.StopMode) {
			return ErrProtectionRule
		}
		if kind == "ecs" && in.StopMode == "stop_charging" {
			var facts struct {
				Charge string `json:"charge_type"`
			}
			if json.Unmarshal([]byte(raw), &facts) != nil || facts.Charge != "pay_as_you_go" && facts.Charge != "spot" {
				return ErrProtectionRule
			}
		}
	}
	if in.Action == "notify" && (len(in.Targets) != 0 || in.StopMode != "") {
		return ErrProtectionRule
	}
	if in.ConfirmedSelfID != "" && !slices.Contains(in.Targets, in.ConfirmedSelfID) {
		return ErrProtectionRule
	}
	return nil
}
func cancelQuotaJobs(ctx context.Context, tx *sql.Tx, rule, code string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code=?,finished_at=?,lease_owner=NULL,lease_until=0
 WHERE status IN ('queued','running') AND id IN(SELECT q.job_id FROM quota_power_job q JOIN instance_power_job p ON p.job_id=q.job_id WHERE q.rule_id=? AND p.dispatch_started_at IS NULL)`, code, timestamp(now), rule)
	return err
}
func (s *Store) SaveProtectionRule(ctx context.Context, id string, in model.ProtectionRuleInput, now time.Time) (model.ProtectionRule, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.ProtectionRule{}, err
	}
	defer tx.Rollback()
	slices.Sort(in.Targets)
	if err = validateProtectionRule(ctx, tx, in); err != nil {
		return model.ProtectionRule{}, err
	}
	created := now
	if id == "" {
		if in.Revision != 0 {
			return model.ProtectionRule{}, ErrEstimationRevision
		}
		id = model.NewID("quota_")
	} else {
		old, e := readProtectionRule(ctx, tx, id)
		if e != nil {
			return old, e
		}
		if old.Revision != in.Revision {
			return old, ErrEstimationRevision
		}
		created = old.CreatedAt
		if err = cancelQuotaJobs(ctx, tx, id, "quota_rule_changed", now); err != nil {
			return old, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM quota_rule_state WHERE rule_id=?", id); err != nil {
			return old, err
		}
	}
	in.Revision++
	raw, err := json.Marshal(in)
	if err != nil {
		return model.ProtectionRule{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO quota_rule(id,revision,enabled,account_id,config_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,account_id=excluded.account_id,config_json=excluded.config_json,updated_at=excluded.updated_at`, id, in.Revision, in.Enabled, in.AccountID, string(raw), timestamp(created), timestamp(now)); err != nil {
		return model.ProtectionRule{}, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "quota_rule_saved", TargetType: "quota_rule", TargetID: id, AccountID: in.AccountID, Details: model.AuditDetails{ChangedFields: []string{"rule"}, Revision: &in.Revision}}, now); err != nil {
		return model.ProtectionRule{}, err
	}
	r, err := readProtectionRule(ctx, tx, id)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func (s *Store) DeleteProtectionRule(ctx context.Context, id string, revision int64, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readProtectionRule(ctx, tx, id)
	if err != nil {
		return err
	}
	if r.Revision != revision {
		return ErrEstimationRevision
	}
	if err = cancelQuotaJobs(ctx, tx, id, "quota_rule_deleted", now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE quota_rule SET deleted=1,enabled=0,revision=revision+1,updated_at=? WHERE id=?", timestamp(now), id); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "quota_rule_deleted", TargetType: "quota_rule", TargetID: id, AccountID: r.AccountID}, now); err != nil {
		return err
	}
	return tx.Commit()
}

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func readFailoverRuleRow(row rowScanner) (model.FailoverRule, string, error) {
	var r model.FailoverRule
	var en, dr int
	var lastKey, lastAt sql.NullString
	var created, updated string
	if err := row.Scan(&r.ID, &r.Revision, &en, &dr, &r.Name, &r.PrimaryInstanceID, &r.StandbyInstanceID, &lastKey, &lastAt, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, "", ErrNotFound
		}
		return r, "", err
	}
	r.Enabled = en == 1
	r.DryRun = dr == 1
	r.LastTriggeredAt = parseAgentTime(lastAt)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return r, lastKey.String, nil
}

func readFailoverRuleTx(ctx context.Context, q queryRower, id string) (model.FailoverRule, string, error) {
	return readFailoverRuleRow(q.QueryRowContext(ctx, `SELECT id,revision,enabled,dry_run,name,primary_instance_id,standby_instance_id,last_trigger_key,last_triggered_at,created_at,updated_at
 FROM failover_rule WHERE id=? AND deleted=0`, id))
}

func (s *Store) FailoverRules(ctx context.Context) ([]model.FailoverRule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,revision,enabled,dry_run,name,primary_instance_id,standby_instance_id,last_trigger_key,last_triggered_at,created_at,updated_at
 FROM failover_rule WHERE deleted=0 ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.FailoverRule{}
	for rows.Next() {
		r, _, err := readFailoverRuleRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SaveFailoverRule(ctx context.Context, id string, in model.FailoverRuleInput, now time.Time) (model.FailoverRule, error) {
	if len(in.Validate()) > 0 {
		return model.FailoverRule{}, ErrAutomationConfig
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.FailoverRule{}, err
	}
	defer tx.Rollback()

	for _, instID := range []string{in.PrimaryInstanceID, in.StandbyInstanceID} {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", instID).Scan(&n); err != nil {
			return model.FailoverRule{}, err
		}
		if n != 1 {
			return model.FailoverRule{}, ErrNotFound
		}
	}

	enInt, drInt := 0, 0
	if in.Enabled {
		enInt = 1
	}
	if in.DryRun {
		drInt = 1
	}
	cleanName := strings.TrimSpace(in.Name)

	if id == "" {
		if in.Revision != 0 {
			return model.FailoverRule{}, ErrEstimationRevision
		}
		id = model.NewID("fo_")
		rev := int64(1)
		if _, err = tx.ExecContext(ctx, `INSERT INTO failover_rule(id,revision,enabled,dry_run,name,primary_instance_id,standby_instance_id,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?)`, id, rev, enInt, drInt, cleanName, in.PrimaryInstanceID, in.StandbyInstanceID, timestamp(now), timestamp(now)); err != nil {
			return model.FailoverRule{}, err
		}
		details := model.AuditDetails{
			ChangedFields: []string{"name", "enabled", "dry_run", "primary_instance_id", "standby_instance_id"},
			After:         map[string]any{"name": cleanName, "enabled": in.Enabled, "dry_run": in.DryRun, "primary_instance_id": in.PrimaryInstanceID, "standby_instance_id": in.StandbyInstanceID},
			Revision:      &rev,
		}
		if err = appendAudit(ctx, tx, auditEvent{Action: "failover_rule_saved", TargetType: "failover_rule", TargetID: id, Details: details}, now); err != nil {
			return model.FailoverRule{}, err
		}
	} else {
		prev, _, err := readFailoverRuleTx(ctx, tx, id)
		if err != nil {
			return model.FailoverRule{}, err
		}
		if prev.Revision != in.Revision {
			return model.FailoverRule{}, ErrEstimationRevision
		}
		nextRev := prev.Revision + 1
		if _, err = tx.ExecContext(ctx, `UPDATE failover_rule SET revision=?,enabled=?,dry_run=?,name=?,primary_instance_id=?,standby_instance_id=?,updated_at=? WHERE id=? AND deleted=0`,
			nextRev, enInt, drInt, cleanName, in.PrimaryInstanceID, in.StandbyInstanceID, timestamp(now), id); err != nil {
			return model.FailoverRule{}, err
		}
		details := auditDiff(
			auditChange{"name", prev.Name, cleanName},
			auditChange{"enabled", prev.Enabled, in.Enabled},
			auditChange{"dry_run", prev.DryRun, in.DryRun},
			auditChange{"primary_instance_id", prev.PrimaryInstanceID, in.PrimaryInstanceID},
			auditChange{"standby_instance_id", prev.StandbyInstanceID, in.StandbyInstanceID},
		)
		details.Revision = &nextRev
		if err = appendAudit(ctx, tx, auditEvent{Action: "failover_rule_saved", TargetType: "failover_rule", TargetID: id, Details: details}, now); err != nil {
			return model.FailoverRule{}, err
		}
	}
	out, _, err := readFailoverRuleTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) DeleteFailoverRule(ctx context.Context, id string, revision int64, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev, _, err := readFailoverRuleTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if prev.Revision != revision {
		return ErrEstimationRevision
	}
	nextRev := prev.Revision + 1
	if _, err = tx.ExecContext(ctx, "UPDATE failover_rule SET deleted=1,enabled=0,revision=?,updated_at=? WHERE id=?", nextRev, timestamp(now), id); err != nil {
		return err
	}
	details := model.AuditDetails{ChangedFields: []string{"deleted"}, Before: map[string]any{"name": prev.Name}, Revision: &nextRev}
	if err = appendAudit(ctx, tx, auditEvent{Action: "failover_rule_deleted", TargetType: "failover_rule", TargetID: id, Details: details}, now); err != nil {
		return err
	}
	return tx.Commit()
}

// evaluateFailovers implements PRD F6.8: when primary instance A stops due to
// quota protection (desired_state='quota_stopped'), automatically start standby
// instance B (supports cross-account) and send a notification.
func (s *Store) evaluateFailovers(ctx context.Context, policy model.ProtectionPolicy, loc *time.Location, now time.Time) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM failover_rule WHERE deleted=0 AND enabled=1 ORDER BY created_at,id")
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
	rows.Close()
	for _, id := range ids {
		if err := s.evaluateOneFailover(ctx, id, policy, loc, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) evaluateOneFailover(ctx context.Context, ruleID string, policy model.ProtectionPolicy, loc *time.Location, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rule, lastKey, err := readFailoverRuleTx(ctx, tx, ruleID)
	if err != nil || !rule.Enabled {
		return nil
	}

	var pDesired, pStatus, pAccount string
	err = tx.QueryRowContext(ctx, "SELECT desired_state,status,account_id FROM instance WHERE id=?", rule.PrimaryInstanceID).Scan(&pDesired, &pStatus, &pAccount)
	if err != nil {
		return nil
	}
	// Trigger condition: primary instance is quota_stopped (or has a completed quota_power_job in this cycle)
	if pDesired != "quota_stopped" {
		return nil
	}
	var triggerKey string
	_ = tx.QueryRowContext(ctx, `SELECT j.id FROM quota_power_job q JOIN job j ON j.id=q.job_id WHERE j.instance_id=? ORDER BY j.created_at DESC LIMIT 1`, rule.PrimaryInstanceID).Scan(&triggerKey)
	if triggerKey == "" {
		start, _ := ReferenceCycle(now, loc)
		triggerKey = "quota_stopped:" + rule.PrimaryInstanceID + ":" + timestamp(start)
	}
	if lastKey == triggerKey {
		return nil
	}

	// Verify standby instance B is eligible to start (cross-account allowed!)
	blocked, err := instanceBlockedByQuotaRuleTx(ctx, tx, rule.StandbyInstanceID)
	if err != nil || blocked {
		return err
	}
	inStopWin, err := instanceInScheduledStopWindowTx(ctx, tx, rule.StandbyInstanceID, loc, now)
	if err != nil || inStopWin {
		return err
	}
	standbyAcct, _, reason := automaticTargetReadyTx(ctx, tx, rule.StandbyInstanceID, "start", "", "", policy, now)
	if reason == "already_running" {
		// Standby is already running; mark trigger key handled cleanly
		_, err = tx.ExecContext(ctx, "UPDATE failover_rule SET last_trigger_key=?,last_triggered_at=? WHERE id=?", triggerKey, timestamp(now), rule.ID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if reason != "" {
		return nil
	}

	pName := instanceCopyName(ctx, tx, rule.PrimaryInstanceID)
	sName := instanceCopyName(ctx, tx, rule.StandbyInstanceID)
	standbyID := rule.StandbyInstanceID

	if rule.DryRun || !policy.PowerEnabled {
		if _, err = tx.ExecContext(ctx, "UPDATE failover_rule SET last_trigger_key=?,last_triggered_at=? WHERE id=?", triggerKey, timestamp(now), rule.ID); err != nil {
			return err
		}
		if _, err = enqueueNotification(ctx, tx, model.Notification{
			EventKey:   fmt.Sprintf("failover:%s:%s:rehearsal", rule.ID, triggerKey),
			Kind:       "alert",
			Severity:   "info",
			Title:      "🟡 主备切换试运行命中：" + rule.Name,
			Body:       "主实例 " + pName + " 已因额度保护停机，试运行模式下未实际启动备实例 " + sName + "。",
			AccountID:  &standbyAcct,
			InstanceID: &standbyID,
		}, now); err != nil {
			return err
		}
		return tx.Commit()
	}

	sum := sha256.Sum256([]byte(fmt.Sprintf("failover|%s|%s|%s", rule.ID, triggerKey, rule.StandbyInstanceID)))
	j, qErr := enqueuePowerJobTx(model.WithJobTrigger(ctx, "schedule"), tx, rule.StandbyInstanceID, "start", "", hex.EncodeToString(sum[:]), "failover:"+rule.ID, now)
	if errors.Is(qErr, ErrPowerBusy) {
		return nil
	}
	if qErr != nil {
		return qErr
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO failover_power_job(job_id,rule_id,rule_revision,primary_instance_id,trigger_key) VALUES(?,?,?,?,?)",
		j.ID, rule.ID, rule.Revision, rule.PrimaryInstanceID, triggerKey); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE failover_rule SET last_trigger_key=?,last_triggered_at=? WHERE id=?", triggerKey, timestamp(now), rule.ID); err != nil {
		return err
	}
	if _, err = enqueueNotification(ctx, tx, model.Notification{
		EventKey:   fmt.Sprintf("failover:%s:%s:triggered", rule.ID, triggerKey),
		JobID:      j.ID,
		Kind:       "alert",
		Severity:   "warning",
		Title:      "🟠 主备切换已触发：" + pName + " → " + sName,
		Body:       "主实例 " + pName + " 因额度保护停机，正在按规则「" + rule.Name + "」启动备实例 " + sName + "。",
		AccountID:  &standbyAcct,
		InstanceID: &standbyID,
		Urgent:     true,
	}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func authorizeFailoverDispatchTx(ctx context.Context, tx *sql.Tx, p PowerOperation, policy model.ProtectionPolicy, loc *time.Location, now time.Time) (bool, error) {
	var ruleID string
	var ruleRev int64
	err := tx.QueryRowContext(ctx, "SELECT rule_id,rule_revision FROM failover_power_job WHERE job_id=?", p.Job.ID).Scan(&ruleID, &ruleRev)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !policy.PowerEnabled || p.Action != "start" {
		return true, ErrProtectionGuard
	}
	cur, _, fErr := readFailoverRuleTx(ctx, tx, ruleID)
	if fErr != nil || !cur.Enabled || cur.DryRun || cur.Revision != ruleRev {
		return true, ErrProtectionGuard
	}
	blocked, _ := instanceBlockedByQuotaRuleTx(ctx, tx, p.InstanceID)
	inStopWin, _ := instanceInScheduledStopWindowTx(ctx, tx, p.InstanceID, loc, now)
	if blocked || inStopWin {
		return true, ErrProtectionGuard
	}
	if _, _, reason := automaticTargetReadyTx(ctx, tx, p.InstanceID, "start", "", "", policy, now); reason != "" {
		return true, ErrProtectionGuard
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO automatic_power_action(job_id,instance_id,dispatch_started_at) VALUES(?,?,?)", p.Job.ID, p.InstanceID, timestamp(now)); err != nil {
		return true, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance SET desired_state='running' WHERE id=?", p.InstanceID); err != nil {
		return true, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO instance_intent(instance_id,job_id,updated_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET job_id=excluded.job_id,updated_at=excluded.updated_at", p.InstanceID, p.Job.ID, timestamp(now))
	return true, err
}

func finishFailoverNotificationsTx(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT f.job_id,f.primary_instance_id,j.status,i.account_id,i.id
 FROM failover_power_job f
 JOIN job j ON j.id=f.job_id
 JOIN instance i ON i.id=j.instance_id
 WHERE f.final_notified=0 AND j.status IN ('succeeded','failed')`)
	if err != nil {
		return err
	}
	type item struct{ job, primary, status, account, standby string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.job, &v.primary, &v.status, &v.account, &v.standby); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	rows.Close()
	for _, v := range items {
		pName := instanceCopyName(ctx, tx, v.primary)
		sName := instanceCopyName(ctx, tx, v.standby)
		sev := "info"
		title := "🟢 主备切换已完成：备实例 " + sName + " 已启动（接替 " + pName + "）"
		if v.status != "succeeded" {
			sev = "critical"
			title = "🔴 主备切换失败：未能启动备实例 " + sName + "，请手动处理"
		}
		if _, err = enqueueNotification(ctx, tx, model.Notification{
			EventKey:   "failover-job:" + v.job + ":finished",
			JobID:      v.job,
			Kind:       "alert",
			Severity:   sev,
			Title:      title,
			Body:       "",
			AccountID:  &v.account,
			InstanceID: &v.standby,
			Urgent:     v.status != "succeeded",
		}, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE failover_power_job SET final_notified=1 WHERE job_id=?", v.job); err != nil {
			return err
		}
	}
	return nil
}

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrAutomationConfig = errors.New("invalid_automation_config")

func readPowerScheduleTx(ctx context.Context, q queryRower, id string) (model.PowerSchedule, error) {
	var out model.PowerSchedule
	var raw, activated, created, updated string
	var lastTrig sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,revision,config_json,activated_at,last_triggered_at,created_at,updated_at
 FROM power_schedule WHERE id=? AND deleted=0`, id).Scan(&out.ID, &out.Revision, &raw, &activated, &lastTrig, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	rev := out.Revision
	if err = json.Unmarshal([]byte(raw), &out.PowerScheduleInput); err != nil {
		return out, err
	}
	out.Revision = rev
	if out.ActivatedAt, err = time.Parse(time.RFC3339Nano, activated); err != nil {
		return out, err
	}
	out.LastTriggeredAt = parseAgentTime(lastTrig)
	if out.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return out, err
	}
	if out.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) PowerSchedules(ctx context.Context) ([]model.PowerSchedule, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM power_schedule WHERE deleted=0 ORDER BY created_at,id")
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
	out := make([]model.PowerSchedule, 0, len(ids))
	for _, id := range ids {
		item, err := readPowerScheduleTx(ctx, s.DB, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func validateScheduleTargetsTx(ctx context.Context, tx *sql.Tx, in model.PowerScheduleInput) error {
	a, err := loadAccount(ctx, tx, in.AccountID)
	if err != nil {
		return err
	}
	if !a.Enabled {
		return ErrAccountDisabled
	}
	hasECS := false
	for _, id := range in.Targets {
		var kind, region, raw string
		err = tx.QueryRowContext(ctx, `SELECT i.kind,i.region,r.payload_json FROM instance i
 JOIN resource_snapshot r ON r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1
 WHERE i.id=? AND i.account_id=?`, id, in.AccountID).Scan(&kind, &region, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAutomationConfig
		}
		if err != nil {
			return err
		}
		if !slices.Contains(a.RegionIDs, region) {
			return ErrAutomationConfig
		}
		if kind == "ecs" {
			hasECS = true
		}
		if in.Action == "stop" {
			stopMode := in.StopMode
			if kind == "swas" {
				stopMode = ""
			}
			if !model.ValidPowerAction(kind, "stop", stopMode) {
				return ErrAutomationConfig
			}
			if kind == "ecs" && in.StopMode == "stop_charging" {
				var facts struct {
					Charge string `json:"charge_type"`
				}
				if json.Unmarshal([]byte(raw), &facts) != nil || (facts.Charge != "pay_as_you_go" && facts.Charge != "spot") {
					return ErrAutomationConfig
				}
			}
		}
	}
	if in.Action == "stop" && hasECS && in.StopMode == "" {
		return ErrAutomationConfig
	}
	return nil
}

func cancelAutomationJobsTx(ctx context.Context, tx *sql.Tx, kind, ruleID, code string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code=?,finished_at=?,lease_owner=NULL,lease_until=0
 WHERE status IN ('queued','running') AND id IN (
  SELECT a.job_id FROM automation_power_job a JOIN instance_power_job p ON p.job_id=a.job_id
  WHERE a.kind=? AND a.rule_id=? AND p.dispatch_started_at IS NULL
 )`, code, timestamp(now), kind, ruleID)
	return err
}

func (s *Store) SavePowerSchedule(ctx context.Context, id string, in model.PowerScheduleInput, now time.Time) (model.PowerSchedule, error) {
	if !in.NormalizeAndValidate() {
		return model.PowerSchedule{}, ErrAutomationConfig
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.PowerSchedule{}, err
	}
	defer tx.Rollback()
	if err = validateScheduleTargetsTx(ctx, tx, in); err != nil {
		return model.PowerSchedule{}, err
	}
	at := now.UTC()
	created := at
	activated := at
	var prev any
	if id == "" {
		if in.Revision != 0 {
			return model.PowerSchedule{}, ErrEstimationRevision
		}
		id = model.NewID("sched_")
	} else {
		old, err := readPowerScheduleTx(ctx, tx, id)
		if err != nil {
			return old, err
		}
		if old.Revision != in.Revision {
			return old, ErrEstimationRevision
		}
		created = old.CreatedAt
		prev = old.PowerScheduleInput
		// User-confirmed policy: newly enabled/edited schedule takes effect from the NEXT scheduled point.
		activated = at
		if err = cancelAutomationJobsTx(ctx, tx, "schedule", id, "schedule_changed", at); err != nil {
			return old, err
		}
	}
	in.Revision++
	raw, err := json.Marshal(in)
	if err != nil {
		return model.PowerSchedule{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO power_schedule(id,revision,enabled,account_id,config_json,activated_at,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,account_id=excluded.account_id,config_json=excluded.config_json,activated_at=excluded.activated_at,updated_at=excluded.updated_at`,
		id, in.Revision, in.Enabled, in.AccountID, string(raw), timestamp(activated), timestamp(created), timestamp(at)); err != nil {
		return model.PowerSchedule{}, err
	}
	details := auditDiff(auditChange{"schedule", prev, in})
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "power_schedule_saved", TargetType: "power_schedule", TargetID: id, AccountID: in.AccountID, Details: details}, at); err != nil {
		return model.PowerSchedule{}, err
	}
	saved, err := readPowerScheduleTx(ctx, tx, id)
	if err != nil {
		return saved, err
	}
	return saved, tx.Commit()
}

func (s *Store) DeletePowerSchedule(ctx context.Context, id string, revision int64, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := readPowerScheduleTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if old.Revision != revision {
		return ErrEstimationRevision
	}
	at := now.UTC()
	if err = cancelAutomationJobsTx(ctx, tx, "schedule", id, "schedule_deleted", at); err != nil {
		return err
	}
	nextRev := revision + 1
	if _, err = tx.ExecContext(ctx, "UPDATE power_schedule SET deleted=1,enabled=0,revision=?,updated_at=? WHERE id=?", nextRev, timestamp(at), id); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "power_schedule_deleted", TargetType: "power_schedule", TargetID: id, AccountID: old.AccountID, Details: model.AuditDetails{ChangedFields: []string{"deleted"}, Revision: &nextRev}}, at); err != nil {
		return err
	}
	return tx.Commit()
}

func readKeepaliveTx(ctx context.Context, q queryRower) (model.KeepaliveSettings, error) {
	out := model.DefaultKeepaliveSettings()
	var raw string
	var at sql.NullString
	err := q.QueryRowContext(ctx, "SELECT revision,config_json,updated_at FROM keepalive_settings WHERE id=1").Scan(&out.Revision, &raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	rev := out.Revision
	if err = json.Unmarshal([]byte(raw), &out); err != nil {
		return out, err
	}
	out.Revision = rev
	if out.Targets == nil {
		out.Targets = []string{}
	}
	out.UpdatedAt = parseAgentTime(at)
	return out, nil
}

func (s *Store) KeepaliveSettings(ctx context.Context) (model.KeepaliveSettings, error) {
	return readKeepaliveTx(ctx, s.DB)
}

func validateManagedInstanceListTx(ctx context.Context, tx *sql.Tx, ids []string) ([]string, error) {
	if ids == nil || len(ids) > 1000 {
		return nil, ErrAutomationConfig
	}
	clean := slices.Clone(ids)
	slices.Sort(clean)
	for i, id := range clean {
		if id == "" || (i > 0 && id == clean[i-1]) {
			return nil, ErrAutomationConfig
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", id).Scan(&n); err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrAutomationConfig
		}
	}
	return clean, nil
}

func (s *Store) SaveKeepaliveSettings(ctx context.Context, in model.KeepaliveSettings, now time.Time) (model.KeepaliveSettings, error) {
	if in.Revision < 0 {
		return model.KeepaliveSettings{}, ErrAutomationConfig
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.KeepaliveSettings{}, err
	}
	defer tx.Rollback()
	clean, err := validateManagedInstanceListTx(ctx, tx, in.Targets)
	if err != nil {
		return model.KeepaliveSettings{}, err
	}
	in.Targets = clean
	cur, err := readKeepaliveTx(ctx, tx)
	if err != nil {
		return cur, err
	}
	if cur.Revision != in.Revision {
		return cur, ErrEstimationRevision
	}
	at := now.UTC()
	if err = cancelAutomationJobsTx(ctx, tx, "keepalive", "1", "keepalive_changed", at); err != nil {
		return cur, err
	}
	in.Revision = cur.Revision + 1
	in.UpdatedAt = &at
	raw, err := json.Marshal(struct {
		Enabled         bool     `json:"enabled"`
		DryRun          bool     `json:"dry_run"`
		SpotAutoRecover bool     `json:"spot_auto_recover"`
		Targets         []string `json:"target_instance_ids"`
	}{Enabled: in.Enabled, DryRun: in.DryRun, SpotAutoRecover: in.SpotAutoRecover, Targets: in.Targets})
	if err != nil {
		return cur, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE keepalive_settings SET revision=?,config_json=?,updated_at=? WHERE id=1", in.Revision, string(raw), timestamp(at)); err != nil {
		return cur, err
	}
	details := auditDiff(
		auditChange{"enabled", cur.Enabled, in.Enabled},
		auditChange{"dry_run", cur.DryRun, in.DryRun},
		auditChange{"spot_auto_recover", cur.SpotAutoRecover, in.SpotAutoRecover},
		auditChange{"target_instance_ids", cur.Targets, in.Targets},
	)
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "keepalive_rule_saved", TargetType: "keepalive", TargetID: "1", Details: details}, at); err != nil {
		return cur, err
	}
	return in, tx.Commit()
}

func readMonthlyResetTx(ctx context.Context, q queryRower) (model.MonthlyResetSettings, error) {
	out := model.DefaultMonthlyResetSettings()
	var raw string
	var lastCycle, at sql.NullString
	err := q.QueryRowContext(ctx, "SELECT revision,config_json,last_cycle_start,updated_at FROM monthly_reset_settings WHERE id=1").Scan(&out.Revision, &raw, &lastCycle, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	rev := out.Revision
	if err = json.Unmarshal([]byte(raw), &out); err != nil {
		return out, err
	}
	out.Revision = rev
	if out.Targets == nil {
		out.Targets = []string{}
	}
	out.LastCycleStart = parseAgentTime(lastCycle)
	out.UpdatedAt = parseAgentTime(at)
	return out, nil
}

func (s *Store) MonthlyResetSettings(ctx context.Context) (model.MonthlyResetSettings, error) {
	return readMonthlyResetTx(ctx, s.DB)
}

func (s *Store) SaveMonthlyResetSettings(ctx context.Context, in model.MonthlyResetSettings, now time.Time) (model.MonthlyResetSettings, error) {
	if in.Revision < 0 {
		return model.MonthlyResetSettings{}, ErrAutomationConfig
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.MonthlyResetSettings{}, err
	}
	defer tx.Rollback()
	clean, err := validateManagedInstanceListTx(ctx, tx, in.Targets)
	if err != nil {
		return model.MonthlyResetSettings{}, err
	}
	in.Targets = clean
	cur, err := readMonthlyResetTx(ctx, tx)
	if err != nil {
		return cur, err
	}
	if cur.Revision != in.Revision {
		return cur, ErrEstimationRevision
	}
	at := now.UTC()
	if err = cancelAutomationJobsTx(ctx, tx, "monthly_reset", "1", "monthly_reset_changed", at); err != nil {
		return cur, err
	}
	in.Revision = cur.Revision + 1
	in.UpdatedAt = &at
	in.LastCycleStart = cur.LastCycleStart
	raw, err := json.Marshal(struct {
		Enabled bool     `json:"enabled"`
		DryRun  bool     `json:"dry_run"`
		Targets []string `json:"target_instance_ids"`
	}{Enabled: in.Enabled, DryRun: in.DryRun, Targets: in.Targets})
	if err != nil {
		return cur, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE monthly_reset_settings SET revision=?,config_json=?,updated_at=? WHERE id=1", in.Revision, string(raw), timestamp(at)); err != nil {
		return cur, err
	}
	details := auditDiff(
		auditChange{"enabled", cur.Enabled, in.Enabled},
		auditChange{"dry_run", cur.DryRun, in.DryRun},
		auditChange{"target_instance_ids", cur.Targets, in.Targets},
	)
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "monthly_reset_saved", TargetType: "monthly_reset", TargetID: "1", Details: details}, at); err != nil {
		return cur, err
	}
	return in, tx.Commit()
}

// PendingIntentConfirmations returns currently pending external-stop confirmations.
func (s *Store) PendingIntentConfirmations(ctx context.Context, ids []string) (map[string]model.PendingIntentConfirmation, error) {
	out := map[string]model.PendingIntentConfirmation{}
	if len(ids) == 0 {
		return out, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT instance_id,detected_at FROM instance_intent_confirmation WHERE status='pending' AND instance_id IN ("+strings.Join(marks, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, atStr string
		if err = rows.Scan(&id, &atStr); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339Nano, atStr); err == nil {
			out[id] = model.PendingIntentConfirmation{DetectedAt: t}
		}
	}
	return out, rows.Err()
}

func checkExternalShutdownTx(ctx context.Context, tx *sql.Tx, accountID, instanceID, instanceName string, snapshotPayload map[string]any, now time.Time) error {
	// Only check when recorded intent was explicitly 'running'.
	var desired string
	err := tx.QueryRowContext(ctx, `SELECT i.desired_state FROM instance i
 JOIN instance_intent d ON d.instance_id=i.id WHERE i.id=?`, instanceID).Scan(&desired)
	if errors.Is(err, sql.ErrNoRows) || desired != "running" {
		return nil
	}
	if err != nil {
		return err
	}
	// If Harbormaster itself requested or dispatched a stop recently, this is not an external console shutdown.
	var hmStop int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM instance_power_job p JOIN job j ON j.id=p.job_id
 WHERE j.instance_id=? AND p.action='stop' AND (j.status IN ('queued','running') OR julianday(coalesce(j.finished_at,j.created_at))>julianday(?))`,
		instanceID, timestamp(now.Add(-15*time.Minute))).Scan(&hmStop); err != nil {
		return err
	}
	if hmStop > 0 {
		return nil
	}
	// Spot instances covered by keepalive with SpotAutoRecover=true skip manual prompt so reclamation auto-recovers!
	charge, _ := snapshotPayload["charge_type"].(string)
	if charge == "spot" {
		ka, kErr := readKeepaliveTx(ctx, tx)
		if kErr == nil && ka.Enabled && ka.SpotAutoRecover && slices.Contains(ka.Targets, instanceID) {
			return nil
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_intent_confirmation(instance_id,account_id,detected_at,status,resolved_at,resolved_by)
 VALUES(?,?,?,'pending',NULL,NULL)
 ON CONFLICT(instance_id) DO UPDATE SET detected_at=excluded.detected_at,status='pending',resolved_at=NULL,resolved_by=NULL`,
		instanceID, accountID, timestamp(now)); err != nil {
		return err
	}
	if instanceName == "" {
		instanceName = instanceID
	}
	ruleKey := "instance_public_ip_changed" // reuse valid rule_key or nil; Notification.RuleKey is nullable!
	_ = ruleKey
	evKey := fmt.Sprintf("external_stop:%s:%d", instanceID, now.UnixNano())
	_, err = enqueueNotification(ctx, tx, model.Notification{
		EventKey:   evKey,
		Kind:       "alert",
		Severity:   "warning",
		Title:      "🟡 " + instanceName + " 在面板之外被关机了",
		Body:       "它原来的期望状态是“运行”。请在面板或下方按钮确认：保持停止，还是恢复运行？未确认前不会触发自动保活。",
		AccountID:  &accountID,
		InstanceID: &instanceID,
		Urgent:     true,
	}, now)
	return err
}

func (s *Store) ConfirmInstanceIntent(ctx context.Context, instanceID, decision string, powerEnabled bool, now time.Time) (string, *string, error) {
	if decision != "keep_stopped" && decision != "restore_running" {
		return "", nil, ErrAutomationConfig
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	var accountID, prevDesired string
	err = tx.QueryRowContext(ctx, "SELECT account_id,desired_state FROM instance WHERE id=?", instanceID).Scan(&accountID, &prevDesired)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	at := now.UTC()
	actor := model.AuditActorForContext(ctx)
	if _, err = tx.ExecContext(ctx, "UPDATE instance_intent_confirmation SET status=?,resolved_at=?,resolved_by=? WHERE instance_id=? AND status='pending'",
		decision, timestamp(at), actor.Kind, instanceID); err != nil {
		return "", nil, err
	}

	var jobID *string
	desired := "stopped"
	if decision == "keep_stopped" {
		if _, err = tx.ExecContext(ctx, "UPDATE instance SET desired_state='stopped' WHERE id=?", instanceID); err != nil {
			return "", nil, err
		}
		// Record an explicit intent anchor job or update existing intent row if present
		var existingJob string
		if qErr := tx.QueryRowContext(ctx, "SELECT job_id FROM instance_intent WHERE instance_id=?", instanceID).Scan(&existingJob); qErr == nil {
			_, _ = tx.ExecContext(ctx, "UPDATE instance_intent SET updated_at=? WHERE instance_id=?", timestamp(at), instanceID)
		}
	} else {
		desired = "running"
		if _, err = tx.ExecContext(ctx, "UPDATE instance SET desired_state='running' WHERE id=?", instanceID); err != nil {
			return "", nil, err
		}
		if powerEnabled {
			sum := sha256.Sum256([]byte(fmt.Sprintf("intent-restore|%s|%d", instanceID, at.UnixNano())))
			j, pErr := enqueuePowerJobTx(ctx, tx, instanceID, "start", "", hex.EncodeToString(sum[:]), "", at)
			if pErr == nil {
				jobID = &j.ID
			} else if !errors.Is(pErr, ErrPowerBusy) {
				return "", nil, pErr
			}
		}
	}
	details := auditDiff(auditChange{"desired_state", prevDesired, desired}, auditChange{"decision", "pending", decision})
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_intent_confirmed", TargetType: "instance", TargetID: instanceID, AccountID: accountID, Details: details}, at); err != nil {
		return "", nil, err
	}
	return desired, jobID, tx.Commit()
}

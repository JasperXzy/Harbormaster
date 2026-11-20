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
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func parseCivilHM(hm string) (int, int) {
	parts := strings.Split(hm, ":")
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}

func instanceBlockedByQuotaRuleTx(ctx context.Context, tx *sql.Tx, instanceID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT q.config_json FROM quota_rule q
 JOIN quota_rule_state s ON s.rule_id=q.id
 WHERE q.deleted=0 AND q.enabled=1 AND s.active=1`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		var r model.ProtectionRuleInput
		if json.Unmarshal([]byte(raw), &r) == nil && r.Action == "stop_notify" && !r.DryRun && slices.Contains(r.Targets, instanceID) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// instanceInScheduledStopWindowTx checks F6.6 priority: "定时停机期间不触发保活".
// An instance is in a scheduled stop window if its most recent triggered/scheduled
// daily rule in the last 24h is a 'stop' rule and no newer 'start' rule or newer
// manual intent occurred afterward.
func instanceInScheduledStopWindowTx(ctx context.Context, tx *sql.Tx, instanceID string, loc *time.Location, now time.Time) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT config_json,activated_at FROM power_schedule WHERE deleted=0 AND enabled=1")
	if err != nil {
		return false, err
	}
	type sched struct {
		in  model.PowerScheduleInput
		act time.Time
	}
	list := []sched{}
	for rows.Next() {
		var raw, actStr string
		if err = rows.Scan(&raw, &actStr); err != nil {
			rows.Close()
			return false, err
		}
		var in model.PowerScheduleInput
		act, _ := time.Parse(time.RFC3339Nano, actStr)
		if json.Unmarshal([]byte(raw), &in) == nil && !in.DryRun && slices.Contains(in.Targets, instanceID) {
			list = append(list, sched{in: in, act: act})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(list) == 0 {
		return false, err
	}
	localNow := now.In(loc)
	var latestStop, latestStart time.Time
	for _, sc := range list {
		h, m := parseCivilHM(sc.in.TimeOfDay)
		for dayOffset := -1; dayOffset <= 0; dayOffset++ {
			d := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), h, m, 0, 0, loc).AddDate(0, 0, dayOffset)
			if d.After(localNow) || d.UTC().Before(sc.act) || !slices.Contains(sc.in.DaysOfWeek, int(d.Weekday())) {
				continue
			}
			if sc.in.Action == "stop" && d.After(latestStop) {
				latestStop = d
			}
			if sc.in.Action == "start" && d.After(latestStart) {
				latestStart = d
			}
		}
	}
	if latestStop.IsZero() || !latestStop.After(latestStart) {
		return false, nil
	}
	var intentStr string
	if qErr := tx.QueryRowContext(ctx, "SELECT updated_at FROM instance_intent WHERE instance_id=?", instanceID).Scan(&intentStr); qErr == nil {
		if t, pErr := time.Parse(time.RFC3339Nano, intentStr); pErr == nil && t.After(latestStop.UTC().Add(2*time.Minute)) {
			// User manually changed intent after the scheduled stop fired
			var curDesired string
			_ = tx.QueryRowContext(ctx, "SELECT desired_state FROM instance WHERE id=?", instanceID).Scan(&curDesired)
			if curDesired == "running" {
				return false, nil
			}
		}
	}
	return true, nil
}

func automaticTargetReadyTx(ctx context.Context, tx *sql.Tx, id string, action, stopMode string, confirmedSelfID string, policy model.ProtectionPolicy, now time.Time) (string, string, string) {
	if !policy.SelfKnown && action == "stop" {
		return "", "", "self_identity_unknown"
	}
	var quarantine string
	if tx.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='protection_restore_until'").Scan(&quarantine) == nil {
		if until, e := time.Parse(time.RFC3339Nano, quarantine); e == nil && until.After(now) {
			return "", "", "restore_quarantine"
		}
	}
	if action == "stop" && policy.SelfInstanceID == id && confirmedSelfID != id {
		return "", "", "self_excluded"
	}
	var accountID, status, region, kind, raw string
	err := tx.QueryRowContext(ctx, `SELECT i.account_id,i.status,i.region,i.kind,r.payload_json
 FROM instance i JOIN resource_snapshot r ON r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1
 WHERE i.id=?`, id).Scan(&accountID, &status, &region, &kind, &raw)
	if err != nil {
		return "", "", "target_unavailable"
	}
	a, err := loadAccount(ctx, tx, accountID)
	if err != nil || !a.Enabled || !slices.Contains(a.RegionIDs, region) {
		return accountID, kind, "target_unavailable"
	}
	if action == "stop" {
		if status == "stopped" {
			return accountID, kind, "already_stopped"
		}
		if status != "running" {
			return accountID, kind, "target_unavailable"
		}
		effMode := stopMode
		if kind == "swas" {
			effMode = ""
		}
		if !model.ValidPowerAction(kind, "stop", effMode) {
			return accountID, kind, "target_unavailable"
		}
		if kind == "ecs" && effMode == "stop_charging" {
			var facts struct {
				Charge string `json:"charge_type"`
			}
			if json.Unmarshal([]byte(raw), &facts) != nil || (facts.Charge != "pay_as_you_go" && facts.Charge != "spot") {
				return accountID, kind, "target_unavailable"
			}
		}
	} else if action == "start" {
		if status == "running" {
			return accountID, kind, "already_running"
		}
		if status != "stopped" {
			return accountID, kind, "target_unavailable"
		}
	}
	var count int
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM automatic_power_action WHERE instance_id=? AND julianday(dispatch_started_at)>julianday(?)", id, timestamp(now.Add(-10*time.Minute))).Scan(&count) == nil && count >= 3 {
		return accountID, kind, "operation_limited"
	}
	return accountID, kind, ""
}

func (s *Store) EvaluateAutomation(ctx context.Context, policy model.ProtectionPolicy, now time.Time) error {
	loc, err := s.BusinessLocation(ctx, policy.Timezone)
	if err != nil {
		loc = time.UTC
	}
	if err := s.evaluatePowerSchedules(ctx, policy, loc, now); err != nil {
		return err
	}
	if err := s.evaluateMonthlyReset(ctx, policy, loc, now); err != nil {
		return err
	}
	if err := s.evaluateKeepalive(ctx, policy, loc, now); err != nil {
		return err
	}
	return s.finishAutomationNotifications(ctx, now)
}

func (s *Store) evaluatePowerSchedules(ctx context.Context, policy model.ProtectionPolicy, loc *time.Location, now time.Time) error {
	schedules, err := s.PowerSchedules(ctx)
	if err != nil {
		return err
	}
	localNow := now.In(loc)
	for _, sched := range schedules {
		if !sched.Enabled {
			continue
		}
		h, m := parseCivilHM(sched.TimeOfDay)
		for dayOffset := -1; dayOffset <= 0; dayOffset++ {
			dueLocal := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), h, m, 0, 0, loc).AddDate(0, 0, dayOffset)
			dueUTC := dueLocal.UTC()
			if dueUTC.After(now) || dueUTC.Before(sched.ActivatedAt) || !slices.Contains(sched.DaysOfWeek, int(dueLocal.Weekday())) {
				continue
			}
			deadline := dueUTC.Add(time.Duration(sched.CatchUpMinutes) * time.Minute)
			if now.After(deadline) {
				continue
			}
			winKey := dueLocal.Format("2006-01-02") + "@" + sched.TimeOfDay + ":r" + strconv.FormatInt(sched.Revision, 10)
			if err := s.firePowerScheduleWindow(ctx, sched, winKey, policy, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) firePowerScheduleWindow(ctx context.Context, sched model.PowerSchedule, winKey string, policy model.ProtectionPolicy, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM power_schedule_run WHERE schedule_id=? AND window_key=?", sched.ID, winKey).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	cur, err := readPowerScheduleTx(ctx, tx, sched.ID)
	if err != nil || !cur.Enabled || cur.Revision != sched.Revision {
		return nil
	}
	queued := 0
	wouldAct := 0
	for _, instID := range cur.Targets {
		if cur.Action == "start" {
			var desired string
			_ = tx.QueryRowContext(ctx, "SELECT desired_state FROM instance WHERE id=?", instID).Scan(&desired)
			blocked, bErr := instanceBlockedByQuotaRuleTx(ctx, tx, instID)
			if bErr != nil {
				return bErr
			}
			if desired == "quota_stopped" || blocked {
				continue
			}
		}
		_, kind, reason := automaticTargetReadyTx(ctx, tx, instID, cur.Action, cur.StopMode, cur.ConfirmedSelfID, policy, now)
		if reason != "" {
			continue
		}
		if cur.DryRun || !policy.PowerEnabled {
			wouldAct++
			continue
		}
		effMode := cur.StopMode
		if kind == "swas" {
			effMode = ""
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("sched|%s|%s|%s", cur.ID, winKey, instID)))
		j, qErr := enqueuePowerJobTx(model.WithJobTrigger(ctx, "schedule"), tx, instID, cur.Action, effMode, hex.EncodeToString(sum[:]), "schedule:"+cur.ID, now)
		if errors.Is(qErr, ErrPowerBusy) {
			continue
		}
		if qErr != nil {
			return qErr
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO automation_power_job(job_id,kind,rule_id,rule_revision,window_key) VALUES(?,?,?,?,?)", j.ID, "schedule", cur.ID, cur.Revision, winKey); err != nil {
			return err
		}
		queued++
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO power_schedule_run(schedule_id,window_key,triggered_at) VALUES(?,?,?)", cur.ID, winKey, timestamp(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE power_schedule SET last_triggered_at=? WHERE id=?", timestamp(now), cur.ID); err != nil {
		return err
	}
	if cur.DryRun || (!policy.PowerEnabled && wouldAct > 0) {
		actWord := "开机"
		if cur.Action == "stop" {
			actWord = "关机"
		}
		acc := cur.AccountID
		if _, err = enqueueNotification(ctx, tx, model.Notification{
			EventKey:  fmt.Sprintf("sched:%s:%s:rehearsal", cur.ID, winKey),
			Kind:      "alert",
			Severity:  "info",
			Title:     "🟡 定时规则试运行命中：" + cur.Name,
			Body:      fmt.Sprintf("计划时间 %s 本应对 %d 台机器执行%s（试运行/电源未开启，未实际执行）。", cur.TimeOfDay, wouldAct, actWord),
			AccountID: &acc,
		}, now); err != nil {
			return err
		}
	}
	_ = queued
	return tx.Commit()
}

func (s *Store) evaluateMonthlyReset(ctx context.Context, policy model.ProtectionPolicy, loc *time.Location, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cfg, err := readMonthlyResetTx(ctx, tx)
	if err != nil || !cfg.Enabled || len(cfg.Targets) == 0 {
		return err
	}
	cycleStart, _ := ReferenceCycle(now, loc)
	// Trigger once per new month within the first 24h of the 1st of the month.
	if now.Sub(cycleStart) > 24*time.Hour {
		return nil
	}
	if cfg.LastCycleStart != nil && cfg.LastCycleStart.Equal(cycleStart) {
		return nil
	}
	winKey := timestamp(cycleStart)
	rehearsalCount := 0
	for _, instID := range cfg.Targets {
		var desired, accountID string
		if qErr := tx.QueryRowContext(ctx, "SELECT desired_state,account_id FROM instance WHERE id=?", instID).Scan(&desired, &accountID); qErr != nil || desired != "quota_stopped" {
			continue
		}
		blocked, bErr := instanceBlockedByQuotaRuleTx(ctx, tx, instID)
		if bErr != nil {
			return bErr
		}
		inStopWin, sErr := instanceInScheduledStopWindowTx(ctx, tx, instID, loc, now)
		if sErr != nil {
			return sErr
		}
		if blocked || inStopWin {
			continue
		}
		_, _, reason := automaticTargetReadyTx(ctx, tx, instID, "start", "", "", policy, now)
		if reason != "" {
			continue
		}
		if cfg.DryRun || !policy.PowerEnabled {
			rehearsalCount++
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("monthly_reset|%s|%s", winKey, instID)))
		j, qErr := enqueuePowerJobTx(model.WithJobTrigger(ctx, "schedule"), tx, instID, "start", "", hex.EncodeToString(sum[:]), "monthly_reset:1", now)
		if errors.Is(qErr, ErrPowerBusy) {
			continue
		}
		if qErr != nil {
			return qErr
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO automation_power_job(job_id,kind,rule_id,rule_revision,window_key) VALUES(?,?,?,?,?)", j.ID, "monthly_reset", "1", cfg.Revision, winKey); err != nil {
			return err
		}
		_ = accountID
	}
	if _, err = tx.ExecContext(ctx, "UPDATE monthly_reset_settings SET last_cycle_start=? WHERE id=1", winKey); err != nil {
		return err
	}
	if (cfg.DryRun || !policy.PowerEnabled) && rehearsalCount > 0 {
		if _, err = enqueueNotification(ctx, tx, model.Notification{
			EventKey: "monthly_reset:" + winKey + ":rehearsal",
			Kind:     "alert",
			Severity: "info",
			Title:    "🟡 月初自动开机试运行命中",
			Body:     fmt.Sprintf("新计费周期开始，本应将 %d 台因额度停机的机器自动开机（试运行，未实际执行）。", rehearsalCount),
		}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) evaluateKeepalive(ctx context.Context, policy model.ProtectionPolicy, loc *time.Location, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cfg, err := readKeepaliveTx(ctx, tx)
	if err != nil || !cfg.Enabled || len(cfg.Targets) == 0 {
		return err
	}
	for _, instID := range cfg.Targets {
		var desired, accountID string
		if qErr := tx.QueryRowContext(ctx, "SELECT i.desired_state,i.account_id FROM instance i JOIN instance_intent d ON d.instance_id=i.id WHERE i.id=?", instID).Scan(&desired, &accountID); qErr != nil || desired != "running" {
			continue
		}
		var pendingConfirm int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance_intent_confirmation WHERE instance_id=? AND status='pending'", instID).Scan(&pendingConfirm); err != nil {
			return err
		}
		if pendingConfirm > 0 {
			continue
		}
		blocked, bErr := instanceBlockedByQuotaRuleTx(ctx, tx, instID)
		if bErr != nil {
			return bErr
		}
		inStopWin, sErr := instanceInScheduledStopWindowTx(ctx, tx, instID, loc, now)
		if sErr != nil {
			return sErr
		}
		if blocked || inStopWin {
			continue
		}
		_, _, reason := automaticTargetReadyTx(ctx, tx, instID, "start", "", "", policy, now)
		if reason != "" {
			continue
		}
		// Cooldown: at most 1 keepalive attempt per minute bucket per instance
		bucketKey := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
		if cfg.DryRun || !policy.PowerEnabled {
			name := instanceCopyName(ctx, tx, instID)
			if _, err = enqueueNotification(ctx, tx, model.Notification{
				EventKey:   fmt.Sprintf("keepalive:%s:%s:rehearsal", instID, bucketKey),
				Kind:       "alert",
				Severity:   "warning",
				Title:      "🟡 保活试运行命中：" + name,
				Body:       "检测到期望状态为“运行”但机器已停止，本应自动拉起（试运行，未实际开机）。",
				AccountID:  &accountID,
				InstanceID: &instID,
			}, now); err != nil {
				return err
			}
			continue
		}
		var recentJob int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM automation_power_job a JOIN job j ON j.id=a.job_id
 WHERE a.kind='keepalive' AND j.instance_id=? AND (j.status IN ('queued','running') OR julianday(j.created_at)>julianday(?))`,
			instID, timestamp(now.Add(-time.Minute))).Scan(&recentJob); err != nil {
			return err
		}
		if recentJob > 0 {
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("keepalive|%s|%s", instID, bucketKey)))
		j, qErr := enqueuePowerJobTx(model.WithJobTrigger(ctx, "schedule"), tx, instID, "start", "", hex.EncodeToString(sum[:]), "keepalive:1", now)
		if errors.Is(qErr, ErrPowerBusy) {
			continue
		}
		if qErr != nil {
			return qErr
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO automation_power_job(job_id,kind,rule_id,rule_revision,window_key) VALUES(?,?,?,?,?)", j.ID, "keepalive", "1", cfg.Revision, bucketKey); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func authorizeAutomationDispatch(ctx context.Context, tx *sql.Tx, p PowerOperation, policy model.ProtectionPolicy, now time.Time) error {
	var kind, ruleID string
	var ruleRev int64
	err := tx.QueryRowContext(ctx, "SELECT kind,rule_id,rule_revision FROM automation_power_job WHERE job_id=?", p.Job.ID).Scan(&kind, &ruleID, &ruleRev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !policy.PowerEnabled || (!policy.SelfKnown && p.Action == "stop") {
		return ErrProtectionGuard
	}
	loc := copyLocation(ctx, tx)
	switch kind {
	case "schedule":
		cur, sErr := readPowerScheduleTx(ctx, tx, ruleID)
		if sErr != nil || !cur.Enabled || cur.DryRun || cur.Revision != ruleRev {
			return ErrProtectionGuard
		}
		if _, _, reason := automaticTargetReadyTx(ctx, tx, p.InstanceID, p.Action, p.StopMode, cur.ConfirmedSelfID, policy, now); reason != "" {
			return ErrProtectionGuard
		}
		if p.Action == "start" {
			var desired string
			_ = tx.QueryRowContext(ctx, "SELECT desired_state FROM instance WHERE id=?", p.InstanceID).Scan(&desired)
			blocked, _ := instanceBlockedByQuotaRuleTx(ctx, tx, p.InstanceID)
			if desired == "quota_stopped" || blocked {
				return ErrProtectionGuard
			}
		}
	case "keepalive":
		cfg, kErr := readKeepaliveTx(ctx, tx)
		if kErr != nil || !cfg.Enabled || cfg.DryRun || cfg.Revision != ruleRev {
			return ErrProtectionGuard
		}
		var desired string
		var pendingConfirm int
		_ = tx.QueryRowContext(ctx, "SELECT desired_state FROM instance WHERE id=?", p.InstanceID).Scan(&desired)
		_ = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance_intent_confirmation WHERE instance_id=? AND status='pending'", p.InstanceID).Scan(&pendingConfirm)
		blocked, _ := instanceBlockedByQuotaRuleTx(ctx, tx, p.InstanceID)
		inStopWin, _ := instanceInScheduledStopWindowTx(ctx, tx, p.InstanceID, loc, now)
		if desired != "running" || pendingConfirm > 0 || blocked || inStopWin {
			return ErrProtectionGuard
		}
		if _, _, reason := automaticTargetReadyTx(ctx, tx, p.InstanceID, "start", "", "", policy, now); reason != "" {
			return ErrProtectionGuard
		}
	case "monthly_reset":
		cfg, mErr := readMonthlyResetTx(ctx, tx)
		if mErr != nil || !cfg.Enabled || cfg.DryRun || cfg.Revision != ruleRev {
			return ErrProtectionGuard
		}
		var desired string
		_ = tx.QueryRowContext(ctx, "SELECT desired_state FROM instance WHERE id=?", p.InstanceID).Scan(&desired)
		blocked, _ := instanceBlockedByQuotaRuleTx(ctx, tx, p.InstanceID)
		inStopWin, _ := instanceInScheduledStopWindowTx(ctx, tx, p.InstanceID, loc, now)
		if desired != "quota_stopped" || blocked || inStopWin {
			return ErrProtectionGuard
		}
		if _, _, reason := automaticTargetReadyTx(ctx, tx, p.InstanceID, "start", "", "", policy, now); reason != "" {
			return ErrProtectionGuard
		}
	default:
		return ErrProtectionGuard
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO automatic_power_action(job_id,instance_id,dispatch_started_at) VALUES(?,?,?)", p.Job.ID, p.InstanceID, timestamp(now)); err != nil {
		return err
	}
	nextDesired := "running"
	if p.Action == "stop" {
		nextDesired = "stopped"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance SET desired_state=? WHERE id=?", nextDesired, p.InstanceID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO instance_intent(instance_id,job_id,updated_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET job_id=excluded.job_id,updated_at=excluded.updated_at", p.InstanceID, p.Job.ID, timestamp(now))
	return err
}

func (s *Store) finishAutomationNotifications(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.job_id,a.kind,p.action,j.status,i.account_id,i.id
 FROM automation_power_job a
 JOIN job j ON j.id=a.job_id
 JOIN instance_power_job p ON p.job_id=a.job_id
 JOIN instance i ON i.id=j.instance_id
 WHERE a.final_notified=0 AND j.status IN ('succeeded','failed')`)
	if err != nil {
		return err
	}
	type item struct{ job, kind, action, status, account, instance string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.job, &v.kind, &v.action, &v.status, &v.account, &v.instance); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		name := instanceCopyName(ctx, tx, v.instance)
		sev := "info"
		var title string
		if v.status == "succeeded" {
			switch v.kind {
			case "keepalive":
				title = "🟢 保活已把 " + name + " 重新开机"
			case "monthly_reset":
				title = "🟢 月初重置已把 " + name + " 自动开机"
			default:
				actWord := "开机"
				if v.action == "stop" {
					actWord = "关机"
				}
				title = "🟢 定时任务已把 " + name + " " + actWord
			}
		} else {
			sev = "serious"
			title = "🟠 自动化任务未能完成对 " + name + " 的电源操作"
		}
		if _, err = enqueueNotification(ctx, tx, model.Notification{
			EventKey:   "auto-job:" + v.job + ":finished",
			JobID:      v.job,
			Kind:       "alert",
			Severity:   sev,
			Title:      title,
			Body:       "",
			AccountID:  &v.account,
			InstanceID: &v.instance,
		}, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE automation_power_job SET final_notified=1 WHERE job_id=?", v.job); err != nil {
			return err
		}
	}
	return tx.Commit()
}

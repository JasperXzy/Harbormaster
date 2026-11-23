package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var (
	ErrEmailNotConfigured   = errors.New("email_not_configured")
	ErrWebhookNotConfigured = errors.New("webhook_not_configured")
)

func readExtraChannelRow(ctx context.Context, q queryRower, table string, notFoundErr error) ([]byte, model.ExtraChannelState, error) {
	var state model.ExtraChannelState
	var cipher []byte
	var updated string
	var lastSuccess, code sql.NullString
	var enabled int
	err := q.QueryRowContext(ctx, "SELECT enabled,revision,ciphertext,updated_at,last_success_at,last_error_code FROM "+table+" WHERE id=1").Scan(&enabled, &state.Revision, &cipher, &updated, &lastSuccess, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state, notFoundErr
	}
	if err != nil {
		return nil, state, err
	}
	state.Enabled = enabled == 1
	state.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	state.LastSuccessAt = parseAgentTime(lastSuccess)
	if code.Valid && code.String != "" {
		state.LastError = model.Error(code.String)
	}
	return cipher, state, nil
}

func (s *Store) EmailChannel(ctx context.Context) ([]byte, model.ExtraChannelState, error) {
	return readExtraChannelRow(ctx, s.DB, "email_channel", ErrEmailNotConfigured)
}

func (s *Store) WebhookChannel(ctx context.Context) ([]byte, model.ExtraChannelState, error) {
	return readExtraChannelRow(ctx, s.DB, "webhook_channel", ErrWebhookNotConfigured)
}

func saveExtraChannel(ctx context.Context, s *Store, table, channelName, auditAction string, enabled bool, sealed []byte, expectedRevision int64, details model.AuditDetails, now time.Time) (model.ExtraChannelState, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.ExtraChannelState{}, err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM "+table+" WHERE id=1").Scan(&rev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.ExtraChannelState{}, err
	}
	if rev != expectedRevision {
		return model.ExtraChannelState{}, ErrChannelChanged
	}
	nextRev := rev + 1
	enInt := 0
	if enabled {
		enInt = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+table+`(id,enabled,revision,ciphertext,updated_at) VALUES(1,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,revision=excluded.revision,ciphertext=excluded.ciphertext,updated_at=excluded.updated_at,last_error_code=NULL`,
		enInt, nextRev, sealed, timestamp(now)); err != nil {
		return model.ExtraChannelState{}, err
	}
	chgCode := channelName + "_configuration_changed"
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',finished_at=?,error_code=?,lease_owner=NULL,lease_until=0
 WHERE status IN ('queued','running') AND id IN (SELECT job_id FROM notification_channel_delivery WHERE channel=? AND delivery_status='pending' AND job_id IS NOT NULL)`,
		timestamp(now), chgCode, channelName); err != nil {
		return model.ExtraChannelState{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_channel_delivery SET delivery_status='failed',last_error_code=?,lease_owner=NULL,lease_until=0
 WHERE channel=? AND delivery_status='pending'`, chgCode, channelName); err != nil {
		return model.ExtraChannelState{}, err
	}
	details.Revision = &nextRev
	if err = appendAudit(ctx, tx, auditEvent{Action: auditAction, TargetType: "notification_channel", TargetID: channelName, Details: details}, now); err != nil {
		return model.ExtraChannelState{}, err
	}
	_, state, err := readExtraChannelRow(ctx, tx, table, ErrNotFound)
	if err != nil {
		return state, err
	}
	return state, tx.Commit()
}

func (s *Store) SaveEmailChannel(ctx context.Context, enabled bool, sealed []byte, expectedRevision int64, details model.AuditDetails, now time.Time) (model.ExtraChannelState, error) {
	return saveExtraChannel(ctx, s, "email_channel", "email", "email_channel_updated", enabled, sealed, expectedRevision, details, now)
}

func (s *Store) SaveWebhookChannel(ctx context.Context, enabled bool, sealed []byte, expectedRevision int64, details model.AuditDetails, now time.Time) (model.ExtraChannelState, error) {
	return saveExtraChannel(ctx, s, "webhook_channel", "webhook", "webhook_channel_updated", enabled, sealed, expectedRevision, details, now)
}

func enqueueExtraChannelDeliveriesTx(ctx context.Context, tx *sql.Tx, notifID string, in model.Notification, suppressed bool, now time.Time) error {
	if suppressed || strings.HasPrefix(in.EventKey, "telegram-test:") || strings.HasPrefix(in.EventKey, "email-test:") || strings.HasPrefix(in.EventKey, "webhook-test:") {
		return nil
	}
	for _, item := range []struct{ table, channel string }{{"email_channel", "email"}, {"webhook_channel", "webhook"}} {
		var enabled int
		var rev int64
		err := tx.QueryRowContext(ctx, "SELECT enabled,revision FROM "+item.table+" WHERE id=1").Scan(&enabled, &rev)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if enabled != 1 || rev <= 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO notification_channel_delivery(notification_id,channel,channel_revision,delivery_status,available_at)
 VALUES(?,?,?,'pending',?) ON CONFLICT(notification_id,channel) DO NOTHING`, notifID, item.channel, rev, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

func enqueueExtraChannelTest(ctx context.Context, s *Store, table, channelName, jobKind string, notConfErr error, title, body string, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM "+table+" WHERE id=1").Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, notConfErr
	}
	if err != nil {
		return model.Job{}, err
	}
	j, err := enqueueJob(ctx, tx, jobKind, channelName, []string{}, now)
	if err != nil {
		return j, err
	}
	notifID := model.NewID("notice_")
	evKey := channelName + "-test:" + j.ID
	if _, err = tx.ExecContext(ctx, `INSERT INTO notification(id,event_key,job_id,created_at,kind,severity,title,body,delivery_status,channel_revision,available_at,urgent)
 VALUES(?,?,?,?,'test','info',?,?,'not_sent',0,?,1) ON CONFLICT(event_key) DO NOTHING`,
		notifID, evKey, j.ID, timestamp(now), title, body, now.Unix()); err != nil {
		return j, err
	}
	var actualID string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM notification WHERE event_key=?", evKey).Scan(&actualID); err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO notification_channel_delivery(notification_id,channel,channel_revision,job_id,delivery_status,available_at)
 VALUES(?,?,?,?,'pending',?) ON CONFLICT(notification_id,channel) DO NOTHING`,
		actualID, channelName, rev, j.ID, now.Unix()); err != nil {
		return j, err
	}
	return j, tx.Commit()
}

func (s *Store) EnqueueEmailTest(ctx context.Context, title, body string, now time.Time) (model.Job, error) {
	return enqueueExtraChannelTest(ctx, s, "email_channel", "email", "email_test", ErrEmailNotConfigured, title, body, now)
}

func (s *Store) EnqueueWebhookTest(ctx context.Context, title, body string, now time.Time) (model.Job, error) {
	return enqueueExtraChannelTest(ctx, s, "webhook_channel", "webhook", "webhook_test", ErrWebhookNotConfigured, title, body, now)
}

func (s *Store) ClaimExtraChannelDelivery(ctx context.Context, now time.Time, maxAttempts int) (model.ExtraChannelDelivery, error) {
	var out model.ExtraChannelDelivery
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	// Expire exhausted deliveries
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',finished_at=?,error_code='retry_exhausted',lease_owner=NULL,lease_until=0
 WHERE status IN ('queued','running') AND id IN (SELECT job_id FROM notification_channel_delivery WHERE delivery_status='pending' AND attempts>=? AND (lease_owner IS NULL OR lease_until<=?) AND job_id IS NOT NULL)`,
		timestamp(now), maxAttempts, now.Unix()); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_channel_delivery SET delivery_status='failed',last_error_code='retry_exhausted',lease_owner=NULL,lease_until=0
 WHERE delivery_status='pending' AND attempts>=? AND (lease_owner IS NULL OR lease_until<=?)`,
		maxAttempts, now.Unix()); err != nil {
		return out, err
	}

	var notifID, channel string
	var rev int64
	var jobID sql.NullString
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT notification_id,channel,channel_revision,job_id,attempts
 FROM notification_channel_delivery
 WHERE delivery_status='pending' AND available_at<=? AND (lease_owner IS NULL OR lease_until<=?)
 ORDER BY rowid LIMIT 1`, now.Unix(), now.Unix()).Scan(&notifID, &channel, &rev, &jobID, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Commit()
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	n, err := readNotification(tx.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE id=?", notifID))
	if err != nil {
		return out, err
	}
	if !n.AlwaysUrgent() {
		quiet, qErr := readQuietHours(ctx, tx, now)
		if qErr != nil {
			return out, qErr
		}
		if quiet.Active {
			if _, err = tx.ExecContext(ctx, `UPDATE notification_channel_delivery SET delivery_status='not_sent',last_error_code='quiet_hours',lease_owner=NULL,lease_until=0 WHERE notification_id=? AND channel=?`, notifID, channel); err != nil {
				return out, err
			}
			_ = tx.Commit()
			return out, ErrNotFound
		}
	}

	owner := model.NewID("ch_lease_")
	until := now.Add(time.Minute).Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE notification_channel_delivery SET lease_owner=?,lease_until=?,attempts=attempts+1 WHERE notification_id=? AND channel=?`,
		owner, until, notifID, channel); err != nil {
		return out, err
	}
	if jobID.Valid && jobID.String != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE job SET status='running',started_at=coalesce(started_at,?),attempts=attempts+1,lease_owner=?,lease_until=? WHERE id=? AND status IN ('queued','running')`,
			timestamp(now), owner, until, jobID.String); err != nil {
			return out, err
		}
		out.JobID = jobID.String
	}
	out.NotificationRow = n
	out.Channel = channel
	out.ChannelRevision = rev
	out.Attempts = attempts + 1
	out.Owner = owner
	return out, tx.Commit()
}

func (s *Store) FinishExtraChannelDelivery(ctx context.Context, d model.ExtraChannelDelivery, code string, retryAt *time.Time, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	table := "email_channel"
	if d.Channel == "webhook" {
		table = "webhook_channel"
	}
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_channel_delivery ncd JOIN `+table+` c ON c.id=1 AND c.revision=ncd.channel_revision
 WHERE ncd.notification_id=? AND ncd.channel=? AND ncd.delivery_status='pending' AND ncd.lease_owner=? AND ncd.lease_until>?`,
		d.NotificationRow.ID, d.Channel, d.Owner, now.Unix()).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return ErrLeaseLost
	}

	status, jobStatus := "sent", "succeeded"
	var errVal, delivered any
	avail := now.Unix()
	if code != "" {
		status = "failed"
		jobStatus = "failed"
		errVal = code
	} else {
		delivered = timestamp(now)
	}
	if retryAt != nil {
		status = "pending"
		jobStatus = "queued"
		avail = retryAt.Unix()
		if retryAt.Nanosecond() > 0 {
			avail++
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_channel_delivery SET delivery_status=?,available_at=?,lease_owner=NULL,lease_until=0,last_error_code=?,delivered_at=?
 WHERE notification_id=? AND channel=?`, status, avail, errVal, delivered, d.NotificationRow.ID, d.Channel); err != nil {
		return err
	}
	if d.JobID != "" {
		var finished any
		if jobStatus != "queued" {
			finished = timestamp(now)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE job SET status=?,finished_at=?,error_code=?,lease_owner=NULL,lease_until=0,available_at=? WHERE id=? AND lease_owner=?`,
			jobStatus, finished, errVal, avail, d.JobID, d.Owner); err != nil {
			return err
		}
	}
	if code == "" {
		if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET last_success_at=?,last_error_code=NULL WHERE id=1 AND revision=?", timestamp(now), d.ChannelRevision); err != nil {
			return err
		}
		// Reflect delivery on parent notification when Telegram wasn't configured
		if _, err = tx.ExecContext(ctx, `UPDATE notification SET delivery_status='sent',delivered_at=coalesce(delivered_at,?) WHERE id=? AND delivery_status='not_sent' AND suppression_reason IS NULL`,
			timestamp(now), d.NotificationRow.ID); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET last_error_code=? WHERE id=1 AND revision=?", code, d.ChannelRevision); err != nil {
			return err
		}
		if d.JobID != "" && status == "failed" {
			if _, err = tx.ExecContext(ctx, `UPDATE notification SET delivery_status='failed',last_error_code=? WHERE id=? AND delivery_status='not_sent'`,
				code, d.NotificationRow.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func validHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	for i, c := range s {
		if i != 2 && (c < '0' || c > '9') {
			return false
		}
	}
	hh := int(s[0]-'0')*10 + int(s[1]-'0')
	mm := int(s[3]-'0')*10 + int(s[4]-'0')
	return hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59
}

func readDailyReportSettingsTx(ctx context.Context, q queryRower) (model.DailyReportSettings, error) {
	out := model.DefaultDailyReportSettings()
	var en int
	var lastSent, updated sql.NullString
	err := q.QueryRowContext(ctx, "SELECT enabled,time_of_day,revision,last_sent_date,updated_at FROM daily_report_settings WHERE id=1").Scan(&en, &out.TimeOfDay, &out.Revision, &lastSent, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enabled = en == 1
	if lastSent.Valid && lastSent.String != "" {
		s := lastSent.String
		out.LastSentDate = &s
	}
	out.UpdatedAt = parseAgentTime(updated)
	return out, nil
}

func (s *Store) DailyReportSettings(ctx context.Context) (model.DailyReportSettings, error) {
	return readDailyReportSettingsTx(ctx, s.DB)
}

func (s *Store) SaveDailyReportSettings(ctx context.Context, in model.DailyReportSettings, now time.Time) (model.DailyReportSettings, error) {
	if !validHHMM(in.TimeOfDay) {
		return model.DailyReportSettings{}, errors.New("invalid_time_of_day")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.DailyReportSettings{}, err
	}
	defer tx.Rollback()
	prev, err := readDailyReportSettingsTx(ctx, tx)
	if err != nil {
		return prev, err
	}
	if prev.Revision != in.Revision {
		return prev, ErrChannelChanged
	}
	nextRev := prev.Revision + 1
	loc := copyLocation(ctx, tx)
	localNow := now.In(loc)
	todayStr := localNow.Format("2006-01-02")
	lastSent := prev.LastSentDate
	// When newly enabling after today's scheduled time has already passed, start from tomorrow
	// rather than surprising the admin with an immediate catch-up digest (manual send is available).
	if !prev.Enabled && in.Enabled && localNow.Format("15:04") >= in.TimeOfDay {
		lastSent = &todayStr
	}
	enInt := 0
	if in.Enabled {
		enInt = 1
	}
	var lastVal any
	if lastSent != nil {
		lastVal = *lastSent
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO daily_report_settings(id,enabled,time_of_day,revision,last_sent_date,updated_at)
 VALUES(1,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,time_of_day=excluded.time_of_day,revision=excluded.revision,last_sent_date=excluded.last_sent_date,updated_at=excluded.updated_at`,
		enInt, in.TimeOfDay, nextRev, lastVal, timestamp(now)); err != nil {
		return prev, err
	}
	details := auditDiff(auditChange{"enabled", prev.Enabled, in.Enabled}, auditChange{"time_of_day", prev.TimeOfDay, in.TimeOfDay})
	details.Revision = &nextRev
	if err = appendAudit(ctx, tx, auditEvent{Action: "daily_report_updated", TargetType: "daily_report", TargetID: "default", Details: details}, now); err != nil {
		return prev, err
	}
	out, err := readDailyReportSettingsTx(ctx, tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func buildDailyReportCopyTx(ctx context.Context, tx *sql.Tx, accounts []model.Account, loc *time.Location, now time.Time) (string, string, error) {
	localNow := now.In(loc)
	start, end := ReferenceCycle(now, loc)
	title := "⚪ 每日流量与实例简报（" + localNow.Format("01-02") + "）"

	var running, stopped int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(CASE WHEN rs.present=1 AND json_extract(rs.payload_json,'$.status')='running' THEN 1 ELSE 0 END),0),
 coalesce(sum(CASE WHEN rs.present=1 AND json_extract(rs.payload_json,'$.status')='stopped' THEN 1 ELSE 0 END),0)
 FROM resource_snapshot rs JOIN account_settings a ON a.account_id=rs.account_id AND a.enabled=1
 WHERE rs.kind IN ('ecs','swas')`).Scan(&running, &stopped); err != nil {
		return "", "", err
	}

	lines := []string{fmt.Sprintf("实例概览：运行中 %d 台 · 已停止 %d 台", running, stopped)}
	settings, err := readEstimationSettings(ctx, tx)
	if err != nil {
		return "", "", err
	}
	for _, a := range accounts {
		for _, scope := range a.TrackedScopes() {
			quota := a.QuotaMainland
			col := "mainland_bytes"
			scName := "国内"
			if scope == "overseas" {
				quota = a.QuotaOverseas
				col = "overseas_bytes"
				scName = "海外"
			}
			var usedStr = "待读取"
			good, gErr := readObservation(tx.QueryRowContext(ctx, "SELECT "+observationColumns+" FROM cdt_observation WHERE account_id=? AND credential_version=? AND fetched_at>=? AND fetched_at<? AND error_code IS NULL AND "+col+" IS NOT NULL ORDER BY id DESC LIMIT 1",
				a.ID, a.CredentialVersion, timestamp(start), timestamp(end)))
			if gErr == nil {
				val := good.Mainland
				if scope == "overseas" {
					val = good.Overseas
				}
				if val != nil {
					used := *val
					var rawEst string
					if tx.QueryRowContext(ctx, "SELECT payload_json FROM traffic_estimate WHERE account_id=? AND scope=? AND credential_version=? AND account_revision=? AND settings_revision=? AND basis_id=?",
						a.ID, scope, a.CredentialVersion, a.Revision, settings.Revision, good.ID).Scan(&rawEst) == nil {
						var parsed model.TrafficEstimation
						if json.Unmarshal([]byte(rawEst), &parsed) == nil && parsed.Status == "ready" && parsed.Estimated != nil && *parsed.Estimated > used {
							used = *parsed.Estimated
						}
					}
					usedStr = copyGB(used)
				}
			}
			lines = append(lines, fmt.Sprintf("• %s %s：%s / %s", a.Name, scName, usedStr, copyGB(quota)))
		}
	}
	if len(accounts) == 0 {
		lines = append(lines, "当前暂无启用的阿里云账号。")
	}
	return title, strings.Join(lines, "\n"), nil
}

func evaluateDailyReportTx(ctx context.Context, tx *sql.Tx, accounts []model.Account, now time.Time) error {
	dr, err := readDailyReportSettingsTx(ctx, tx)
	if err != nil || !dr.Enabled || !validHHMM(dr.TimeOfDay) {
		return err
	}
	loc := copyLocation(ctx, tx)
	localNow := now.In(loc)
	todayStr := localNow.Format("2006-01-02")
	if dr.LastSentDate != nil && *dr.LastSentDate >= todayStr {
		return nil
	}
	if localNow.Format("15:04") < dr.TimeOfDay {
		return nil
	}
	title, body, err := buildDailyReportCopyTx(ctx, tx, accounts, loc, now)
	if err != nil {
		return err
	}
	if _, err = enqueueNotification(ctx, tx, model.Notification{
		EventKey: "daily_report:" + todayStr,
		Kind:     "alert",
		Severity: "info",
		Title:    title,
		Body:     body,
		Urgent:   false,
	}, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE daily_report_settings SET last_sent_date=?,updated_at=? WHERE id=1", todayStr, timestamp(now))
	return err
}

func (s *Store) TriggerDailyReportNow(ctx context.Context, now time.Time) (model.Notification, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Notification{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT account_id,credential_version,revision,config_json FROM account_settings WHERE enabled=1 ORDER BY account_id")
	if err != nil {
		return model.Notification{}, err
	}
	accounts := []model.Account{}
	for rows.Next() {
		var a model.Account
		var id, raw string
		var ver, rev int64
		if err = rows.Scan(&id, &ver, &rev, &raw); err != nil {
			rows.Close()
			return model.Notification{}, err
		}
		if json.Unmarshal([]byte(raw), &a) == nil {
			a.ID, a.CredentialVersion, a.Revision = id, ver, rev
			accounts = append(accounts, a)
		}
	}
	rows.Close()
	loc := copyLocation(ctx, tx)
	title, body, err := buildDailyReportCopyTx(ctx, tx, accounts, loc, now)
	if err != nil {
		return model.Notification{}, err
	}
	n, err := enqueueNotification(ctx, tx, model.Notification{
		EventKey: "daily_report_manual:" + model.NewID("dr_"),
		Kind:     "test",
		Severity: "info",
		Title:    title,
		Body:     body,
		Urgent:   true,
	}, now)
	if err != nil {
		return n, err
	}
	if err = appendAudit(ctx, tx, auditEvent{
		Action:     "daily_report_sent",
		TargetType: "daily_report",
		TargetID:   "default",
		Details:    model.AuditDetails{ChangedFields: []string{"notification_id"}, After: map[string]any{"notification_id": n.ID}},
	}, now); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/JasperXzy/harbormaster/internal/model"
)

const notificationColumns = `seq,id,event_key,coalesce(job_id,''),created_at,kind,severity,rule_key,title,body,account_id,instance_id,delivery_status,channel_revision,attempts,coalesce(lease_owner,''),last_error_code,uncertain,delivered_at,message_id,urgent,suppression_reason,suppressed_at,probe_plan_id,resource_id`

func readNotification(row rowScanner) (model.Notification, error) {
	var n model.Notification
	var created string
	var rule, account, instance, code, delivered, suppression, suppressedAt sql.NullString
	var message sql.NullInt64
	err := row.Scan(&n.Sequence, &n.ID, &n.EventKey, &n.JobID, &created, &n.Kind, &n.Severity, &rule, &n.Title, &n.Body, &account, &instance, &n.DeliveryStatus, &n.ChannelRevision, &n.Attempts, &n.Owner, &code, &n.Uncertain, &delivered, &message, &n.Urgent, &suppression, &suppressedAt, &n.ProbePlanID, &n.ResourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	if err != nil {
		return n, err
	}
	n.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return n, err
	}
	if rule.Valid {
		n.RuleKey = &rule.String
	}
	if account.Valid {
		n.AccountID = &account.String
	}
	if instance.Valid {
		n.InstanceID = &instance.String
	}
	if code.Valid {
		n.LastError = model.Error(code.String)
	}
	n.DeliveredAt = parseAgentTime(delivered)
	if suppression.Valid {
		n.SuppressionReason = &suppression.String
	}
	n.SuppressedAt = parseAgentTime(suppressedAt)
	if message.Valid {
		n.MessageID = &message.Int64
	}
	return n, nil
}
func enqueueNotification(ctx context.Context, tx *sql.Tx, in model.Notification, now time.Time) (model.Notification, error) {
	if in.EventKey == "" || len(in.EventKey) > 512 || in.Title == "" || !utf8.ValidString(in.Title+in.Body) || utf8.RuneCountInString(in.Title+"\n"+in.Body) > 4096 {
		return in, errors.New("invalid_notification")
	}
	existing, err := readNotification(tx.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE event_key=?", in.EventKey))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return in, err
	}
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM telegram_channel WHERE id=1").Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return in, err
	}
	status := "pending"
	if revision == 0 {
		status = "not_sent"
	}
	urgent, err := notificationUrgent(ctx, tx, in)
	if err != nil {
		return in, err
	}
	var reason, suppressed, quietRevision any
	if !urgent {
		quiet, err := readQuietHours(ctx, tx, now)
		if err != nil {
			return in, err
		}
		if quiet.Active {
			status = "not_sent"
			reason = quiet.ActiveReason
			suppressed = timestamp(now)
			quietRevision = quiet.Revision
		}
	}
	id := model.NewID("notice_")
	_, err = tx.ExecContext(ctx, `INSERT INTO notification(id,event_key,job_id,created_at,kind,severity,rule_key,title,body,account_id,instance_id,delivery_status,channel_revision,available_at,urgent,suppression_reason,suppressed_at,suppression_revision,probe_plan_id,resource_id)
 VALUES(?,?,nullif(?,''),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, in.EventKey, in.JobID, timestamp(now), in.Kind, in.Severity, in.RuleKey, in.Title, in.Body, in.AccountID, in.InstanceID, status, revision, now.Unix(), urgent, reason, suppressed, quietRevision, in.ProbePlanID, in.ResourceID)
	if err != nil {
		return in, err
	}
	if err = enqueueExtraChannelDeliveriesTx(ctx, tx, id, in, reason != nil, now); err != nil {
		return in, err
	}
	return readNotification(tx.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE id=?", id))
}
func (s *Store) EnqueueNotification(ctx context.Context, in model.Notification, now time.Time) (model.Notification, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	n, err := enqueueNotification(ctx, tx, in, now)
	if err != nil {
		return n, err
	}
	return n, tx.Commit()
}
func (s *Store) EnqueueTelegramTest(ctx context.Context, title, body string, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM telegram_channel WHERE id=1").Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrTelegramNotConfigured
	}
	if err != nil {
		return model.Job{}, err
	}
	j, err := enqueueJob(ctx, tx, "telegram_test", "telegram", []string{}, now)
	if err != nil {
		return j, err
	}
	_, err = enqueueNotification(ctx, tx, model.Notification{EventKey: "telegram-test:" + j.ID, JobID: j.ID, Kind: "test", Severity: "info", Title: title, Body: body}, now)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) Notification(ctx context.Context, id string) (model.Notification, error) {
	return readNotification(s.DB.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE id=?", id))
}
func (s *Store) Notifications(ctx context.Context, before int64, limit int) ([]model.Notification, error) {
	if before < 0 || limit < 1 || limit > 201 {
		return nil, errors.New("invalid_notification_page")
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?", before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.Notification{}
	for rows.Next() {
		n, err := readNotification(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	return items, rows.Err()
}

// retryUncertain is a required caller policy decision. An expired send lease
// may mean the message was sent and only its ACK was lost; recovery is not
// automatically equivalent to re-running a read-only collection job.
func (s *Store) ClaimNotification(ctx context.Context, now time.Time, maxAttempts int, retryUncertain bool) (model.Notification, error) {
	if maxAttempts < 1 || maxAttempts > 20 {
		return model.Notification{}, errors.New("invalid_notification_attempts")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Notification{}, err
	}
	defer tx.Rollback()
	// Destination changes are fenced by SaveTelegramChannel; check again here so
	// a stale queue can never be silently redirected to a newly configured chat.
	type terminalRule struct {
		where, code string
		args        []any
		uncertain   bool
	}
	terminal := []terminalRule{
		{"delivery_status='pending' AND channel_revision<>coalesce((SELECT revision FROM telegram_channel WHERE id=1),0)", "telegram_configuration_changed", nil, false},
	}
	if !retryUncertain {
		terminal = append(terminal, terminalRule{"delivery_status='pending' AND lease_owner IS NOT NULL AND lease_until<=?", "telegram_delivery_uncertain", []any{now.Unix()}, true})
	}
	terminal = append(terminal, terminalRule{"delivery_status='pending' AND attempts>=? AND (lease_owner IS NULL OR lease_until<=?)", "retry_exhausted", []any{maxAttempts, now.Unix()}, false})
	for _, rule := range terminal {
		args := append([]any{timestamp(now), rule.code}, rule.args...)
		if _, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',finished_at=?,error_code=?,lease_owner=NULL,lease_until=0 WHERE status IN ('queued','running') AND id IN (SELECT job_id FROM notification WHERE "+rule.where+")", args...); err != nil {
			return model.Notification{}, err
		}
		args = append([]any{rule.code, rule.uncertain}, rule.args...)
		if _, err = tx.ExecContext(ctx, "UPDATE notification SET delivery_status='failed',last_error_code=?,uncertain=(? OR uncertain OR lease_owner IS NOT NULL),lease_owner=NULL,lease_until=0 WHERE "+rule.where, args...); err != nil {
			return model.Notification{}, err
		}
	}
	quiet, err := readQuietHours(ctx, tx, now)
	if err != nil {
		return model.Notification{}, err
	}
	if err = suppressDueNotifications(ctx, tx, quiet, now, true); err != nil {
		return model.Notification{}, err
	}
	n, err := readNotification(tx.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE delivery_status='pending' AND available_at<=? AND (lease_owner IS NULL OR lease_until<=?) ORDER BY seq LIMIT 1", now.Unix(), now.Unix()))
	if errors.Is(err, ErrNotFound) {
		if err = tx.Commit(); err != nil {
			return n, err
		}
		return n, ErrNotFound
	}
	if err != nil {
		return n, err
	}
	owner := model.NewID("delivery_")
	if _, err = tx.ExecContext(ctx, "UPDATE notification SET uncertain=(uncertain OR lease_owner IS NOT NULL),lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?", owner, now.Add(time.Minute).Unix(), n.ID); err != nil {
		return n, err
	}
	if n.JobID != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=coalesce(started_at,?),attempts=attempts+1,lease_owner=?,lease_until=? WHERE id=? AND status IN ('queued','running')", timestamp(now), owner, now.Add(time.Minute).Unix(), n.JobID); err != nil {
			return n, err
		}
	}
	if n.Owner != "" {
		n.Uncertain = true
	}
	n.Owner = owner
	n.Attempts++
	return n, tx.Commit()
}

// FinishNotification atomically records the delivery result and public test job.
// The caller supplies a retry time only after applying its explicit policy.
func (s *Store) FinishNotification(ctx context.Context, n model.Notification, messageID int64, code string, retryAt *time.Time, uncertain bool, now time.Time) error {
	if code == "" && (messageID <= 0 || retryAt != nil || uncertain) {
		return errors.New("invalid_notification_result")
	}
	if retryAt != nil && !retryAt.After(now) {
		return errors.New("invalid_notification_retry")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification n JOIN telegram_channel c ON c.id=1 AND c.revision=n.channel_revision
	WHERE n.id=? AND n.delivery_status='pending' AND n.lease_owner=? AND n.lease_until>?
 AND (n.job_id IS NULL OR EXISTS(SELECT 1 FROM job j WHERE j.id=n.job_id AND j.status='running' AND j.lease_owner=n.lease_owner AND j.lease_until>?))`, n.ID, n.Owner, now.Unix(), now.Unix()).Scan(&valid)
	if err != nil {
		return err
	}
	if valid != 1 {
		return ErrLeaseLost
	}
	status, jobStatus := "sent", "succeeded"
	var errorCode, delivered, message any
	available := now.Unix()
	if code != "" {
		status = "failed"
		jobStatus = "failed"
		errorCode = code
	} else {
		delivered = timestamp(now)
		message = messageID
	}
	if retryAt != nil {
		status = "pending"
		jobStatus = "queued"
		available = retryAt.Unix()
		if retryAt.Nanosecond() > 0 {
			available++
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification SET delivery_status=?,available_at=?,lease_owner=NULL,lease_until=0,last_error_code=?,uncertain=(uncertain OR ?),delivered_at=?,message_id=? WHERE id=?`, status, available, errorCode, uncertain, delivered, message, n.ID); err != nil {
		return err
	}
	if n.JobID != "" {
		var finished any
		if jobStatus != "queued" {
			finished = timestamp(now)
		}
		if _, err = tx.ExecContext(ctx, "UPDATE job SET status=?,finished_at=?,error_code=?,lease_owner=NULL,lease_until=0,available_at=? WHERE id=? AND lease_owner=?", jobStatus, finished, errorCode, available, n.JobID, n.Owner); err != nil {
			return err
		}
	}
	if code == "" {
		_, err = tx.ExecContext(ctx, "UPDATE telegram_channel SET last_success_at=?,last_error_code=NULL WHERE id=1 AND revision=?", timestamp(now), n.ChannelRevision)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE telegram_channel SET last_error_code=? WHERE id=1 AND revision=?", code, n.ChannelRevision)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// predicate is a fixed internal SQL expression; values remain bound arguments.
func cancelPendingNotifications(ctx context.Context, tx *sql.Tx, predicate string, values []any, code string, now time.Time) error {
	args := append([]any{timestamp(now), code}, values...)
	if _, err := tx.ExecContext(ctx, "UPDATE job SET status='failed',finished_at=?,error_code=?,lease_owner=NULL,lease_until=0 WHERE status IN ('queued','running') AND id IN (SELECT job_id FROM notification WHERE delivery_status='pending' AND "+predicate+")", args...); err != nil {
		return err
	}
	args = append([]any{code}, values...)
	_, err := tx.ExecContext(ctx, `UPDATE notification SET delivery_status=CASE WHEN lease_owner IS NULL THEN 'not_sent' ELSE 'failed' END,
 last_error_code=?,uncertain=(uncertain OR lease_owner IS NOT NULL),lease_owner=NULL,lease_until=0 WHERE delivery_status='pending' AND `+predicate, args...)
	return err
}

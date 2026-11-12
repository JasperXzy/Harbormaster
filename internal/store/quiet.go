package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrQuietChanged = errors.New("quiet_hours_changed")

type quietReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readQuietHours(ctx context.Context, q quietReader, now time.Time) (model.QuietHours, error) {
	var result model.QuietHours
	var start, end, updated sql.NullString
	err := q.QueryRowContext(ctx, `SELECT revision,enabled,start_time,end_time,maintenance_start,maintenance_end,updated_at,
 coalesce((SELECT value FROM app_meta WHERE key='business_timezone'),'Asia/Shanghai') FROM quiet_hours WHERE id=1`).Scan(&result.Revision, &result.Enabled, &result.Start, &result.End, &start, &end, &updated, &result.Timezone)
	if err != nil {
		return result, err
	}
	result.UpdatedAt = parseAgentTime(updated)
	if start.Valid {
		a, e1 := time.Parse(time.RFC3339Nano, start.String)
		b, e2 := time.Parse(time.RFC3339Nano, end.String)
		if e1 != nil || e2 != nil {
			return result, errors.New("invalid_stored_maintenance")
		}
		result.Maintenance = &model.MaintenanceWindow{StartAt: a, EndAt: b}
	}
	if len(result.Validate()) != 0 {
		return result, errors.New("invalid_stored_quiet_hours")
	}
	err = result.Evaluate(now)
	return result, err
}
func (s *Store) QuietHours(ctx context.Context, now time.Time) (model.QuietHours, error) {
	return readQuietHours(ctx, s.DB, now)
}
func (s *Store) SaveQuietHours(ctx context.Context, in model.QuietHours, now time.Time) (model.QuietHours, error) {
	if fields := in.Validate(); len(fields) > 0 {
		return model.QuietHours{}, &AlertValidationError{Fields: fields}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.QuietHours{}, err
	}
	defer tx.Rollback()
	current, err := readQuietHours(ctx, tx, now)
	if err != nil {
		return current, err
	}
	if current.Revision != in.Revision {
		return current, ErrQuietChanged
	}
	var start, end any
	if in.Maintenance != nil {
		start = timestamp(in.Maintenance.StartAt)
		end = timestamp(in.Maintenance.EndAt)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE quiet_hours SET revision=revision+1,enabled=?,start_time=?,end_time=?,maintenance_start=?,maintenance_end=?,updated_at=? WHERE id=1`, in.Enabled, in.Start, in.End, start, end, timestamp(now)); err != nil {
		return current, err
	}
	current, err = readQuietHours(ctx, tx, now)
	if err != nil {
		return current, err
	}
	if err = suppressDueNotifications(ctx, tx, current, now, false); err != nil {
		return current, err
	}
	return current, tx.Commit()
}
func notificationUrgent(ctx context.Context, q quietReader, n model.Notification) (bool, error) {
	if n.AlwaysUrgent() {
		return true, nil
	}
	if n.RuleKey == nil {
		return false, nil
	}
	var urgent bool
	err := q.QueryRowContext(ctx, "SELECT urgent FROM alert_rule WHERE key=?", *n.RuleKey).Scan(&urgent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return urgent, err
}

// Never changes cloud operation jobs. Explicit delivery tests bypass quiet.
// Active leases are left intact: only their owner can still check before send.
func suppressDueNotifications(ctx context.Context, tx *sql.Tx, q model.QuietHours, now time.Time, includeExpired bool) error {
	if !q.Active {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE notification SET delivery_status='not_sent',suppression_reason=?,suppressed_at=?,suppression_revision=?,
 uncertain=(uncertain OR lease_owner IS NOT NULL),lease_owner=NULL,lease_until=0
 WHERE delivery_status='pending' AND urgent=0 AND kind<>'test' AND available_at<=?
 AND (lease_owner IS NULL OR (? AND lease_until<=?))`, q.ActiveReason, timestamp(now), q.Revision, now.Unix(), includeExpired, now.Unix())
	return err
}

// AuthorizeNotification must be called by a dispatcher immediately before the
// network send. False means this lease was terminally suppressed, not deferred.
func (s *Store) AuthorizeNotification(ctx context.Context, leased model.Notification, now time.Time) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM notification n JOIN telegram_channel c ON c.id=1 AND c.revision=n.channel_revision
 WHERE n.id=? AND n.delivery_status='pending' AND n.lease_owner=? AND n.lease_until>?
 AND (n.job_id IS NULL OR EXISTS(SELECT 1 FROM job j WHERE j.id=n.job_id AND j.status='running' AND j.lease_owner=n.lease_owner AND j.lease_until>?))`, leased.ID, leased.Owner, now.Unix(), now.Unix()).Scan(&valid)
	if err != nil {
		return false, err
	}
	if valid != 1 {
		return false, ErrLeaseLost
	}
	n, err := readNotification(tx.QueryRowContext(ctx, "SELECT "+notificationColumns+" FROM notification WHERE id=?", leased.ID))
	if err != nil {
		return false, err
	}
	if !n.Urgent && n.Kind != "test" {
		quiet, err := readQuietHours(ctx, tx, now)
		if err != nil {
			return false, err
		}
		if quiet.Active {
			if _, err = tx.ExecContext(ctx, `UPDATE notification SET delivery_status='not_sent',suppression_reason=?,suppressed_at=?,suppression_revision=?,lease_owner=NULL,lease_until=0 WHERE id=?`, quiet.ActiveReason, timestamp(now), quiet.Revision, n.ID); err != nil {
				return false, err
			}
			return false, tx.Commit()
		}
	}
	return true, tx.Commit()
}

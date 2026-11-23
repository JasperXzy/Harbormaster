package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrChannelChanged = errors.New("telegram_configuration_changed")
var ErrTelegramNotConfigured = errors.New("telegram_not_configured")

func (s *Store) TelegramChannel(ctx context.Context) ([]byte, model.TelegramChannelState, error) {
	var ciphertext []byte
	var state model.TelegramChannelState
	var at string
	var success, code sql.NullString
	err := s.DB.QueryRowContext(ctx, "SELECT ciphertext,revision,updated_at,last_success_at,last_error_code FROM telegram_channel WHERE id=1").Scan(&ciphertext, &state.Revision, &at, &success, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state, ErrTelegramNotConfigured
	}
	if err != nil {
		return nil, state, err
	}
	state.UpdatedAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, state, err
	}
	state.LastSuccessAt = parseAgentTime(success)
	if code.Valid {
		state.LastError = model.Error(code.String)
	}
	return ciphertext, state, nil
}

// SaveTelegramChannel only receives ciphertext. It fences queued/in-flight
// sends against recipient or credential changes in the same transaction.
func (s *Store) SaveTelegramChannel(ctx context.Context, ciphertext []byte, expectedRevision int64, details model.AuditDetails, now time.Time) (model.TelegramChannelState, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.TelegramChannelState{}, err
	}
	defer tx.Rollback()
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM telegram_channel WHERE id=1").Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.TelegramChannelState{}, err
	}
	if revision != expectedRevision {
		return model.TelegramChannelState{}, ErrChannelChanged
	}
	state := model.TelegramChannelState{Revision: revision + 1, UpdatedAt: now.UTC()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO telegram_channel(id,ciphertext,revision,updated_at) VALUES(1,?,?,?)
 ON CONFLICT(id) DO UPDATE SET ciphertext=excluded.ciphertext,revision=excluded.revision,updated_at=excluded.updated_at,last_success_at=NULL,last_error_code=NULL`, ciphertext, state.Revision, timestamp(now)); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',finished_at=?,error_code='telegram_configuration_changed',lease_owner=NULL,lease_until=0
 WHERE id IN (SELECT job_id FROM notification WHERE delivery_status='pending') AND status IN ('queued','running')`, timestamp(now)); err != nil {
		return state, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification SET delivery_status='failed',last_error_code='telegram_configuration_changed',uncertain=(lease_owner IS NOT NULL),lease_owner=NULL,lease_until=0 WHERE delivery_status='pending'`); err != nil {
		return state, err
	}
	details.Revision = &state.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "telegram_channel_updated", TargetType: "telegram_channel", TargetID: "1", Details: details}, now); err != nil {
		return state, err
	}
	return state, tx.Commit()
}

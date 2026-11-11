package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

var ErrUnauthorized = errors.New("invalid_agent_token")
var ErrConflict = errors.New("report_conflict")

func (s *Store) AcceptReport(ctx context.Context, token string, r agentproto.Report, now time.Time) (bool, error) {
	if err := r.Validate(now); err != nil {
		return false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	h := sha256.Sum256([]byte(token))
	var agentID string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM agent WHERE token_hash=? AND revoked_at IS NULL", h[:]).Scan(&agentID); err != nil || agentID != r.AgentID {
		return false, ErrUnauthorized
	}
	var at string
	var uptime, rx, txbytes int64
	err = tx.QueryRowContext(ctx, "SELECT sampled_at,uptime_ms,rx_bytes,tx_bytes FROM traffic_sample WHERE agent_id=? AND counter_epoch=? AND sequence=?", r.AgentID, r.CounterEpoch, r.Sequence).Scan(&at, &uptime, &rx, &txbytes)
	if err == nil {
		if at != timestamp(r.SampledAt) || uptime != r.UptimeMS || rx != r.RXBytes || txbytes != r.TXBytes {
			return false, ErrConflict
		}
		// Replays never advance last_received_at and cannot keep a dead agent online.
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var epoch string
	var seq int64
	err = tx.QueryRowContext(ctx, "SELECT counter_epoch,sequence,uptime_ms,rx_bytes,tx_bytes,sampled_at FROM traffic_sample WHERE agent_id=? ORDER BY received_at DESC,sequence DESC LIMIT 1", r.AgentID).Scan(&epoch, &seq, &uptime, &rx, &txbytes, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil {
		if epoch == r.CounterEpoch {
			if r.Sequence <= seq || r.UptimeMS <= uptime || r.RXBytes < rx || r.TXBytes < txbytes {
				return false, ErrConflict
			}
		} else {
			var n int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM traffic_sample WHERE agent_id=? AND counter_epoch=?", r.AgentID, r.CounterEpoch).Scan(&n); err != nil {
				return false, err
			}
			if n > 0 {
				return false, ErrConflict
			}
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO traffic_sample VALUES(?,?,?,?,?,?,?,?)", r.AgentID, r.CounterEpoch, r.Sequence, timestamp(r.SampledAt), timestamp(now), r.UptimeMS, r.RXBytes, r.TXBytes)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE agent SET last_received_at=?,version=? WHERE id=?", timestamp(now), r.AgentVersion, r.AgentID)
	if err != nil {
		return false, err
	}
	return false, tx.Commit()
}

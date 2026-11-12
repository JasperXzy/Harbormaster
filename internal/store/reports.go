package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
)

var ErrUnauthorized = errors.New("invalid_agent_token")
var ErrConflict = errors.New("report_conflict")
var ErrSampleExpired = errors.New("sample_expired")

func (s *Store) AcceptReport(ctx context.Context, token string, r agentproto.Report, now time.Time) (bool, error) {
	duplicates, err := s.AcceptReports(ctx, token, []agentproto.Report{r}, now)
	if err != nil {
		return false, err
	}
	return duplicates[0], nil
}

// A batch is atomic and ordered. A later invalid sample rolls back earlier
// samples, so an ACK always describes a fully durable batch.
func (s *Store) AcceptReports(ctx context.Context, token string, reports []agentproto.Report, now time.Time) ([]bool, error) {
	if len(reports) == 0 || len(reports) > agentproto.MaxBatch {
		return nil, errors.New("invalid_report")
	}
	if len(token) < 16 || len(token) > 256 {
		return nil, ErrUnauthorized
	}
	for _, r := range reports {
		if err := r.Validate(now); err != nil {
			return nil, err
		}
		if r.AgentID != reports[0].AgentID {
			return nil, ErrUnauthorized
		}
		if r.SampledAt.Before(now.Add(-7 * 24 * time.Hour)) {
			return nil, ErrSampleExpired
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	h := sha256.Sum256([]byte(token))
	var agentID string
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT g.id,((coalesce(a.enabled,0)=1 AND EXISTS(SELECT 1 FROM resource_snapshot rs WHERE rs.id=i.id AND rs.account_id=i.account_id AND rs.kind=i.kind AND rs.present=1)) OR (SELECT value FROM app_meta WHERE key='mode')='development')
 FROM agent g JOIN instance i ON i.id=g.instance_id LEFT JOIN account_settings a ON a.account_id=i.account_id
 WHERE g.token_hash=? AND g.revoked_at IS NULL`, h[:]).Scan(&agentID, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if agentID != reports[0].AgentID {
		return nil, ErrUnauthorized
	}
	if !enabled {
		return nil, ErrAccountDisabled
	}
	duplicates := make([]bool, 0, len(reports))
	for _, r := range reports {
		duplicate, err := acceptSample(ctx, tx, r, now)
		if err != nil {
			return nil, err
		}
		duplicates = append(duplicates, duplicate)
	}
	return duplicates, tx.Commit()
}

func acceptSample(ctx context.Context, tx *sql.Tx, r agentproto.Report, now time.Time) (bool, error) {
	var at string
	var uptime, rx, txbytes int64
	err := tx.QueryRowContext(ctx, "SELECT sampled_at,uptime_ms,rx_bytes,tx_bytes FROM traffic_sample WHERE agent_id=? AND counter_epoch=? AND sequence=?", r.AgentID, r.CounterEpoch, r.Sequence).Scan(&at, &uptime, &rx, &txbytes)
	var family any
	if r.FamilyCounters != nil {
		b, e := json.Marshal(r.FamilyCounters)
		if e != nil {
			return false, e
		}
		family = string(b)
	}
	if err == nil {
		if at != timestamp(r.SampledAt) || uptime != r.UptimeMS || rx != r.RXBytes || txbytes != r.TXBytes {
			return false, ErrConflict
		}
		var rules, version string
		var dropped int64
		var interval int
		var existing sql.NullString
		err = tx.QueryRowContext(ctx, "SELECT rules_version,dropped_samples,family_json,agent_version,report_interval_seconds FROM traffic_sample_detail WHERE agent_id=? AND counter_epoch=? AND sequence=?", r.AgentID, r.CounterEpoch, r.Sequence).Scan(&rules, &dropped, &existing, &version, &interval)
		if errors.Is(err, sql.ErrNoRows) {
			if r.RulesVersion != "" || r.DroppedSamples != 0 || r.FamilyCounters != nil || r.IntervalSeconds() != agentproto.DefaultReportIntervalSeconds {
				return false, ErrConflict
			}
		} else if err != nil {
			return false, err
		} else {
			if interval != r.IntervalSeconds() || rules != r.RulesVersion || dropped != r.DroppedSamples || version != r.AgentVersion || (existing.Valid != (family != nil)) || (existing.Valid && existing.String != family) {
				return false, ErrConflict
			}
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var epoch, rules string
	var seq int64
	err = tx.QueryRowContext(ctx, "SELECT counter_epoch,sequence,uptime_ms,rx_bytes,tx_bytes,sampled_at,rules_version FROM agent_counter_state WHERE agent_id=?", r.AgentID).Scan(&epoch, &seq, &uptime, &rx, &txbytes, &at, &rules)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	quality := "baseline"
	if err == nil {
		if epoch == r.CounterEpoch {
			if r.Sequence <= seq || r.UptimeMS <= uptime || r.RXBytes < rx || r.TXBytes < txbytes || (rules != "" && r.RulesVersion != rules) {
				return false, ErrConflict
			}
			quality = "ok"
			if r.UptimeMS-uptime > agentproto.RateWindow(r.IntervalSeconds()).Milliseconds() {
				quality = "gap"
			}
			prior, e := time.Parse(time.RFC3339Nano, at)
			if e != nil {
				return false, e
			}
			if r.SampledAt.Before(prior) {
				quality = "clock_skew"
			}
		} else {
			var n int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_retired_epoch WHERE agent_id=? AND counter_epoch=?", r.AgentID, r.CounterEpoch).Scan(&n); err != nil {
				return false, err
			}
			if n != 0 {
				return false, ErrConflict
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO agent_retired_epoch(agent_id,counter_epoch,retired_at) VALUES(?,?,?)", r.AgentID, epoch, timestamp(now)); err != nil {
				return false, err
			}
			quality = "counter_reset"
		}
	}
	fresh := !r.SampledAt.Before(now.Add(-30*time.Second)) && !r.SampledAt.After(now.Add(30*time.Second))
	if !fresh && quality != "clock_skew" {
		quality = "stale"
	}
	if r.SampledAt.After(now.Add(30 * time.Second)) {
		quality = "clock_skew"
	}
	if r.DroppedSamples > 0 && quality != "clock_skew" {
		quality = "gap"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO traffic_sample VALUES(?,?,?,?,?,?,?,?)", r.AgentID, r.CounterEpoch, r.Sequence, timestamp(r.SampledAt), timestamp(now), r.UptimeMS, r.RXBytes, r.TXBytes); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO traffic_sample_detail(agent_id,counter_epoch,sequence,rules_version,dropped_samples,family_json,agent_version,quality,report_interval_seconds) VALUES(?,?,?,?,?,?,?,?,?)", r.AgentID, r.CounterEpoch, r.Sequence, r.RulesVersion, r.DroppedSamples, family, r.AgentVersion, quality, r.IntervalSeconds()); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_counter_state(agent_id,counter_epoch,sequence,uptime_ms,rx_bytes,tx_bytes,sampled_at,received_at,rules_version,quality,report_interval_seconds) VALUES(?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(agent_id) DO UPDATE SET counter_epoch=excluded.counter_epoch,sequence=excluded.sequence,uptime_ms=excluded.uptime_ms,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes,sampled_at=excluded.sampled_at,received_at=excluded.received_at,rules_version=excluded.rules_version,quality=excluded.quality,report_interval_seconds=excluded.report_interval_seconds`, r.AgentID, r.CounterEpoch, r.Sequence, r.UptimeMS, r.RXBytes, r.TXBytes, timestamp(r.SampledAt), timestamp(now), r.RulesVersion, quality, r.IntervalSeconds()); err != nil {
		return false, err
	}
	if fresh && quality != "clock_skew" {
		_, err = tx.ExecContext(ctx, "UPDATE agent SET last_received_at=?,version=? WHERE id=?", timestamp(now), r.AgentVersion, r.AgentID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE agent SET version=? WHERE id=?", r.AgentVersion, r.AgentID)
	}
	return false, err
}

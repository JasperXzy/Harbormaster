package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func (s *Store) AgentNetworkHistory(ctx context.Context, f model.AgentNetworkHistoryFilter, limit int) ([]model.AgentNetworkSample, error) {
	result := []model.AgentNetworkSample{}
	if !f.Valid(time.Now().UTC()) || limit < 1 || limit > 1001 {
		return nil, errors.New("invalid_history_query")
	}
	var agentID sql.NullString
	err := s.DB.QueryRowContext(ctx, "SELECT a.id FROM instance i LEFT JOIN agent a ON a.instance_id=i.id WHERE i.id=?", f.InstanceID).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !agentID.Valid {
		return result, nil
	}
	query := `SELECT c.counter_epoch,c.sequence,c.sampled_at,c.received_at,c.uptime_ms,c.rx_bytes,c.tx_bytes,
 d.quality,d.rules_version,d.report_interval_seconds,d.dropped_samples,
 p.sequence,p.sampled_at,p.uptime_ms,p.rx_bytes,p.tx_bytes,pd.quality,pd.rules_version,
 (SELECT sampled_at FROM traffic_sample pp WHERE pp.agent_id=p.agent_id AND pp.counter_epoch=p.counter_epoch AND pp.sequence=(SELECT max(sequence) FROM traffic_sample WHERE agent_id=p.agent_id AND counter_epoch=p.counter_epoch AND sequence<p.sequence))
 FROM traffic_sample c
 LEFT JOIN traffic_sample_detail d ON d.agent_id=c.agent_id AND d.counter_epoch=c.counter_epoch AND d.sequence=c.sequence
 LEFT JOIN traffic_sample p ON p.agent_id=c.agent_id AND p.counter_epoch=c.counter_epoch AND p.sequence=
 (SELECT max(sequence) FROM traffic_sample WHERE agent_id=c.agent_id AND counter_epoch=c.counter_epoch AND sequence<c.sequence)
 LEFT JOIN traffic_sample_detail pd ON pd.agent_id=p.agent_id AND pd.counter_epoch=p.counter_epoch AND pd.sequence=p.sequence
 WHERE c.agent_id=? AND c.sampled_at>=? AND c.sampled_at<?`
	args := []any{agentID.String, timestamp(f.From), timestamp(f.To)}
	if !f.BeforeTime.IsZero() {
		query += ` AND (c.sampled_at<? OR (c.sampled_at=? AND (c.counter_epoch<? OR (c.counter_epoch=? AND c.sequence<?))))`
		args = append(args, timestamp(f.BeforeTime), timestamp(f.BeforeTime), f.BeforeEpoch, f.BeforeEpoch, f.BeforeSequence)
	}
	query += " ORDER BY c.sampled_at DESC,c.counter_epoch DESC,c.sequence DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v model.AgentNetworkSample
		var sampled, received string
		var quality, rules, prevAt, prevQuality, prevRules, prevPreviousAt sql.NullString
		var cadence, dropped, prevSeq, prevUptime, prevRX, prevTX sql.NullInt64
		if err = rows.Scan(&v.CounterEpoch, &v.Sequence, &sampled, &received, &v.UptimeMS, &v.RXBytes, &v.TXBytes, &quality, &rules, &cadence, &dropped, &prevSeq, &prevAt, &prevUptime, &prevRX, &prevTX, &prevQuality, &prevRules, &prevPreviousAt); err != nil {
			return nil, err
		}
		if v.SampledAt, err = time.Parse(time.RFC3339Nano, sampled); err != nil {
			return nil, err
		}
		if v.ReceivedAt, err = time.Parse(time.RFC3339Nano, received); err != nil {
			return nil, err
		}
		v.Quality = "unknown"
		if quality.Valid {
			v.Quality = quality.String
		}
		if rules.Valid {
			v.RulesVersion = &rules.String
		}
		if cadence.Valid {
			n := int(cadence.Int64)
			v.ReportIntervalSeconds = &n
		}
		if dropped.Valid {
			v.DroppedSamples = &dropped.Int64
		}
		prior := agentHistoryPrevious{Sequence: prevSeq, Uptime: prevUptime, RX: prevRX, TX: prevTX, At: parseAgentTime(prevAt), Quality: prevQuality, Rules: prevRules, PreviousAt: parseAgentTime(prevPreviousAt)}
		deriveAgentHistoryInterval(&v, prior)
		result = append(result, v)
	}
	return result, rows.Err()
}

type agentHistoryPrevious struct {
	PreviousAt               *time.Time
	Sequence, Uptime, RX, TX sql.NullInt64
	At                       *time.Time
	Quality, Rules           sql.NullString
}

func deriveAgentHistoryInterval(v *model.AgentNetworkSample, p agentHistoryPrevious) {
	v.Interval = nil
	v.IntervalStatus = "unavailable"
	unavailable := func(reason string) { v.IntervalUnavailableReason = &reason }
	if !p.Sequence.Valid {
		unavailable("no_previous_sample")
		return
	}
	if v.RulesVersion == nil || v.ReportIntervalSeconds == nil || !p.Rules.Valid || !p.Quality.Valid {
		unavailable("metadata_unknown")
		return
	}
	if *v.RulesVersion != p.Rules.String {
		unavailable("rules_changed")
		return
	}
	if p.At == nil || !v.SampledAt.After(*p.At) || v.Quality == "clock_skew" || p.Quality.String == "clock_skew" || (p.PreviousAt != nil && !p.At.After(*p.PreviousAt)) {
		unavailable("clock_skew")
		return
	}
	if !p.Uptime.Valid || !p.RX.Valid || !p.TX.Valid || v.UptimeMS <= p.Uptime.Int64 || v.RXBytes < p.RX.Int64 || v.TXBytes < p.TX.Int64 {
		unavailable("invalid_counter")
		return
	}
	duration := v.UptimeMS - p.Uptime.Int64
	rx, tx := v.RXBytes-p.RX.Int64, v.TXBytes-p.TX.Int64
	rxRate, rxOK := byteRate(rx, duration)
	txRate, txOK := byteRate(tx, duration)
	if !rxOK || !txOK {
		unavailable("rate_overflow")
		return
	}
	v.Interval = &model.AgentNetworkInterval{PreviousSequence: p.Sequence.Int64, StartAt: *p.At, EndAt: v.SampledAt, DurationMS: duration, RXBytes: rx, TXBytes: tx, RXRate: rxRate, TXRate: txRate}
	v.IntervalUnavailableReason = nil
	v.IntervalStatus = "continuous"
	if v.Sequence-p.Sequence.Int64 != 1 || duration > agentproto.RateWindow(*v.ReportIntervalSeconds).Milliseconds() || v.Quality == "gap" || (v.DroppedSamples != nil && *v.DroppedSamples > 0) {
		v.IntervalStatus = "gap"
	}
}

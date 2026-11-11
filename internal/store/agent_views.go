package store

import (
	"context"
	"database/sql"
	"math/big"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// AgentViews reads the current ledger and the previous sample in its epoch.
// Receipt order alone is insufficient: a catch-up batch shares one receipt time.
func (s *Store) AgentViews(ctx context.Context, now time.Time) (map[string]model.AgentView, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT a.instance_id,a.version,a.registered_at,a.last_received_at,a.revoked_at,
 coalesce(cfg.enabled,0),c.sampled_at,c.received_at,c.quality,c.uptime_ms,c.rx_bytes,c.tx_bytes,
 p.uptime_ms,p.rx_bytes,p.tx_bytes,p.sampled_at
 FROM agent a JOIN instance i ON i.id=a.instance_id LEFT JOIN account_settings cfg ON cfg.account_id=i.account_id
 LEFT JOIN agent_counter_state c ON c.agent_id=a.id
 LEFT JOIN traffic_sample p ON p.agent_id=c.agent_id AND p.counter_epoch=c.counter_epoch AND p.sequence=
 (SELECT max(sequence) FROM traffic_sample WHERE agent_id=c.agent_id AND counter_epoch=c.counter_epoch AND sequence<c.sequence)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := map[string]model.AgentView{}
	for rows.Next() {
		var id, version string
		var registered, received, revoked, sampled, ledgerReceived, quality, previousAt sql.NullString
		var uptime, rx, tx, prevUptime, prevRX, prevTX sql.NullInt64
		var enabled bool
		if err = rows.Scan(&id, &version, &registered, &received, &revoked, &enabled, &sampled, &ledgerReceived, &quality, &uptime, &rx, &tx, &prevUptime, &prevRX, &prevTX, &previousAt); err != nil {
			return nil, err
		}
		v := model.AgentView{Agent: model.AgentStatus{Status: "not_installed"}}
		if !revoked.Valid {
			v.Agent.Status = "offline"
			v.Agent.Version = &version
			v.Agent.RegisteredAt = parseAgentTime(registered)
			v.Agent.LastReportAt = parseAgentTime(received)
			v.Agent.Pending = !received.Valid
			if quality.Valid && received.Valid {
				q := quality.String
				v.Agent.Quality = &q
			}
			if last := v.Agent.LastReportAt; last != nil {
				offline := last.Add(30 * time.Second)
				if enabled && now.Before(offline) && !last.After(now) {
					v.Agent.Status = "online"
				} else if !now.Before(offline) {
					v.Agent.OfflineSince = &offline
				}
			}
			at := parseAgentTime(sampled)
			prior := parseAgentTime(previousAt)
			ledgerAt := parseAgentTime(ledgerReceived)
			// Registration clears heartbeat; only samples from this registration
			// may supply rates. Time jumps and long gaps are not current rates.
			if v.Agent.Status == "online" && quality.String == "ok" && at != nil && prior != nil && ledgerAt != nil &&
				(v.Agent.RegisteredAt == nil || !ledgerAt.Before(*v.Agent.RegisteredAt)) &&
				!at.Before(now.Add(-30*time.Second)) && !at.After(now.Add(30*time.Second)) && at.After(*prior) &&
				prevUptime.Valid && uptime.Int64 > prevUptime.Int64 && uptime.Int64-prevUptime.Int64 <= 30000 && rx.Int64 >= prevRX.Int64 && tx.Int64 >= prevTX.Int64 {
				dt := uptime.Int64 - prevUptime.Int64
				rxRate, rxOK := byteRate(rx.Int64-prevRX.Int64, dt)
				txRate, txOK := byteRate(tx.Int64-prevTX.Int64, dt)
				if rxOK && txOK {
					v.Realtime = &model.InstanceRealtime{RX: rxRate, TX: txRate, SampledAt: *at}
				}
			}
		}
		views[id] = v
	}
	return views, rows.Err()
}
func parseAgentTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}
func byteRate(delta, ms int64) (int64, bool) {
	if delta < 0 || ms <= 0 {
		return 0, false
	}
	n := new(big.Int).Mul(big.NewInt(delta), big.NewInt(1000))
	n.Quo(n, big.NewInt(ms))
	return n.Int64(), n.IsInt64()
}

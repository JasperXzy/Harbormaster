package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type HistoryPruneResult struct {
	RawAgentSamples int64
	HourlyBuckets   int64
	CloudSamples    int64
	AuditEntries    int64
	Jobs            int64
	More            bool
}

func (s *Store) PruneDataHistory(ctx context.Context, now time.Time, ret model.RetentionSettings, batch int) (HistoryPruneResult, error) {
	var out HistoryPruneResult
	if batch <= 0 || batch > 2000 {
		batch = 500
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	// Ensure raw samples cover both configured agent_raw_days and estimation lag window + 24h.
	lagSeconds := 3600
	_ = tx.QueryRowContext(ctx, "SELECT lag_seconds FROM metering_settings WHERE id=1").Scan(&lagSeconds)
	minRawWindow := max(time.Duration(ret.AgentRawDays)*24*time.Hour, time.Duration(lagSeconds)*time.Second+24*time.Hour)
	rawCutoff := now.UTC().Add(-minRawWindow).Truncate(time.Hour)

	// Delete rolled-up raw samples (traffic_sample_detail cascades via FK), keeping
	// active counter baselines in agent_counter_state and any un-rolled-up hours.
	res, err := tx.ExecContext(ctx, `DELETE FROM traffic_sample WHERE (agent_id, counter_epoch, sequence) IN (
		SELECT s.agent_id, s.counter_epoch, s.sequence FROM traffic_sample s
		WHERE s.sampled_at < ?
		AND NOT EXISTS (
			SELECT 1 FROM agent_counter_state cs
			WHERE cs.agent_id = s.agent_id AND cs.counter_epoch = s.counter_epoch AND cs.sequence = s.sequence
		)
		AND NOT EXISTS (
			SELECT 1 FROM hourly_work hw
			WHERE hw.agent_id = s.agent_id AND hw.hour_start = substr(s.sampled_at, 1, 13) || ':00:00Z' AND hw.pending = 1
		)
		LIMIT ?
	)`, timestamp(rawCutoff), batch)
	if err != nil {
		return out, err
	}
	out.RawAgentSamples, _ = res.RowsAffected()

	hourlyCutoff := now.UTC().AddDate(0, -max(1, ret.AgentHourlyMonths), 0).Truncate(time.Hour)
	res, err = tx.ExecContext(ctx, `DELETE FROM agent_hourly_traffic WHERE (agent_id, hour_start) IN (
		SELECT agent_id, hour_start FROM agent_hourly_traffic WHERE hour_start < ? LIMIT ?
	)`, hourlyCutoff.Format(time.RFC3339), batch)
	if err != nil {
		return out, err
	}
	out.HourlyBuckets, _ = res.RowsAffected()
	_, _ = tx.ExecContext(ctx, "DELETE FROM hourly_work WHERE pending=0 AND hour_start < ?", hourlyCutoff.Format(time.RFC3339))

	cloudCutoff := now.UTC().Add(-time.Duration(max(2, ret.AgentRawDays)) * 24 * time.Hour)
	res, err = tx.ExecContext(ctx, `DELETE FROM cloud_network_sample WHERE sequence IN (
		SELECT sequence FROM cloud_network_sample WHERE sampled_at < ? LIMIT ?
	)`, timestamp(cloudCutoff), batch)
	if err != nil && !isMissingTable(err) {
		return out, err
	}
	if res != nil {
		out.CloudSamples, _ = res.RowsAffected()
	}

	auditCutoff := now.UTC().Add(-time.Duration(max(7, ret.AuditLogDays)) * 24 * time.Hour)
	res, err = tx.ExecContext(ctx, `DELETE FROM audit_log WHERE seq IN (
		SELECT seq FROM audit_log WHERE created_at < ? ORDER BY seq ASC LIMIT ?
	)`, timestamp(auditCutoff), batch)
	if err != nil {
		return out, err
	}
	out.AuditEntries, _ = res.RowsAffected()

	// Prune terminal unreferenced read-only jobs (refresh / permission_check / heartbeat_test).
	jobCutoff := now.UTC().Add(-time.Duration(max(7, ret.JobHistoryDays)) * 24 * time.Hour)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM job
		WHERE status IN ('succeeded','failed')
		AND type IN ('refresh','permission_check','heartbeat_test')
		AND finished_at IS NOT NULL AND finished_at < ?
		AND NOT EXISTS (SELECT 1 FROM cdt_observation o WHERE o.job_id = job.id)
		AND NOT EXISTS (SELECT 1 FROM notification n WHERE n.job_id = job.id)
		LIMIT ?`, timestamp(jobCutoff), batch)
	if err != nil {
		return out, err
	}
	jobIDs := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		jobIDs = append(jobIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, id := range jobIDs {
		for _, child := range []string{
			"DELETE FROM heartbeat_test_job WHERE job_id=?",
			"DELETE FROM api_key_refresh_job WHERE job_id=?",
			"DELETE FROM job_change WHERE job_id=?",
			"DELETE FROM job_order WHERE job_id=?",
			"DELETE FROM job WHERE id=?",
		} {
			if _, err = tx.ExecContext(ctx, child, id); err != nil && !isMissingTable(err) {
				return out, err
			}
		}
		out.Jobs++
	}

	out.More = int(out.RawAgentSamples) >= batch || int(out.HourlyBuckets) >= batch || int(out.AuditEntries) >= batch || len(jobIDs) >= batch
	return out, tx.Commit()
}

func isMissingTable(err error) bool {
	return err != nil && (err == sql.ErrNoRows || len(err.Error()) > 0 && (containsStr(err.Error(), "no such table")))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

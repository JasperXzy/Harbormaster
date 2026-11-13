package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type ProbePruneResult struct {
	Payloads, HistoryRows int
	More                  bool
}

// PruneProbeHistory removes terminal latency measurements, never active work,
// trace data, tokens, task identities or the alert progress watermark. Fingerprints
// retain authenticated duplicate ACKs without keeping the expired measurements.
func (s *Store) PruneProbeHistory(ctx context.Context, now time.Time, days, limit int) (ProbePruneResult, error) {
	var out ProbePruneResult
	if days < 1 || days > 3650 || limit < 1 || limit > 1000 || now.IsZero() {
		return out, errors.New("invalid_probe_retention")
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT p.job_id,p.result_json FROM latency_probe_retention q
 JOIN network_probe_job p ON p.job_id=q.job_id JOIN job j ON j.id=q.job_id
 WHERE q.completed_at<? AND j.finished_at=q.completed_at AND j.type='network_probe'
 AND j.status IN ('succeeded','failed') AND p.action='probe' AND p.result_pruned_at IS NULL
 ORDER BY q.completed_at,q.job_id LIMIT ?`, timestamp(cutoff), limit)
	if err != nil {
		return out, err
	}
	type payload struct {
		id  string
		raw sql.NullString
	}
	items := []payload{}
	for rows.Next() {
		var p payload
		if err = rows.Scan(&p.id, &p.raw); err != nil {
			rows.Close()
			return out, err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	marker, _ := json.Marshal(struct {
		Action  string    `json:"action"`
		Expired bool      `json:"result_expired"`
		At      time.Time `json:"result_expired_at"`
	}{"probe", true, now.UTC()})
	for _, p := range items {
		if p.raw.Valid {
			digest := sha256.Sum256([]byte(p.raw.String))
			if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET result_json=NULL,result_digest=?,result_pruned_at=? WHERE job_id=?", digest[:], timestamp(now), p.id); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE job SET result_json=? WHERE id=?", string(marker), p.id); err != nil {
				return out, err
			}
			out.Payloads++
		} else {
			if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET result_pruned_at=? WHERE job_id=?", timestamp(now), p.id); err != nil {
				return out, err
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM latency_probe_retention WHERE job_id=?", p.id); err != nil {
			return out, err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT r.seq FROM probe_plan_run r WHERE r.created_at<? AND
 (r.job_id IS NULL OR EXISTS(SELECT 1 FROM job j JOIN network_probe_job p ON p.job_id=j.id
 WHERE j.id=r.job_id AND j.type='network_probe' AND p.action='probe' AND p.result_pruned_at IS NOT NULL
 AND j.status IN ('succeeded','failed') AND j.finished_at<?)) ORDER BY r.created_at,r.seq LIMIT ?`, timestamp(cutoff), timestamp(cutoff), limit)
	if err != nil {
		return ProbePruneResult{}, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return ProbePruneResult{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ProbePruneResult{}, err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, "DELETE FROM probe_plan_run WHERE seq=?", id); err != nil {
			return ProbePruneResult{}, err
		}
	}
	out.HistoryRows = len(ids)
	out.More = len(items) == limit || len(ids) == limit
	if err = tx.Commit(); err != nil {
		return ProbePruneResult{}, err
	}
	return out, nil
}

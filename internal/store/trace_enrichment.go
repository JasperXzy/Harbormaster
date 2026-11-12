package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

// Enrichment has its own lease; it never reclaims the network measurement job
// or modifies canonical agent reports (which are used for duplicate ACKs).
type TraceEnrichmentClaim struct {
	TraceID, Owner, RequestJSON, ResultJSON string
	ExpiresAt                               time.Time
}

func (s *Store) ClaimTraceEnrichment(ctx context.Context, now time.Time) (TraceEnrichmentClaim, error) {
	var c TraceEnrichmentClaim
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT e.trace_seq,p.request_json,coalesce(p.result_json,'')
 FROM trace_enrichment e JOIN network_trace_run r ON r.seq=e.trace_seq JOIN network_probe_job p ON p.job_id=r.job_id
 WHERE (e.state='queued' OR (e.state='running' AND e.lease_until<=?))
 AND NOT EXISTS (SELECT 1 FROM trace_enrichment WHERE state='running' AND lease_until>?)
 ORDER BY e.trace_seq LIMIT 1`, now.Unix(), now.Unix()).Scan(&seq, &c.RequestJSON, &c.ResultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.TraceID = strconv.FormatInt(seq, 10)
	c.Owner = model.NewID("enrich_")
	c.ExpiresAt = now.Add(45 * time.Second)
	_, err = tx.ExecContext(ctx, "UPDATE trace_enrichment SET state='running',updated_at=?,lease_owner=?,lease_until=?,error_code=NULL WHERE trace_seq=?", timestamp(now), c.Owner, c.ExpiresAt.Unix(), seq)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *Store) FinishTraceEnrichment(ctx context.Context, c TraceEnrichmentClaim, result *model.TraceEnrichment, code string, now time.Time) error {
	state := "failed"
	var raw, failure any
	if result != nil {
		if code != "" || (result.Status != "complete" && result.Status != "partial") {
			return netprobe.ErrInvalid
		}
		data, err := json.Marshal(result)
		if err != nil {
			return netprobe.ErrInvalid
		}
		raw, state = string(data), result.Status
	} else {
		if code != "trace_enrichment_invalid" {
			return netprobe.ErrInvalid
		}
		failure = code
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE trace_enrichment SET state=?,updated_at=?,result_json=?,error_code=?,lease_owner=NULL,lease_until=0
 WHERE trace_seq=? AND state='running' AND lease_owner=? AND lease_until>?`, state, timestamp(now), raw, failure, c.TraceID, c.Owner, now.Unix())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		err = ErrLeaseLost
	}
	return err
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type ServerProbeClaim struct {
	Job       model.Job
	Request   netprobe.Request
	ExpiresAt time.Time
}
type ServerProbeResult struct {
	Result    netprobe.Result `json:"result"`
	ErrorCode *string         `json:"error_code"`
}

func (s *Store) EnqueueServerProbe(ctx context.Context, key string, request netprobe.Request, now time.Time) (model.Job, error) {
	if !probeKey.MatchString(key) || request.Validate() != nil {
		return model.Job{}, netprobe.ErrInvalid
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return model.Job{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return model.Job{}, err
	}
	var oldID, kind, oldRaw string
	err = tx.QueryRowContext(ctx, "SELECT job_id,point_kind,request_json FROM network_probe_job WHERE request_key=?", key).Scan(&oldID, &kind, &oldRaw)
	if err == nil {
		if kind != "server" || oldRaw != string(raw) {
			return model.Job{}, ErrConflict
		}
		j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", oldID))
		if err != nil {
			return j, err
		}
		return j, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM network_probe_job p JOIN job j ON j.id=p.job_id WHERE p.point_kind='server' AND j.status IN ('queued','running')").Scan(&count); err != nil {
		return model.Job{}, err
	}
	if count >= 100 {
		return model.Job{}, ErrProbeBusy
	}
	id := model.NewID("job_")
	if _, err = tx.ExecContext(ctx, "INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source) VALUES(?,'network_probe',?,'[]','queued',?,?,?)", id, "probe:"+key, timestamp(now), now.Unix(), model.JobTrigger(ctx)); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO network_probe_job(job_id,request_key,point_kind,request_json,queued_until) VALUES(?,?,'server',?,?)", id, key, string(raw), now.Add(5*time.Minute).Unix()); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) ClaimServerProbe(ctx context.Context, now time.Time) (ServerProbeClaim, error) {
	var claim ServerProbeClaim
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return claim, err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return claim, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM network_probe_job p JOIN job j ON j.id=p.job_id WHERE p.point_kind='server' AND j.status='running'").Scan(&count); err != nil {
		return claim, err
	}
	if count > 0 {
		if err = tx.Commit(); err != nil {
			return claim, err
		}
		return claim, ErrNotFound
	}
	var id, raw string
	err = tx.QueryRowContext(ctx, `SELECT j.id,p.request_json FROM network_probe_job p JOIN job j ON j.id=p.job_id JOIN job_order o ON o.job_id=j.id WHERE p.point_kind='server' AND j.status='queued' ORDER BY o.seq LIMIT 1`).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return claim, err
		}
		return claim, ErrNotFound
	}
	if err != nil {
		return claim, err
	}
	if json.Unmarshal([]byte(raw), &claim.Request) != nil || claim.Request.Validate() != nil {
		return claim, netprobe.ErrInvalid
	}
	owner := model.NewID("probe_")
	until := now.Add(5 * time.Minute).Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=?,lease_owner=?,lease_until=?,attempts=1 WHERE id=?", timestamp(now), owner, until, id); err != nil {
		return claim, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET claimed_by=? WHERE job_id=?", owner, id); err != nil {
		return claim, err
	}
	claim.ExpiresAt = time.Unix(until, 0).UTC()
	claim.Job, err = readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return claim, err
	}
	return claim, tx.Commit()
}
func (s *Store) AuthorizeServerProbe(ctx context.Context, claim ServerProbeClaim, now time.Time) error {
	raw, err := json.Marshal(claim.Request)
	if err != nil {
		return netprobe.ErrInvalid
	}
	var count int
	err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM job j JOIN network_probe_job p ON p.job_id=j.id WHERE p.point_kind='server' AND p.request_json=? AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, string(raw), claim.Job.ID, claim.Job.Owner, now.Unix()).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) FinishServerProbe(ctx context.Context, claim ServerProbeClaim, result netprobe.Result, code string, now time.Time) error {
	var failure *string
	if code != "" {
		failure = &code
	}
	if err := netprobe.ValidateResult(claim.Request, &result, failure, now); err != nil {
		return err
	}
	if claim.Job.StartedAt == nil || result.StartedAt.Before(claim.Job.StartedAt.Add(-2*time.Minute)) {
		return netprobe.ErrInvalid
	}
	raw, err := json.Marshal(ServerProbeResult{Result: result, ErrorCode: failure})
	if err != nil {
		return netprobe.ErrInvalid
	}
	requestRaw, err := json.Marshal(claim.Request)
	if err != nil {
		return netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM job j JOIN network_probe_job p ON p.job_id=j.id WHERE p.point_kind='server' AND p.request_json=? AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, string(requestRaw), claim.Job.ID, claim.Job.Owner, now.Unix()).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET result_json=? WHERE job_id=?", string(raw), claim.Job.ID); err != nil {
		return err
	}
	status := "succeeded"
	if failure != nil {
		status = "failed"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status=?,error_code=?,finished_at=?,lease_owner=NULL,lease_until=0 WHERE id=?", status, failure, timestamp(now), claim.Job.ID); err != nil {
		return err
	}
	return tx.Commit()
}

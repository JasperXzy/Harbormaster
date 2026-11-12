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
	return s.enqueueServerNetwork(ctx, key, "probe", request, now)
}
func (s *Store) enqueueServerNetwork(ctx context.Context, key, action string, request networkRequest, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return model.Job{}, err
	}
	j, err := enqueueServerNetwork(ctx, tx, key, action, request, now)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func enqueueServerProbe(ctx context.Context, tx *sql.Tx, key string, request netprobe.Request, now time.Time) (model.Job, error) {
	return enqueueServerNetwork(ctx, tx, key, "probe", request, now)
}
func enqueueServerNetwork(ctx context.Context, tx *sql.Tx, key, action string, request networkRequest, now time.Time) (model.Job, error) {
	if !probeKey.MatchString(key) || request.Validate() != nil {
		return model.Job{}, netprobe.ErrInvalid
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return model.Job{}, err
	}
	var oldID, kind, oldRaw, oldAction string
	err = tx.QueryRowContext(ctx, "SELECT job_id,point_kind,request_json,action FROM network_probe_job WHERE request_key=?", key).Scan(&oldID, &kind, &oldRaw, &oldAction)
	if err == nil {
		if kind != "server" || oldRaw != string(raw) || oldAction != action {
			return model.Job{}, ErrConflict
		}
		j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", oldID))
		if err != nil {
			return j, err
		}
		return j, nil
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
	if _, err = tx.ExecContext(ctx, "INSERT INTO network_probe_job(job_id,request_key,point_kind,request_json,queued_until,action) VALUES(?,?,'server',?,?,?)", id, key, string(raw), now.Add(5*time.Minute).Unix(), action); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	return j, nil
}
func (s *Store) ClaimServerProbe(ctx context.Context, now time.Time) (ServerProbeClaim, error) {
	claimed, err := s.claimServerNetwork(ctx, "probe", now)
	claim := ServerProbeClaim{Job: claimed.Job, ExpiresAt: claimed.ExpiresAt}
	if err == nil && json.Unmarshal([]byte(claimed.RequestRaw), &claim.Request) != nil {
		return claim, netprobe.ErrInvalid
	}
	return claim, err
}

type serverNetworkClaim struct {
	Job        model.Job
	RequestRaw string
	ExpiresAt  time.Time
}

func (s *Store) claimServerNetwork(ctx context.Context, action string, now time.Time) (serverNetworkClaim, error) {
	var claim serverNetworkClaim
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
	err = tx.QueryRowContext(ctx, `SELECT j.id,p.request_json FROM network_probe_job p JOIN job j ON j.id=p.job_id JOIN job_order o ON o.job_id=j.id WHERE p.point_kind='server' AND p.action=? AND j.status='queued' ORDER BY o.seq LIMIT 1`, action).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return claim, err
		}
		return claim, ErrNotFound
	}
	if err != nil {
		return claim, err
	}
	var request networkRequest
	if action == "trace" {
		request = &netprobe.TraceRequest{}
	} else {
		request = &netprobe.Request{}
	}
	if json.Unmarshal([]byte(raw), request) != nil || request.Validate() != nil {
		return claim, netprobe.ErrInvalid
	}
	claim.RequestRaw = raw
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
	return s.authorizeServerNetwork(ctx, claim.Job, claim.Request, "probe", now)
}
func (s *Store) authorizeServerNetwork(ctx context.Context, job model.Job, request networkRequest, action string, now time.Time) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return netprobe.ErrInvalid
	}
	var count int
	err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM job j JOIN network_probe_job p ON p.job_id=j.id WHERE p.point_kind='server' AND p.action=? AND p.request_json=? AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, action, string(raw), job.ID, job.Owner, now.Unix()).Scan(&count)
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
	return s.finishServerNetwork(ctx, claim.Job, claim.Request, "probe", raw, failure, now)
}
func (s *Store) finishServerNetwork(ctx context.Context, job model.Job, request networkRequest, action string, raw []byte, failure *string, now time.Time) error {
	requestRaw, err := json.Marshal(request)
	if err != nil {
		return netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM job j JOIN network_probe_job p ON p.job_id=j.id WHERE p.point_kind='server' AND p.action=? AND p.request_json=? AND j.id=? AND j.status='running' AND j.lease_owner=? AND j.lease_until>?`, action, string(requestRaw), job.ID, job.Owner, now.Unix()).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET result_json=? WHERE job_id=?", string(raw), job.ID); err != nil {
		return err
	}
	if action == "trace" {
		if err = saveTraceOutcome(ctx, tx, job.ID, string(raw), now); err != nil {
			return err
		}
	}
	status := "succeeded"
	if failure != nil {
		status = "failed"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status=?,error_code=?,finished_at=?,lease_owner=NULL,lease_until=0 WHERE id=?", status, failure, timestamp(now), job.ID); err != nil {
		return err
	}
	if action == "probe" {
		if err = checkProbeResultAlerts(ctx, tx, job.ID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

var probeKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var ErrProbeBusy = errors.New("probe_queue_full")

// Only this typed entry point can create network_probe jobs. Collection workers
// cannot claim, renew, finish or retry them.
func (s *Store) EnqueueAgentProbe(ctx context.Context, instanceID, key string, request netprobe.Request, now time.Time) (model.Job, error) {
	return s.enqueueAgentNetwork(ctx, instanceID, key, "probe", request, now)
}
func (s *Store) EnqueueAgentTrace(ctx context.Context, instanceID, key string, request netprobe.TraceRequest, now time.Time) (model.Job, error) {
	return s.enqueueAgentNetwork(ctx, instanceID, key, "trace", request, now)
}

type networkRequest interface{ Validate() error }

func (s *Store) enqueueAgentNetwork(ctx context.Context, instanceID, key, action string, request networkRequest, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return model.Job{}, err
	}
	j, err := enqueueAgentNetwork(ctx, tx, instanceID, key, action, request, now)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func enqueueAgentProbe(ctx context.Context, tx *sql.Tx, instanceID, key string, request netprobe.Request, now time.Time) (model.Job, error) {
	return enqueueAgentNetwork(ctx, tx, instanceID, key, "probe", request, now)
}
func enqueueAgentNetwork(ctx context.Context, tx *sql.Tx, instanceID, key, action string, request networkRequest, now time.Time) (model.Job, error) {
	if !probeKey.MatchString(key) || request.Validate() != nil {
		return model.Job{}, netprobe.ErrInvalid
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return model.Job{}, err
	}
	var agentID, accountID string
	var version int64
	var token []byte
	err = tx.QueryRowContext(ctx, `SELECT g.id,i.account_id,a.credential_version,g.token_hash FROM instance i JOIN agent g ON g.instance_id=i.id JOIN account_settings a ON a.account_id=i.account_id WHERE i.id=? AND g.revoked_at IS NULL AND a.enabled=1 AND EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1)`, instanceID).Scan(&agentID, &accountID, &version, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrAccountDisabled
	}
	if err != nil {
		return model.Job{}, err
	}
	var oldID, oldInstance, oldRaw, oldAction string
	err = tx.QueryRowContext(ctx, "SELECT job_id,coalesce(instance_id,''),request_json,action FROM network_probe_job WHERE request_key=?", key).Scan(&oldID, &oldInstance, &oldRaw, &oldAction)
	if err == nil {
		if oldInstance != instanceID || oldRaw != string(raw) || oldAction != action {
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
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM network_probe_job p JOIN job j ON j.id=p.job_id WHERE p.agent_id=? AND j.status IN ('queued','running')", agentID).Scan(&count); err != nil {
		return model.Job{}, err
	}
	if count >= 100 {
		return model.Job{}, ErrProbeBusy
	}
	id := model.NewID("job_")
	ids, _ := json.Marshal([]string{accountID})
	if _, err = tx.ExecContext(ctx, "INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source,instance_id) VALUES(?,'network_probe',?,?,'queued',?,?,?,?)", id, "probe:"+key, string(ids), timestamp(now), now.Unix(), model.JobTrigger(ctx), instanceID); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO network_probe_job(job_id,request_key,instance_id,agent_id,token_hash,credential_version,request_json,queued_until,action) VALUES(?,?,?,?,?,?,?,?,?)", id, key, instanceID, agentID, token, version, string(raw), now.Add(5*time.Minute).Unix(), action); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	return j, nil
}
func authenticateProbeAgent(ctx context.Context, tx *sql.Tx, token, agentID string) error {
	if len(token) < 16 || len(token) > 256 {
		return ErrUnauthorized
	}
	h := sha256.Sum256([]byte(token))
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT a.enabled=1 AND EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1) FROM agent g JOIN instance i ON i.id=g.instance_id JOIN account_settings a ON a.account_id=i.account_id WHERE g.id=? AND g.token_hash=? AND g.revoked_at IS NULL`, agentID, h[:]).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrAccountDisabled
	}
	return nil
}
func expireNetworkProbes(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='probe_binding_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE type='network_probe' AND status IN ('queued','running') AND id IN (
 SELECT p.job_id FROM network_probe_job p JOIN instance i ON i.id=p.instance_id JOIN agent g ON g.id=p.agent_id JOIN account_settings a ON a.account_id=i.account_id WHERE g.revoked_at IS NOT NULL OR g.token_hash<>p.token_hash OR a.enabled<>1 OR a.credential_version<>p.credential_version OR NOT EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1))`, timestamp(now))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='probe_expired',finished_at=?,lease_owner=NULL,lease_until=0 WHERE type='network_probe' AND ((status='running' AND lease_until<=?) OR (status='queued' AND id IN (SELECT job_id FROM network_probe_job WHERE queued_until<=?)))`, timestamp(now), now.Unix(), now.Unix())
	if err != nil {
		return err
	}
	return validateQueuedProbeTargets(ctx, tx, now)
}
func (s *Store) ExpireNetworkProbes(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ClaimAgentProbe(ctx context.Context, token string, in agentproto.ProbeClaim, now time.Time) (agentproto.ProbeClaimed, error) {
	claimed, err := s.claimAgentNetwork(ctx, token, in, "probe", now)
	out := agentproto.ProbeClaimed{ServerTime: claimed.ServerTime}
	if err == nil && claimed.Task != nil {
		t := claimed.Task
		out.Task = &agentproto.ProbeTask{ID: t.ID, Owner: t.Owner, ExpiresAt: t.ExpiresAt}
		if json.Unmarshal([]byte(t.Raw), &out.Task.Request) != nil {
			return out, netprobe.ErrInvalid
		}
	}
	return out, err
}
func (s *Store) ClaimAgentTrace(ctx context.Context, token string, in agentproto.ProbeClaim, now time.Time) (agentproto.TraceClaimed, error) {
	claimed, err := s.claimAgentNetwork(ctx, token, in, "trace", now)
	out := agentproto.TraceClaimed{ServerTime: claimed.ServerTime}
	if err == nil && claimed.Task != nil {
		t := claimed.Task
		out.Task = &agentproto.TraceTask{ID: t.ID, Owner: t.Owner, ExpiresAt: t.ExpiresAt}
		if json.Unmarshal([]byte(t.Raw), &out.Task.Request) != nil {
			return out, netprobe.ErrInvalid
		}
	}
	return out, err
}

type agentNetworkTask struct {
	ID, Owner, Raw string
	ExpiresAt      time.Time
}
type agentNetworkClaim struct {
	Task       *agentNetworkTask
	ServerTime time.Time
}

func (s *Store) claimAgentNetwork(ctx context.Context, token string, in agentproto.ProbeClaim, action string, now time.Time) (agentNetworkClaim, error) {
	out := agentNetworkClaim{ServerTime: now.UTC()}
	if !in.Valid() {
		return out, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = authenticateProbeAgent(ctx, tx, token, in.AgentID); err != nil {
		return out, err
	}
	hash := sha256.Sum256([]byte(token))
	contactTable := "agent_probe_contact"
	if action == "trace" {
		contactTable = "agent_trace_contact"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+contactTable+"(agent_id,token_hash,last_poll_at) VALUES(?,?,?) ON CONFLICT(agent_id) DO UPDATE SET token_hash=excluded.token_hash,last_poll_at=excluded.last_poll_at", in.AgentID, hash[:], timestamp(now)); err != nil {
		return out, err
	}
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return out, err
	}
	var id, owner, raw, activeAction string
	var until int64
	err = tx.QueryRowContext(ctx, `SELECT j.id,j.lease_owner,j.lease_until,p.request_json,p.action FROM job j JOIN network_probe_job p ON p.job_id=j.id WHERE p.agent_id=? AND j.status='running' LIMIT 1`, in.AgentID).Scan(&id, &owner, &until, &raw, &activeAction)
	if err == nil {
		if owner != in.Owner || activeAction != action {
			return out, tx.Commit()
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT j.id,p.request_json FROM job j JOIN network_probe_job p ON p.job_id=j.id JOIN job_order o ON o.job_id=j.id WHERE p.agent_id=? AND p.action=? AND j.status='queued' ORDER BY o.seq LIMIT 1`, in.AgentID, action).Scan(&id, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return out, tx.Commit()
		}
		if err != nil {
			return out, err
		}
		owner, until = in.Owner, now.Add(5*time.Minute).Unix()
		if _, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=?,lease_owner=?,lease_until=?,attempts=1 WHERE id=?", timestamp(now), owner, until, id); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET claimed_by=? WHERE job_id=?", owner, id); err != nil {
			return out, err
		}
	} else {
		return out, err
	}
	task := &agentNetworkTask{ID: id, Owner: owner, Raw: raw, ExpiresAt: time.Unix(until, 0).UTC()}
	var request networkRequest
	if action == "trace" {
		request = &netprobe.TraceRequest{}
	} else {
		request = &netprobe.Request{}
	}
	if json.Unmarshal([]byte(raw), request) != nil || request.Validate() != nil {
		return out, netprobe.ErrInvalid
	}
	out.Task = task
	return out, tx.Commit()
}
func (s *Store) AcceptProbeResult(ctx context.Context, token string, in agentproto.ProbeReport, now time.Time) (agentproto.ProbeAck, error) {
	return s.acceptAgentNetwork(ctx, token, agentNetworkReport{in.AgentID, in.TaskID, in.Owner, "probe", in.ErrorCode, func(requestRaw string) (string, time.Time, error) {
		var request netprobe.Request
		if json.Unmarshal([]byte(requestRaw), &request) != nil || netprobe.ValidateResult(request, &in.Result, in.ErrorCode, now) != nil {
			return "", time.Time{}, netprobe.ErrInvalid
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return "", time.Time{}, netprobe.ErrInvalid
		}
		return string(raw), in.Result.StartedAt, nil
	}}, now)
}
func (s *Store) AcceptTraceResult(ctx context.Context, token string, in agentproto.TraceReport, now time.Time) (agentproto.ProbeAck, error) {
	return s.acceptAgentNetwork(ctx, token, agentNetworkReport{in.AgentID, in.TaskID, in.Owner, "trace", in.ErrorCode, func(requestRaw string) (string, time.Time, error) {
		var request netprobe.TraceRequest
		if json.Unmarshal([]byte(requestRaw), &request) != nil || netprobe.ValidateTraceResult(request, &in.Result, in.ErrorCode, now) != nil {
			return "", time.Time{}, netprobe.ErrInvalid
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return "", time.Time{}, netprobe.ErrInvalid
		}
		return string(raw), in.Result.StartedAt, nil
	}}, now)
}

type agentNetworkReport struct {
	AgentID, TaskID, Owner, Action string
	ErrorCode                      *string
	canonicalize                   func(string) (string, time.Time, error)
}

func (s *Store) acceptAgentNetwork(ctx context.Context, token string, in agentNetworkReport, now time.Time) (agentproto.ProbeAck, error) {
	ack := agentproto.ProbeAck{TaskID: in.TaskID}
	if !(agentproto.ProbeClaim{AgentID: in.AgentID, Owner: in.Owner}).Valid() || len(in.TaskID) > 128 || in.TaskID == "" {
		return ack, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ack, err
	}
	defer tx.Rollback()
	if err = authenticateProbeAgent(ctx, tx, token, in.AgentID); err != nil {
		return ack, err
	}
	var requestRaw, agentID, owner, status string
	var existing, started sql.NullString
	var until int64
	var binding []byte
	var version, currentVersion int64
	err = tx.QueryRowContext(ctx, `SELECT p.request_json,p.agent_id,coalesce(p.claimed_by,''),p.token_hash,p.credential_version,a.credential_version,j.status,j.started_at,j.lease_until,p.result_json FROM network_probe_job p JOIN job j ON j.id=p.job_id JOIN instance i ON i.id=p.instance_id JOIN account_settings a ON a.account_id=i.account_id WHERE p.job_id=? AND p.action=?`, in.TaskID, in.Action).Scan(&requestRaw, &agentID, &owner, &binding, &version, &currentVersion, &status, &started, &until, &existing)
	if errors.Is(err, sql.ErrNoRows) {
		return ack, ErrNotFound
	}
	if err != nil {
		return ack, err
	}
	h := sha256.Sum256([]byte(token))
	if agentID != in.AgentID || owner != in.Owner || !bytes.Equal(binding, h[:]) || version != currentVersion {
		return ack, ErrUnauthorized
	}
	raw, sampleStart, err := in.canonicalize(requestRaw)
	if err != nil {
		return ack, err
	}
	start, err := time.Parse(time.RFC3339Nano, started.String)
	if err != nil || sampleStart.Before(start.Add(-2*time.Minute)) {
		return ack, netprobe.ErrInvalid
	}
	if existing.Valid {
		if existing.String != string(raw) {
			return ack, ErrConflict
		}
		ack.Duplicate = true
		return ack, tx.Commit()
	}
	if status != "running" || until <= now.Unix() {
		return ack, ErrLeaseLost
	}
	if _, err = tx.ExecContext(ctx, "UPDATE network_probe_job SET result_json=? WHERE job_id=?", string(raw), in.TaskID); err != nil {
		return ack, err
	}
	if in.Action == "trace" {
		if err = saveTraceOutcome(ctx, tx, in.TaskID, raw); err != nil {
			return ack, err
		}
	}
	state := "succeeded"
	var code any
	if in.ErrorCode != nil {
		state = "failed"
		code = *in.ErrorCode
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status=?,error_code=?,finished_at=?,lease_owner=NULL,lease_until=0 WHERE id=?", state, code, timestamp(now), in.TaskID); err != nil {
		return ack, err
	}
	if in.Action == "probe" {
		if err = checkProbeResultAlerts(ctx, tx, in.TaskID, now); err != nil {
			return ack, err
		}
	}
	return ack, tx.Commit()
}

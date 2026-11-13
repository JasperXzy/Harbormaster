package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrPowerEventConflict = errors.New("idempotency_key_conflict")
var ErrPowerBusy = errors.New("instance_power_busy")
var ErrPowerParameters = errors.New("invalid_power_parameters")
var ErrPowerDispatched = errors.New("power_already_dispatched")

// PowerOperation is a private worker record. Credentials are never persisted in
// jobs; the account revision fences configuration changes before dispatch.
type PowerOperation struct {
	Job                                                  model.Job
	AccountID, InstanceID, CloudInstanceID, Kind, Region string
	AccountRevision                                      int64
	Action, StopMode, DispatchOutcome                    string
	DispatchStartedAt                                    *time.Time
}

const powerResultSQL = `(SELECT json_object('action',p.action,'stop_mode',p.stop_mode,'dispatch_outcome',p.dispatch_outcome,'baseline',json(p.baseline_json),'observation',json(p.observation_json),'confirmation_basis',p.confirmation_basis) FROM instance_power_job p WHERE p.job_id=job.id)`

func readPower(ctx context.Context, q queryRower, id string) (PowerOperation, error) {
	var p PowerOperation
	j, err := readJob(q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=? AND type='instance_power'", id))
	if err != nil {
		return p, err
	}
	p.Job = j
	var mode, started sql.NullString
	err = q.QueryRowContext(ctx, `SELECT i.account_id,i.id,i.cloud_id,i.kind,i.region,p.account_revision,p.action,p.stop_mode,p.dispatch_started_at,p.dispatch_outcome
 FROM instance_power_job p JOIN job j ON j.id=p.job_id JOIN instance i ON i.id=j.instance_id WHERE p.job_id=?`, id).Scan(&p.AccountID, &p.InstanceID, &p.CloudInstanceID, &p.Kind, &p.Region, &p.AccountRevision, &p.Action, &mode, &started, &p.DispatchOutcome)
	if err != nil {
		return p, err
	}
	p.StopMode = mode.String
	if started.Valid {
		v, e := time.Parse(time.RFC3339Nano, started.String)
		if e != nil {
			return p, e
		}
		p.DispatchStartedAt = &v
	}
	return p, nil
}

func (s *Store) PowerOperation(ctx context.Context, id string) (PowerOperation, error) {
	return readPower(ctx, s.DB, id)
}

func (s *Store) EnqueuePowerJob(ctx context.Context, instanceID, action, stopMode, eventKey string, now time.Time) (model.Job, error) {
	if !model.ValidPowerEventKey(eventKey) {
		return model.Job{}, ErrPowerParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	keyID := model.APIKeyPrincipal(ctx)
	principal := "panel"
	if keyID != "" {
		if err = requireControlAPIKey(ctx, tx, keyID); err != nil {
			return model.Job{}, err
		}
		principal = "api:" + keyID
		ctx = model.WithJobTrigger(ctx, "api")
	}
	// Event keys outlive completion and restore. A transport retry must not
	// create a second reboot or overwrite a newer user intent.
	var previousID string
	err = tx.QueryRowContext(ctx, "SELECT job_id FROM instance_power_request WHERE principal_id=? AND event_key=?", principal, eventKey).Scan(&previousID)
	if err == nil {
		previous, e := readPower(ctx, tx, previousID)
		if e != nil {
			return model.Job{}, e
		}
		if previous.InstanceID != instanceID || previous.Action != action || previous.StopMode != stopMode {
			return model.Job{}, ErrPowerEventConflict
		}
		return previous.Job, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	var accountID, kind, region string
	err = tx.QueryRowContext(ctx, `SELECT i.account_id,i.kind,i.region FROM instance i JOIN resource_snapshot r ON r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.region=i.region WHERE i.id=? AND r.present=1`, instanceID).Scan(&accountID, &kind, &region)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrNotFound
	}
	if err != nil {
		return model.Job{}, err
	}
	if !model.ValidPowerAction(kind, action, stopMode) {
		return model.Job{}, ErrPowerParameters
	}
	a, err := loadAccount(ctx, tx, accountID)
	if err != nil {
		return model.Job{}, err
	}
	if !a.Enabled {
		return model.Job{}, ErrAccountDisabled
	}
	if !slices.Contains(a.RegionIDs, region) {
		return model.Job{}, ErrAccountChanged
	}
	var releasing int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM job WHERE type='instance_release' AND instance_id=? AND status IN ('queued','running')`, instanceID).Scan(&releasing); err != nil {
		return model.Job{}, err
	}
	if releasing != 0 {
		return model.Job{}, ErrPowerBusy
	}
	var activeID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM job WHERE type='instance_power' AND instance_id=? AND status IN ('queued','running')", instanceID).Scan(&activeID)
	if err == nil {
		active, e := readPower(ctx, tx, activeID)
		if e != nil {
			return model.Job{}, e
		}
		var owner string
		if e = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT key_id FROM api_key_power_job WHERE job_id=?),'')", active.Job.ID).Scan(&owner); e != nil {
			return model.Job{}, e
		}
		if owner != keyID {
			return model.Job{}, ErrPowerBusy
		}
		if active.Action != action || active.StopMode != stopMode || active.AccountRevision != a.Revision {
			return model.Job{}, ErrPowerBusy
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO instance_power_request(principal_id,event_key,job_id) VALUES(?,?,?)", principal, eventKey, active.Job.ID); e != nil {
			return model.Job{}, e
		}
		return active.Job, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	id := model.NewID("job_")
	ids, _ := json.Marshal([]string{accountID})
	var mode *string
	if stopMode != "" {
		mode = &stopMode
	}
	result, _ := json.Marshal(model.PowerJobResult{Action: action, StopMode: mode, DispatchOutcome: "not_sent"})
	_, err = tx.ExecContext(ctx, `INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source,instance_id,result_json) VALUES(?,'instance_power',?,?,'queued',?,?,?,?,?)`, id, "power:"+instanceID, string(ids), timestamp(now), now.Unix(), model.JobTrigger(ctx), instanceID, string(result))
	if err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO instance_power_job(job_id,account_revision,action,stop_mode) VALUES(?,?,?,?)", id, a.Revision, action, mode); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO instance_power_request(principal_id,event_key,job_id) VALUES(?,?,?)", principal, eventKey, id); err != nil {
		return model.Job{}, err
	}
	if keyID != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO api_key_power_job(job_id,key_id) VALUES(?,?)", id, keyID); err != nil {
			return model.Job{}, err
		}
	}
	desired := "running"
	if action == "stop" {
		desired = "stopped"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance SET desired_state=? WHERE id=?", desired, instanceID); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO instance_intent(instance_id,job_id,updated_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET job_id=excluded.job_id,updated_at=excluded.updated_at", instanceID, id, timestamp(now)); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_power_requested", TargetType: "instance", TargetID: instanceID, AccountID: accountID, JobID: id, Details: model.AuditDetails{ChangedFields: []string{"desired_state"}, Operation: action, StopMode: stopMode, Status: "queued", DispatchOutcome: "not_sent"}}, now); err != nil {
		return j, err
	}
	return j, tx.Commit()
}

// ClaimPowerJob only retries a lease that expired BEFORE the dispatch marker.
// An accepted response needs observation, not resending; a lost response is
// uncertain. Neither path may be claimed by another sender after a crash.
func (s *Store) ClaimPowerJob(ctx context.Context, now time.Time) (PowerOperation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PowerOperation{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE instance_power_job SET dispatch_outcome='unknown' WHERE dispatch_outcome='dispatching' AND job_id IN (SELECT id FROM job WHERE type='instance_power' AND status='running' AND lease_until<=?)`, now.Unix())
	if err != nil {
		return PowerOperation{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code=CASE WHEN (SELECT dispatch_outcome FROM instance_power_job WHERE job_id=job.id)='accepted' THEN 'power_confirmation_interrupted' ELSE 'power_outcome_unknown' END,finished_at=?,lease_owner=NULL,lease_until=0,result_json=`+powerResultSQL+` WHERE type='instance_power' AND status='running' AND lease_until<=? AND id IN (SELECT job_id FROM instance_power_job WHERE dispatch_started_at IS NOT NULL)`, timestamp(now), now.Unix())
	if err != nil {
		return PowerOperation{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='retry_exhausted',finished_at=?,lease_owner=NULL,lease_until=0 WHERE type='instance_power' AND attempts>=3 AND (status='queued' OR (status='running' AND lease_until<=?))`, timestamp(now), now.Unix())
	if err != nil {
		return PowerOperation{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM job WHERE type='instance_power' AND ((status='queued' AND available_at<=?) OR (status='running' AND lease_until<=?)) AND id IN (SELECT job_id FROM instance_power_job WHERE dispatch_started_at IS NULL) ORDER BY created_at,id LIMIT 1`, now.Unix(), now.Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if e := tx.Commit(); e != nil {
			return PowerOperation{}, e
		}
		return PowerOperation{}, ErrNotFound
	}
	if err != nil {
		return PowerOperation{}, err
	}
	owner := model.NewID("lease_")
	_, err = tx.ExecContext(ctx, `UPDATE job SET status='running',started_at=coalesce(started_at,?),lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?`, timestamp(now), owner, now.Add(time.Minute).Unix(), id)
	if err != nil {
		return PowerOperation{}, err
	}
	p, err := readPower(ctx, tx, id)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func powerLease(ctx context.Context, tx *sql.Tx, j model.Job, now time.Time) (PowerOperation, error) {
	p, err := readPower(ctx, tx, j.ID)
	if err != nil {
		return p, err
	}
	var valid int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM job WHERE id=? AND type='instance_power' AND status='running' AND lease_owner=? AND lease_until>?", j.ID, j.Owner, now.Unix()).Scan(&valid)
	if err != nil {
		return p, err
	}
	if valid != 1 {
		return p, ErrLeaseLost
	}
	return p, nil
}

// BeginPowerDispatch must commit successfully before making the cloud call.
// Caller-supplied target/action fields are ignored; the persisted record wins.
func (s *Store) BeginPowerDispatch(ctx context.Context, j model.Job, now time.Time) (PowerOperation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PowerOperation{}, err
	}
	defer tx.Rollback()
	p, err := powerLease(ctx, tx, j, now)
	if err != nil {
		return p, err
	}
	if p.DispatchStartedAt != nil {
		return p, ErrPowerDispatched
	}
	var keyID string
	if err = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT key_id FROM api_key_power_job WHERE job_id=?),'')", j.ID).Scan(&keyID); err != nil {
		return p, err
	}
	if keyID != "" {
		if err = requireControlAPIKey(ctx, tx, keyID); err != nil {
			return p, err
		}
	}

	a, err := loadAccount(ctx, tx, p.AccountID)
	if err != nil {
		return p, err
	}
	code := ""
	if !a.Enabled {
		code = "account_disabled"
	} else if a.Revision != p.AccountRevision || !slices.Contains(a.RegionIDs, p.Region) {
		code = "account_changed"
	}
	var present int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM resource_snapshot WHERE id=? AND account_id=? AND kind=? AND region=? AND present=1", p.InstanceID, p.AccountID, p.Kind, p.Region).Scan(&present)
	if err != nil {
		return p, err
	}
	if present != 1 {
		code = "instance_not_found"
	}
	if code != "" {
		_, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',finished_at=?,error_code=?,lease_owner=NULL,lease_until=0 WHERE id=?", timestamp(now), code, j.ID)
		if err != nil {
			return p, err
		}
		if err = tx.Commit(); err != nil {
			return p, err
		}
		return p, map[string]error{"account_disabled": ErrAccountDisabled, "account_changed": ErrAccountChanged, "instance_not_found": ErrNotFound}[code]
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance_power_job SET dispatch_started_at=?,dispatch_outcome='dispatching' WHERE job_id=?", timestamp(now), j.ID); err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+powerResultSQL+" WHERE id=?", j.ID); err != nil {
		return p, err
	}
	p, err = readPower(ctx, tx, j.ID)
	if err != nil {
		return p, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_power_dispatch_started", TargetType: "instance", TargetID: p.InstanceID, AccountID: p.AccountID, JobID: j.ID, Actor: &model.AuditActor{Kind: "system"}, Details: model.AuditDetails{ChangedFields: []string{"dispatch_started_at", "dispatch_outcome"}, Operation: p.Action, StopMode: p.StopMode, Status: "running", DispatchOutcome: "dispatching"}}, now); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// RecordPowerDispatch persists the response without asserting observed success.
// Accepted jobs stay running for a future observation step. All other outcomes
// finish failed, including definitively not-sent requests (no automatic replay).
func (s *Store) RecordPowerDispatch(ctx context.Context, j model.Job, outcome, code string, now time.Time) error {
	if outcome != "accepted" && outcome != "not_sent" && outcome != "rejected" && outcome != "unknown" {
		return ErrPowerParameters
	}
	// A fixed code set prevents SDK/provider text or credentials entering history.
	allowed := map[string][]string{
		"accepted": {""},
		"unknown":  {"power_outcome_unknown"},
		"rejected": {"instance_state_conflict", "instance_not_found", "access_key_invalid", "permission_denied", "aliyun_throttled", "region_unavailable", "power_request_rejected"},
		"not_sent": {"cancelled", "sdk_debug_must_be_disabled", "invalid_site", "invalid_region", "region_not_enabled", "sdk_initialization_failed", "power_request_not_sent", "invalid_instance_kind", "invalid_instance_id", "operation_not_allowed", "invalid_stop_mode", "invalid_power_parameters"},
	}
	if !slices.Contains(allowed[outcome], code) {
		return ErrPowerParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := powerLease(ctx, tx, j, now)
	if err != nil {
		return err
	}
	if p.DispatchStartedAt == nil || p.DispatchOutcome != "dispatching" {
		return ErrPowerDispatched
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance_power_job SET dispatch_outcome=? WHERE job_id=?", outcome, j.ID); err != nil {
		return err
	}
	status := "failed"
	if outcome == "accepted" {
		status = "running"
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_power_dispatch_recorded", TargetType: "instance", TargetID: p.InstanceID, AccountID: p.AccountID, JobID: j.ID, Actor: &model.AuditActor{Kind: "system"}, Details: model.AuditDetails{ChangedFields: []string{"dispatch_outcome"}, Operation: p.Action, StopMode: p.StopMode, Status: status, DispatchOutcome: outcome, ErrorCode: code}}, now); err != nil {
		return err
	}
	if outcome == "accepted" {
		_, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+powerResultSQL+" WHERE id=?", j.ID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',finished_at=?,error_code=?,result_json="+powerResultSQL+",lease_owner=NULL,lease_until=0 WHERE id=?", timestamp(now), code, j.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

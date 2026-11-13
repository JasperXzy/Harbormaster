package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var (
	ErrReleaseConfirmation   = errors.New("release_confirmation_mismatch")
	ErrReleasePreviewChanged = errors.New("release_preview_changed")
	ErrReleaseBusy           = errors.New("instance_release_busy")
	ErrReleaseParameters     = errors.New("invalid_release_parameters")
	ErrReleaseDispatched     = errors.New("release_already_dispatched")
)

type ReleaseOperation struct {
	Job                                            model.Job
	AccountID, InstanceID, CloudInstanceID, Region string
	AccountRevision, CredentialVersion             int64
	ConfirmedName, PreviewToken, DispatchOutcome   string
	PreviewFetchedAt                               time.Time
	Impact                                         model.ReleaseImpact
	DispatchStartedAt                              *time.Time
}

const releaseResultSQL = `(SELECT json_object('action','release','confirmed_name',p.confirmed_name,'preview_fetched_at',p.preview_fetched_at,'dispatch_outcome',p.dispatch_outcome,'observation',json(p.observation_json),'confirmation_basis',p.confirmation_basis) FROM instance_release_job p WHERE p.job_id=job.id)`

func readRelease(ctx context.Context, q queryRower, id string) (ReleaseOperation, error) {
	var p ReleaseOperation
	j, err := readJob(q.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=? AND type='instance_release'", id))
	if err != nil {
		return p, err
	}
	p.Job = j
	var fetched, raw string
	var started sql.NullString
	err = q.QueryRowContext(ctx, `SELECT p.account_id,j.instance_id,p.cloud_instance_id,p.region,p.account_revision,p.credential_version,
 p.confirmed_name,p.preview_token,p.preview_fetched_at,p.impact_json,p.dispatch_started_at,p.dispatch_outcome
 FROM instance_release_job p JOIN job j ON j.id=p.job_id WHERE p.job_id=?`, id).Scan(&p.AccountID, &p.InstanceID, &p.CloudInstanceID, &p.Region, &p.AccountRevision, &p.CredentialVersion, &p.ConfirmedName, &p.PreviewToken, &fetched, &raw, &started, &p.DispatchOutcome)
	if err != nil {
		return p, err
	}
	if p.PreviewFetchedAt, err = time.Parse(time.RFC3339Nano, fetched); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p.Impact); err != nil {
		return p, err
	}
	p.DispatchStartedAt = parseAgentTime(started)
	return p, nil
}

func (s *Store) ReleaseOperation(ctx context.Context, id string) (ReleaseOperation, error) {
	return readRelease(ctx, s.DB, id)
}

// EnqueueReleaseJob validates the exact displayed name and evidence in the
// same transaction as admission. API keys do not grant this destructive scope.
func (s *Store) EnqueueReleaseJob(ctx context.Context, instanceID, name, token, eventKey string, now time.Time) (model.Job, error) {
	if model.APIKeyPrincipal(ctx) != "" {
		return model.Job{}, ErrUnauthorized
	}
	if !model.ValidPowerEventKey(eventKey) || name == "" || len(token) != 64 {
		return model.Job{}, ErrReleaseParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	var previousID string
	err = tx.QueryRowContext(ctx, "SELECT job_id FROM instance_release_request WHERE event_key=?", eventKey).Scan(&previousID)
	if err == nil {
		previous, err := readRelease(ctx, tx, previousID)
		if err != nil {
			return model.Job{}, err
		}
		if previous.InstanceID != instanceID || previous.ConfirmedName != name || previous.PreviewToken != token {
			return model.Job{}, ErrPowerEventConflict
		}
		return previous.Job, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	preview, err := readReleasePreviewTx(ctx, tx, instanceID, now)
	if err != nil {
		return model.Job{}, err
	}
	if preview.Status != "eligible" || preview.Token == nil || *preview.Token != token || preview.Impact == nil {
		return model.Job{}, ErrReleasePreviewChanged
	}
	if preview.Impact.Name != name {
		return model.Job{}, ErrReleaseConfirmation
	}
	a, err := loadAccount(ctx, tx, preview.AccountID)
	if err != nil {
		return model.Job{}, err
	}
	var region string
	if err = tx.QueryRowContext(ctx, "SELECT region FROM instance WHERE id=? AND account_id=? AND kind='ecs'", instanceID, a.ID).Scan(&region); err != nil {
		return model.Job{}, err
	}
	var activeID, activeType string
	err = tx.QueryRowContext(ctx, `SELECT id,type FROM job WHERE instance_id=? AND type IN ('instance_power','instance_release')
 AND status IN ('queued','running') LIMIT 1`, instanceID).Scan(&activeID, &activeType)
	if err == nil {
		if activeType != "instance_release" {
			return model.Job{}, ErrReleaseBusy
		}
		active, err := readRelease(ctx, tx, activeID)
		if err != nil {
			return model.Job{}, err
		}
		if active.PreviewToken != token || active.ConfirmedName != name || active.AccountRevision != a.Revision {
			return model.Job{}, ErrReleaseBusy
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO instance_release_request(event_key,job_id) VALUES(?,?)", eventKey, activeID); err != nil {
			return model.Job{}, err
		}
		return active.Job, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	id := model.NewID("job_")
	ids, _ := json.Marshal([]string{a.ID})
	impact, _ := json.Marshal(preview.Impact)
	result, _ := json.Marshal(model.ReleaseJobResult{Action: "release", ConfirmedName: name, PreviewFetchedAt: *preview.FetchedAt, DispatchOutcome: "not_sent"})
	_, err = tx.ExecContext(ctx, `INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source,instance_id,result_json)
 VALUES(?,'instance_release',?,?,'queued',?,?,?,?,?)`, id, "release:"+instanceID, string(ids), timestamp(now), now.Unix(), model.JobTrigger(ctx), instanceID, string(result))
	if err != nil {
		return model.Job{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO instance_release_job(job_id,account_id,account_revision,credential_version,region,cloud_instance_id,
 confirmed_name,preview_token,preview_fetched_at,impact_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, a.ID, a.Revision, a.CredentialVersion, region, preview.Impact.CloudID, name, token, timestamp(*preview.FetchedAt), string(impact))
	if err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO instance_release_request(event_key,job_id) VALUES(?,?)", eventKey, id); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_release_requested", TargetType: "instance", TargetID: instanceID, AccountID: a.ID, JobID: id, Details: model.AuditDetails{Operation: "release", Status: "queued", DispatchOutcome: "not_sent"}}, now); err != nil {
		return j, err
	}
	return j, tx.Commit()
}

// A lease may be recovered only before the durable dispatch boundary.
func (s *Store) ClaimReleaseJob(ctx context.Context, now time.Time) (ReleaseOperation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReleaseOperation{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE instance_release_job SET dispatch_outcome='unknown' WHERE dispatch_outcome='dispatching'
 AND job_id IN (SELECT id FROM job WHERE type='instance_release' AND status='running' AND lease_until<=?)`, now.Unix()); err != nil {
		return ReleaseOperation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code=CASE WHEN
 (SELECT dispatch_outcome FROM instance_release_job WHERE job_id=job.id)='accepted' THEN 'release_confirmation_interrupted' ELSE 'release_outcome_unknown' END,
 finished_at=?,lease_owner=NULL,lease_until=0,result_json=`+releaseResultSQL+`
 WHERE type='instance_release' AND status='running' AND lease_until<=? AND id IN
 (SELECT job_id FROM instance_release_job WHERE dispatch_started_at IS NOT NULL)`, timestamp(now), now.Unix()); err != nil {
		return ReleaseOperation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='retry_exhausted',finished_at=?,lease_owner=NULL,lease_until=0
 WHERE type='instance_release' AND attempts>=3 AND (status='queued' OR (status='running' AND lease_until<=?))`, timestamp(now), now.Unix()); err != nil {
		return ReleaseOperation{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM job WHERE type='instance_release'
 AND ((status='queued' AND available_at<=?) OR (status='running' AND lease_until<=?))
 AND id IN (SELECT job_id FROM instance_release_job WHERE dispatch_started_at IS NULL) ORDER BY created_at,id LIMIT 1`, now.Unix(), now.Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return ReleaseOperation{}, err
		}
		return ReleaseOperation{}, ErrNotFound
	}
	if err != nil {
		return ReleaseOperation{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE job SET status='running',started_at=coalesce(started_at,?),lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?`, timestamp(now), model.NewID("lease_"), now.Add(time.Minute).Unix(), id)
	if err != nil {
		return ReleaseOperation{}, err
	}
	p, err := readRelease(ctx, tx, id)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func releaseLease(ctx context.Context, tx *sql.Tx, j model.Job, now time.Time) (ReleaseOperation, error) {
	p, err := readRelease(ctx, tx, j.ID)
	if err != nil {
		return p, err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM job WHERE id=? AND type='instance_release'
 AND status='running' AND lease_owner=? AND lease_until>?`, j.ID, j.Owner, now.Unix()).Scan(&n); err != nil {
		return p, err
	}
	if n != 1 {
		return p, ErrLeaseLost
	}
	return p, nil
}

// BeginReleaseDispatch requires a fresh complete read matching the immutable
// reviewed impact. Caller-supplied target identity is never used for dispatch.
func (s *Store) BeginReleaseDispatch(ctx context.Context, j model.Job, fresh model.ReleaseInventoryObservation, now time.Time) (ReleaseOperation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReleaseOperation{}, err
	}
	defer tx.Rollback()
	p, err := releaseLease(ctx, tx, j, now)
	if err != nil {
		return p, err
	}
	if p.DispatchStartedAt != nil {
		return p, ErrReleaseDispatched
	}
	a, err := loadAccount(ctx, tx, p.AccountID)
	if err != nil {
		return p, err
	}
	code := ""
	if !a.Enabled {
		code = "account_disabled"
	} else if a.Revision != p.AccountRevision || a.CredentialVersion != p.CredentialVersion || !slices.Contains(a.RegionIDs, p.Region) {
		code = "account_changed"
	}
	var present int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM resource_snapshot WHERE id=? AND account_id=? AND kind='ecs' AND region=? AND present=1`, p.InstanceID, p.AccountID, p.Region).Scan(&present); err != nil {
		return p, err
	}
	if present != 1 {
		code = "instance_not_found"
	}
	if code == "" {
		if fresh.Error != nil || fresh.Data == nil || !fresh.Data.Valid() || fresh.Region != p.Region || fresh.QueriedAt.IsZero() || now.Before(fresh.QueriedAt) || now.Sub(fresh.QueriedAt) > 30*time.Second {
			code = "release_preflight_unavailable"
		} else {
			impact, reasons := fresh.Data.ImpactFor(p.CloudInstanceID)
			got, _ := json.Marshal(impact)
			expected, _ := json.Marshal(p.Impact)
			if len(reasons) != 0 || impact == nil || !bytes.Equal(got, expected) {
				code = "release_preview_changed"
			}
		}
	}
	if code != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',finished_at=?,error_code=?,lease_owner=NULL,lease_until=0 WHERE id=?`, timestamp(now), code, j.ID); err != nil {
			return p, err
		}
		if err := tx.Commit(); err != nil {
			return p, err
		}
		return p, map[string]error{"account_disabled": ErrAccountDisabled, "account_changed": ErrAccountChanged, "instance_not_found": ErrNotFound, "release_preflight_unavailable": ErrReleaseInventory, "release_preview_changed": ErrReleasePreviewChanged}[code]
	}
	if _, err = tx.ExecContext(ctx, `UPDATE instance_release_job SET dispatch_started_at=?,dispatch_outcome='dispatching' WHERE job_id=?`, timestamp(now), j.ID); err != nil {
		return p, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+releaseResultSQL+" WHERE id=?", j.ID); err != nil {
		return p, err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_release_dispatch_started", TargetType: "instance", TargetID: p.InstanceID, AccountID: p.AccountID, JobID: j.ID, Actor: &model.AuditActor{Kind: "system"}, Details: model.AuditDetails{Operation: "release", Status: "running", DispatchOutcome: "dispatching"}}, now); err != nil {
		return p, err
	}
	p, err = readRelease(ctx, tx, j.ID)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (s *Store) RecordReleaseDispatch(ctx context.Context, j model.Job, outcome, code string, now time.Time) error {
	allowed := map[string][]string{
		"accepted": {""},
		"unknown":  {"release_outcome_unknown"},
		"rejected": {"instance_state_conflict", "instance_not_found", "instance_security_locked", "instance_deletion_protected", "instance_charge_type_unsupported", "instance_dependencies_block_release", "access_key_invalid", "permission_denied", "aliyun_throttled", "region_unavailable", "release_request_rejected"},
		"not_sent": {"cancelled", "sdk_debug_must_be_disabled", "invalid_site", "invalid_region", "region_not_enabled", "sdk_initialization_failed", "release_request_not_sent", "invalid_instance_id"},
	}
	if !slices.Contains(allowed[outcome], code) {
		return ErrReleaseParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := releaseLease(ctx, tx, j, now)
	if err != nil {
		return err
	}
	if p.DispatchStartedAt == nil || p.DispatchOutcome != "dispatching" {
		return ErrReleaseDispatched
	}
	if _, err = tx.ExecContext(ctx, "UPDATE instance_release_job SET dispatch_outcome=? WHERE job_id=?", outcome, j.ID); err != nil {
		return err
	}
	status := "failed"
	if outcome == "accepted" {
		status = "running"
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_release_dispatch_recorded", TargetType: "instance", TargetID: p.InstanceID, AccountID: p.AccountID, JobID: j.ID, Actor: &model.AuditActor{Kind: "system"}, Details: model.AuditDetails{Operation: "release", Status: status, DispatchOutcome: outcome, ErrorCode: code}}, now); err != nil {
		return err
	}
	if outcome == "accepted" {
		_, err = tx.ExecContext(ctx, "UPDATE job SET result_json="+releaseResultSQL+" WHERE id=?", j.ID)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',finished_at=?,error_code=?,result_json="+releaseResultSQL+",lease_owner=NULL,lease_until=0 WHERE id=?", timestamp(now), code, j.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

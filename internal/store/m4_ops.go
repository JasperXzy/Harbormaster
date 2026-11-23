package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var (
	ErrUnmanagedEIPProtected = errors.New("unmanaged_eip_protected")
	ErrEIPRotateUnsupported  = errors.New("eip_rotate_unsupported")
	ErrEIPBusy               = errors.New("instance_eip_busy")
	ErrCreateBusy            = errors.New("instance_create_busy")
)

type EIPJobRecord struct {
	Job                model.Job
	AccountID          string
	AccountRevision    int64
	CredentialVersion  int64
	InstanceID         string
	Region             string
	CloudInstanceID    string
	BandwidthMbps      int
	ChargeType         string
	BandwidthPackageID string
	OldAllocationID    string
	OldIPAddress       string
	NewAllocationID    string
	NewIPAddress       string
	Step               string
}

type CreateJobRecord struct {
	Job               model.Job
	AccountID         string
	AccountRevision   int64
	CredentialVersion int64
	Region            string
	Input             model.ECSCreateInput
	CreatedVPCID      string
	CreatedVSwitchID  string
	CreatedSGID       string
	CreatedCloudID    string
	Step              string
}

func (s *Store) EnqueueEIPRotateJob(ctx context.Context, instanceID string, in model.EIPRotateInput, eventKey string, now time.Time) (model.Job, error) {
	if !model.ValidPowerEventKey(eventKey) {
		return model.Job{}, errors.New("invalid_event_key")
	}
	if len(in.Validate()) > 0 {
		return model.Job{}, errors.New("invalid_eip_rotate_input")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(ctx, "SELECT job_id FROM instance_eip_job WHERE event_key=?", eventKey).Scan(&existingID)
	if err == nil {
		j, rErr := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", existingID))
		if rErr != nil {
			return j, rErr
		}
		if j.InstanceID == nil || *j.InstanceID != instanceID {
			return j, ErrPowerEventConflict
		}
		return j, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}

	var acctID, kind, region, cloudID, raw string
	err = tx.QueryRowContext(ctx, `SELECT i.account_id,i.kind,i.region,i.cloud_id,r.payload_json
 FROM instance i JOIN resource_snapshot r ON r.account_id=i.account_id AND r.id=i.id AND r.kind=i.kind AND r.present=1
 WHERE i.id=?`, instanceID).Scan(&acctID, &kind, &region, &cloudID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrNotFound
	}
	if err != nil {
		return model.Job{}, err
	}
	if kind != "ecs" {
		return model.Job{}, ErrEIPRotateUnsupported
	}
	a, err := loadAccount(ctx, tx, acctID)
	if err != nil {
		return model.Job{}, err
	}
	if !a.Enabled {
		return model.Job{}, ErrAccountDisabled
	}

	var snap struct {
		PublicIPs []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
		} `json:"public_ips"`
	}
	if json.Unmarshal([]byte(raw), &snap) != nil {
		return model.Job{}, errors.New("corrupt_instance_snapshot")
	}
	var oldAllocID, oldIP string
	for _, ip := range snap.PublicIPs {
		if ip.Type == "instance_public_ip" {
			// Fixed/nat instance public IP cannot be replaced via EIP rotation while active
			return model.Job{}, ErrEIPRotateUnsupported
		}
		if ip.Type == "eip" {
			var alloc string
			var bwp sql.NullString
			mErr := tx.QueryRowContext(ctx, `SELECT allocation_id,bandwidth_package_id FROM managed_eip WHERE account_id=? AND region=? AND ip_address=? AND released_at IS NULL`,
				acctID, region, ip.Address).Scan(&alloc, &bwp)
			if errors.Is(mErr, sql.ErrNoRows) {
				// PRD F7.2 strict safety invariant: never detach or release pre-existing EIPs not created by Harbormaster!
				return model.Job{}, ErrUnmanagedEIPProtected
			}
			if mErr != nil {
				return model.Job{}, mErr
			}
			oldAllocID = alloc
			oldIP = ip.Address
			if in.BandwidthPackageID == "" && bwp.Valid {
				in.BandwidthPackageID = bwp.String
			}
		}
	}

	var active int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM job WHERE type='instance_eip_rotate' AND scope=? AND status IN ('queued','running')", "eip:"+instanceID).Scan(&active); err != nil {
		return model.Job{}, err
	}
	if active > 0 {
		return model.Job{}, ErrEIPBusy
	}

	id := model.NewID("job_")
	idsRaw, _ := json.Marshal([]string{acctID})
	trigger := model.JobTrigger(ctx)
	if _, err = tx.ExecContext(ctx, `INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source,instance_id)
 VALUES(?,'instance_eip_rotate',?,?,'queued',?,?,?,?)`,
		id, "eip:"+instanceID, string(idsRaw), timestamp(now), now.Unix(), trigger, instanceID); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_eip_job(job_id,account_id,account_revision,credential_version,instance_id,region,cloud_instance_id,bandwidth_mbps,charge_type,bandwidth_package_id,old_allocation_id,old_ip_address,step,event_key)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'queued',?)`,
		id, acctID, a.Revision, a.CredentialVersion, instanceID, region, cloudID, in.BandwidthMbps, in.InternetChargeType, in.BandwidthPackageID, oldAllocID, oldIP, eventKey); err != nil {
		return model.Job{}, err
	}
	details := model.AuditDetails{
		ChangedFields: []string{"bandwidth_mbps", "internet_charge_type"},
		After:         map[string]any{"bandwidth_mbps": in.BandwidthMbps, "internet_charge_type": in.InternetChargeType, "old_ip": oldIP},
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_eip_rotate_requested", TargetType: "instance", TargetID: instanceID, AccountID: acctID, JobID: id, Details: details}, now); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}

func (s *Store) EnqueueECSCreateJob(ctx context.Context, in model.ECSCreateInput, eventKey string, now time.Time) (model.Job, error) {
	if !model.ValidPowerEventKey(eventKey) {
		return model.Job{}, errors.New("invalid_event_key")
	}
	if len(in.Validate()) > 0 {
		return model.Job{}, errors.New("invalid_ecs_create_input")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(ctx, "SELECT job_id FROM instance_create_job WHERE event_key=?", eventKey).Scan(&existingID)
	if err == nil {
		j, rErr := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", existingID))
		if rErr != nil {
			return j, rErr
		}
		return j, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}

	a, err := loadAccount(ctx, tx, in.AccountID)
	if err != nil {
		return model.Job{}, err
	}
	if !a.Enabled {
		return model.Job{}, ErrAccountDisabled
	}
	if !slices.Contains(a.RegionIDs, in.RegionID) {
		return model.Job{}, errors.New("region_not_enabled")
	}

	id := model.NewID("job_")
	idsRaw, _ := json.Marshal([]string{a.ID})
	cfgRaw, _ := json.Marshal(in)
	trigger := model.JobTrigger(ctx)
	scope := "create:" + id
	if _, err = tx.ExecContext(ctx, `INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source)
 VALUES(?,'instance_create',?,?,'queued',?,?,?)`,
		id, scope, string(idsRaw), timestamp(now), now.Unix(), trigger); err != nil {
		return model.Job{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO instance_create_job(job_id,account_id,account_revision,credential_version,region,config_json,step,event_key)
 VALUES(?,?,?,?,?,?,'queued',?)`,
		id, a.ID, a.Revision, a.CredentialVersion, in.RegionID, string(cfgRaw), eventKey); err != nil {
		return model.Job{}, err
	}
	details := model.AuditDetails{
		ChangedFields: []string{"region_id", "zone_id", "instance_type", "instance_name", "instance_charge_type"},
		After: map[string]any{
			"region_id":            in.RegionID,
			"zone_id":              in.ZoneID,
			"instance_type":        in.InstanceType,
			"instance_name":        strings.TrimSpace(in.InstanceName),
			"instance_charge_type": in.InstanceChargeType,
		},
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "instance_create_requested", TargetType: "account", TargetID: a.ID, AccountID: a.ID, JobID: id, Details: details}, now); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}

func (s *Store) ClaimEIPRotateJob(ctx context.Context, now time.Time) (EIPJobRecord, error) {
	var rec EIPJobRecord
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return rec, err
	}
	defer tx.Rollback()
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+` FROM job WHERE type='instance_eip_rotate' AND status='queued' AND available_at<=? ORDER BY created_at,id LIMIT 1`, now.Unix()))
	if errors.Is(err, ErrNotFound) {
		_ = tx.Commit()
		return rec, ErrNotFound
	}
	if err != nil {
		return rec, err
	}
	owner := model.NewID("eip_lease_")
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=coalesce(started_at,?),lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?",
		timestamp(now), owner, now.Add(2*time.Minute).Unix(), j.ID); err != nil {
		return rec, err
	}
	j.Owner = owner
	j.Status = "running"
	rec.Job = j
	err = tx.QueryRowContext(ctx, `SELECT account_id,account_revision,credential_version,instance_id,region,cloud_instance_id,bandwidth_mbps,charge_type,coalesce(bandwidth_package_id,''),coalesce(old_allocation_id,''),coalesce(old_ip_address,''),coalesce(new_allocation_id,''),coalesce(new_ip_address,''),step
 FROM instance_eip_job WHERE job_id=?`, j.ID).Scan(
		&rec.AccountID, &rec.AccountRevision, &rec.CredentialVersion, &rec.InstanceID, &rec.Region, &rec.CloudInstanceID,
		&rec.BandwidthMbps, &rec.ChargeType, &rec.BandwidthPackageID, &rec.OldAllocationID, &rec.OldIPAddress, &rec.NewAllocationID, &rec.NewIPAddress, &rec.Step,
	)
	if err != nil {
		return rec, err
	}
	return rec, tx.Commit()
}

func (s *Store) RecordManagedEIP(ctx context.Context, jobID, accountID, region, instanceID, allocationID, ipAddr, bwpID string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var bwpVal any
	if bwpID != "" {
		bwpVal = bwpID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_eip(allocation_id,account_id,region,ip_address,created_for_instance_id,bandwidth_package_id,created_at)
 VALUES(?,?,?,?,?,?,?) ON CONFLICT(allocation_id) DO NOTHING`,
		allocationID, accountID, region, ipAddr, instanceID, bwpVal, timestamp(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE instance_eip_job SET new_allocation_id=?,new_ip_address=?,step='allocated' WHERE job_id=?`,
		allocationID, ipAddr, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkManagedEIPReleased(ctx context.Context, allocationID string, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE managed_eip SET released_at=? WHERE allocation_id=?", timestamp(now), allocationID)
	return err
}

func (s *Store) FinishEIPRotateJob(ctx context.Context, rec EIPJobRecord, newAllocID, newIP, errCode string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := "succeeded"
	var errVal, resVal any
	if errCode != "" {
		status = "failed"
		errVal = errCode
	} else {
		raw, _ := json.Marshal(map[string]any{"allocation_id": newAllocID, "ip_address": newIP, "old_ip_address": rec.OldIPAddress})
		resVal = string(raw)
		_, _ = enqueueJob(model.WithJobTrigger(ctx, "system"), tx, "refresh", rec.AccountID, []string{rec.AccountID}, now)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status=?,finished_at=?,error_code=?,result_json=?,lease_owner=NULL,lease_until=0 WHERE id=?`,
		status, timestamp(now), errVal, resVal, rec.Job.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE instance_eip_job SET step=? WHERE job_id=?`, status, rec.Job.ID); err != nil {
		return err
	}
	details := model.AuditDetails{
		ChangedFields: []string{"status"},
		After:         map[string]any{"status": status, "new_ip": newIP, "old_ip": rec.OldIPAddress, "error_code": errCode},
	}
	if err = appendAudit(ctx, tx, auditEvent{
		Action: "instance_eip_rotate_finished", TargetType: "instance", TargetID: rec.InstanceID, AccountID: rec.AccountID, JobID: rec.Job.ID, Actor: &model.AuditActor{Kind: "system"}, Details: details,
	}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimECSCreateJob(ctx context.Context, now time.Time) (CreateJobRecord, error) {
	var rec CreateJobRecord
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return rec, err
	}
	defer tx.Rollback()
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+` FROM job WHERE type='instance_create' AND status='queued' AND available_at<=? ORDER BY created_at,id LIMIT 1`, now.Unix()))
	if errors.Is(err, ErrNotFound) {
		_ = tx.Commit()
		return rec, ErrNotFound
	}
	if err != nil {
		return rec, err
	}
	owner := model.NewID("create_lease_")
	if _, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=coalesce(started_at,?),lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?",
		timestamp(now), owner, now.Add(3*time.Minute).Unix(), j.ID); err != nil {
		return rec, err
	}
	j.Owner = owner
	j.Status = "running"
	rec.Job = j
	var cfgRaw string
	err = tx.QueryRowContext(ctx, `SELECT account_id,account_revision,credential_version,region,config_json,coalesce(created_vpc_id,''),coalesce(created_vswitch_id,''),coalesce(created_sg_id,''),coalesce(created_cloud_instance_id,''),step
 FROM instance_create_job WHERE job_id=?`, j.ID).Scan(
		&rec.AccountID, &rec.AccountRevision, &rec.CredentialVersion, &rec.Region, &cfgRaw,
		&rec.CreatedVPCID, &rec.CreatedVSwitchID, &rec.CreatedSGID, &rec.CreatedCloudID, &rec.Step,
	)
	if err != nil {
		return rec, err
	}
	if err = json.Unmarshal([]byte(cfgRaw), &rec.Input); err != nil {
		return rec, err
	}
	return rec, tx.Commit()
}

func (s *Store) FinishECSCreateJob(ctx context.Context, rec CreateJobRecord, vpcID, vswID, sgID, cloudInstID, errCode string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := "succeeded"
	var errVal, resVal any
	if errCode != "" {
		status = "failed"
		errVal = errCode
	} else {
		raw, _ := json.Marshal(map[string]any{"cloud_instance_id": cloudInstID, "region_id": rec.Region, "vpc_id": vpcID, "vswitch_id": vswID})
		resVal = string(raw)
		_, _ = enqueueJob(model.WithJobTrigger(ctx, "system"), tx, "refresh", rec.AccountID, []string{rec.AccountID}, now)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status=?,finished_at=?,error_code=?,result_json=?,lease_owner=NULL,lease_until=0 WHERE id=?`,
		status, timestamp(now), errVal, resVal, rec.Job.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE instance_create_job SET created_vpc_id=?,created_vswitch_id=?,created_sg_id=?,created_cloud_instance_id=?,step=? WHERE job_id=?`,
		vpcID, vswID, sgID, cloudInstID, status, rec.Job.ID); err != nil {
		return err
	}
	details := model.AuditDetails{
		ChangedFields: []string{"status"},
		After:         map[string]any{"status": status, "cloud_instance_id": cloudInstID, "region_id": rec.Region, "error_code": errCode},
	}
	if err = appendAudit(ctx, tx, auditEvent{
		Action: "instance_create_finished", TargetType: "account", TargetID: rec.AccountID, AccountID: rec.AccountID, JobID: rec.Job.ID, Actor: &model.AuditActor{Kind: "system"}, Details: details,
	}, now); err != nil {
		return err
	}
	return tx.Commit()
}

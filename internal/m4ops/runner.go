package m4ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type Runner struct {
	Store                 *store.Store
	Box                   *security.Box
	Cloud                 aliyun.M4Cloud
	EIPManageEnabled      bool
	InstanceCreateEnabled bool
	Log                   *slog.Logger
	Now                   func() time.Time
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for {
			worked, err := r.RunOne(ctx)
			if err != nil && ctx.Err() == nil && r.Log != nil {
				r.Log.Warn("m4_ops_worker_failed")
			}
			if !worked || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r Runner) loadCredentials(ctx context.Context, accountID string) (aliyun.Credentials, error) {
	if r.Box == nil {
		return aliyun.Credentials{}, errors.New("credential_decryption_failed")
	}
	a, err := r.Store.Account(ctx, accountID)
	if err != nil || !a.Enabled {
		return aliyun.Credentials{}, errors.New("account_disabled")
	}
	cipher, err := r.Store.AccountCipher(ctx, accountID)
	if err != nil {
		return aliyun.Credentials{}, errors.New("credential_unavailable")
	}
	raw, err := r.Box.Decrypt(cipher, model.CredentialOwner(accountID))
	if err != nil {
		return aliyun.Credentials{}, errors.New("credential_decryption_failed")
	}
	defer clear(raw)
	var cred model.Credential
	if json.Unmarshal(raw, &cred) != nil {
		return aliyun.Credentials{}, errors.New("credential_decryption_failed")
	}
	return aliyun.Credentials{Credential: cred, Site: a.Site, Regions: a.RegionIDs}, nil
}

func (r Runner) RunOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil || r.Store == nil || r.Cloud == nil {
		return false, ctx.Err()
	}
	if r.EIPManageEnabled {
		rec, err := r.Store.ClaimEIPRotateJob(ctx, r.now())
		if err == nil {
			return true, r.executeEIPRotate(ctx, rec)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
	}
	if r.InstanceCreateEnabled {
		rec, err := r.Store.ClaimECSCreateJob(ctx, r.now())
		if err == nil {
			return true, r.executeECSCreate(ctx, rec)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
	}
	return false, nil
}

func (r Runner) executeEIPRotate(ctx context.Context, rec store.EIPJobRecord) error {
	cred, err := r.loadCredentials(ctx, rec.AccountID)
	if err != nil {
		return r.Store.FinishEIPRotateJob(ctx, rec, "", "", err.Error(), r.now())
	}
	defer func() { cred.AccessKeySecret = "" }()

	alloc, err := r.Cloud.AllocateEIP(ctx, cred, rec.Region, rec.BandwidthMbps, rec.ChargeType)
	if err != nil {
		return r.Store.FinishEIPRotateJob(ctx, rec, "", "", aliyun.Code(err), r.now())
	}
	// Persist managed ownership immediately before binding so Harbormaster always tracks EIPs it allocated
	if err = r.Store.RecordManagedEIP(ctx, rec.Job.ID, rec.AccountID, rec.Region, rec.InstanceID, alloc.AllocationID, alloc.EIPAddress, rec.BandwidthPackageID, r.now()); err != nil {
		return err
	}
	if rec.BandwidthPackageID != "" {
		if err = r.Cloud.AddEIPToBandwidthPackage(ctx, cred, rec.Region, rec.BandwidthPackageID, alloc.AllocationID); err != nil {
			_ = r.Cloud.ReleaseEIP(ctx, cred, rec.Region, alloc.AllocationID)
			_ = r.Store.MarkManagedEIPReleased(ctx, alloc.AllocationID, r.now())
			return r.Store.FinishEIPRotateJob(ctx, rec, "", "", aliyun.Code(err), r.now())
		}
	}
	if rec.OldAllocationID != "" {
		if err = r.Cloud.UnassociateEIP(ctx, cred, rec.Region, rec.OldAllocationID, rec.CloudInstanceID); err != nil {
			if rec.BandwidthPackageID != "" {
				_ = r.Cloud.RemoveEIPFromBandwidthPackage(ctx, cred, rec.Region, rec.BandwidthPackageID, alloc.AllocationID)
			}
			if r.Cloud.ReleaseEIP(ctx, cred, rec.Region, alloc.AllocationID) == nil {
				_ = r.Store.MarkManagedEIPReleased(ctx, alloc.AllocationID, r.now())
			}
			return r.Store.FinishEIPRotateJob(ctx, rec, "", "", aliyun.Code(err), r.now())
		}
	}
	if err = r.Cloud.AssociateEIP(ctx, cred, rec.Region, alloc.AllocationID, rec.CloudInstanceID); err != nil {
		if rec.OldAllocationID != "" {
			_ = r.Cloud.AssociateEIP(ctx, cred, rec.Region, rec.OldAllocationID, rec.CloudInstanceID)
		}
		if rec.BandwidthPackageID != "" {
			_ = r.Cloud.RemoveEIPFromBandwidthPackage(ctx, cred, rec.Region, rec.BandwidthPackageID, alloc.AllocationID)
		}
		if r.Cloud.ReleaseEIP(ctx, cred, rec.Region, alloc.AllocationID) == nil {
			_ = r.Store.MarkManagedEIPReleased(ctx, alloc.AllocationID, r.now())
		}
		return r.Store.FinishEIPRotateJob(ctx, rec, "", "", aliyun.Code(err), r.now())
	}
	if rec.OldAllocationID != "" {
		if rec.BandwidthPackageID != "" {
			_ = r.Cloud.RemoveEIPFromBandwidthPackage(ctx, cred, rec.Region, rec.BandwidthPackageID, rec.OldAllocationID)
		}
		if r.Cloud.ReleaseEIP(ctx, cred, rec.Region, rec.OldAllocationID) == nil {
			_ = r.Store.MarkManagedEIPReleased(ctx, rec.OldAllocationID, r.now())
		}
	}
	return r.Store.FinishEIPRotateJob(ctx, rec, alloc.AllocationID, alloc.EIPAddress, "", r.now())
}

func (r Runner) executeECSCreate(ctx context.Context, rec store.CreateJobRecord) error {
	cred, err := r.loadCredentials(ctx, rec.AccountID)
	if err != nil {
		return r.Store.FinishECSCreateJob(ctx, rec, "", "", "", "", err.Error(), r.now())
	}
	defer func() { cred.AccessKeySecret = "" }()

	vpcID := rec.Input.VPCID
	vswID := rec.Input.VSwitchID
	sgID := rec.Input.SecurityGroupID
	if rec.Input.AutoCreateNetwork {
		createdNet, nErr := r.Cloud.EnsureNetwork(ctx, cred, rec.Region, rec.Input.ZoneID)
		if nErr != nil {
			return r.Store.FinishECSCreateJob(ctx, rec, createdNet.VPCID, createdNet.VSwitchID, createdNet.SecurityGroupID, "", aliyun.Code(nErr), r.now())
		}
		vpcID, vswID, sgID = createdNet.VPCID, createdNet.VSwitchID, createdNet.SecurityGroupID
	}
	cloudID, err := r.Cloud.RunECSInstance(ctx, cred, rec.Input, vpcID, vswID, sgID)
	if err != nil {
		return r.Store.FinishECSCreateJob(ctx, rec, vpcID, vswID, sgID, "", aliyun.Code(err), r.now())
	}
	return r.Store.FinishECSCreateJob(ctx, rec, vpcID, vswID, sgID, cloudID, "", r.now())
}

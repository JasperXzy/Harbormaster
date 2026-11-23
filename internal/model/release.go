package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

const ReleasePreviewLifetime = 5 * time.Minute

type ReleaseObservation struct {
	InstancePresent bool      `json:"instance_present"`
	ObservedAt      time.Time `json:"observed_at"`
}

type ReleaseJobResult struct {
	Action            string              `json:"action"`
	ConfirmedName     string              `json:"confirmed_name"`
	PreviewFetchedAt  time.Time           `json:"preview_fetched_at"`
	DispatchOutcome   string              `json:"dispatch_outcome"`
	Observation       *ReleaseObservation `json:"observation"`
	ConfirmationBasis *string             `json:"confirmation_basis"`
}

// ReleaseInventory is a complete regional read, never a provider atomic
// snapshot. Optional provider facts remain pointers until actually observed.
type ReleaseInventory struct {
	Instances []ReleaseInstanceFacts `json:"instances"`
	Disks     []ReleaseDiskFacts     `json:"disks"`
}

type ReleaseInstanceFacts struct {
	CloudID            string `json:"cloud_instance_id"`
	Name               string `json:"instance_name"`
	Status             string `json:"cloud_status"`
	ChargeType         string `json:"instance_charge_type"`
	SpotStrategy       string `json:"spot_strategy"`
	DeletionProtection *bool  `json:"deletion_protection"`
	Locked             *bool  `json:"locked"`
}

type ReleaseDiskFacts struct {
	CloudID            string   `json:"cloud_disk_id"`
	Name               string   `json:"name"`
	Category           string   `json:"category"`
	Type               string   `json:"disk_type"`
	InstanceIDs        []string `json:"attached_instance_ids"`
	AttachmentsKnown   bool     `json:"attachments_known"`
	DeleteWithInstance *bool    `json:"delete_with_instance"`
	DeleteAutoSnapshot *bool    `json:"delete_auto_snapshot"`
}

type ReleaseInventoryObservation struct {
	Region    string
	QueriedAt time.Time
	Data      *ReleaseInventory
	Error     *APIError
}

type ReleaseDisk struct {
	ReleaseDiskFacts
	StorageKind        string `json:"storage_kind"`
	Action             string `json:"action"`
	AutoSnapshotAction string `json:"auto_snapshot_action"`
}

type ReleaseImpact struct {
	ReleaseInstanceFacts
	InstanceDataAction    string        `json:"instance_data_action"`
	FixedPublicIPAction   string        `json:"fixed_public_ip_action"`
	ManualSnapshotsAction string        `json:"manual_snapshots_action"`
	EIPsAction            string        `json:"eips_action"`
	Disks                 []ReleaseDisk `json:"disks"`
}

type ReleasePreview struct {
	InstanceID       string         `json:"instance_id"`
	AccountID        string         `json:"account_id"`
	Status           string         `json:"status"`
	BlockReasons     []string       `json:"block_reasons"`
	FetchedAt        *time.Time     `json:"fetched_at"`
	LastAttemptAt    *time.Time     `json:"last_attempt_at"`
	LastError        *APIError      `json:"last_error"`
	ExpiresAt        *time.Time     `json:"expires_at"`
	Token            *string        `json:"preview_token"`
	ExecutionEnabled bool           `json:"execution_enabled"`
	Impact           *ReleaseImpact `json:"impact"`
}

func ReleaseStorageKind(category string) string {
	switch category {
	case "cloud", "cloud_efficiency", "cloud_ssd", "cloud_essd", "cloud_auto", "cloud_essd_entry", "cloud_regional_disk_auto":
		return "cloud"
	case "local_ssd_pro", "local_hdd_pro", "ephemeral", "ephemeral_ssd":
		return "local"
	case "elastic_ephemeral_disk_standard", "elastic_ephemeral_disk_premium":
		return "ephemeral"
	}
	return "unknown"
}

func (d ReleaseInventory) Valid() bool {
	if d.Instances == nil || d.Disks == nil || len(d.Instances) > 10000 || len(d.Disks) > 5000 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range d.Instances {
		if v.CloudID == "" || seen[v.CloudID] {
			return false
		}
		seen[v.CloudID] = true
	}
	seen = map[string]bool{}
	for _, v := range d.Disks {
		if v.CloudID == "" || seen[v.CloudID] || v.InstanceIDs == nil {
			return false
		}
		seen[v.CloudID] = true
		ids := map[string]bool{}
		for _, id := range v.InstanceIDs {
			if id == "" || ids[id] {
				return false
			}
			ids[id] = true
		}
	}
	return true
}

// ImpactFor retains the exact name and provider flags. Unknown disk ownership
// blocks a preview even if the disk cannot currently be assigned to a target.
func (d ReleaseInventory) ImpactFor(cloudID string) (*ReleaseImpact, []string) {
	reasons := []string{}
	add := func(code string) {
		for _, existing := range reasons {
			if existing == code {
				return
			}
		}
		reasons = append(reasons, code)
	}
	var instance *ReleaseInstanceFacts
	for i := range d.Instances {
		if d.Instances[i].CloudID == cloudID {
			instance = &d.Instances[i]
			break
		}
	}
	if instance == nil {
		return nil, []string{"release_information_incomplete"}
	}
	impact := &ReleaseImpact{ReleaseInstanceFacts: *instance, InstanceDataAction: "delete", FixedPublicIPAction: "release_if_present", ManualSnapshotsAction: "retain", EIPsAction: "retain", Disks: []ReleaseDisk{}}
	if instance.Name == "" {
		add("instance_name_unavailable")
	}
	if instance.Status != "Stopped" {
		add("instance_not_stopped")
	}
	if instance.ChargeType != "PostPaid" {
		add("instance_charge_type_unsupported")
	}
	if instance.DeletionProtection == nil || instance.Locked == nil {
		add("release_information_incomplete")
	}
	if instance.DeletionProtection != nil && *instance.DeletionProtection {
		add("instance_deletion_protected")
	}
	if instance.Locked != nil && *instance.Locked {
		add("instance_locked")
	}
	systemDisk := false
	for _, disk := range d.Disks {
		if !disk.AttachmentsKnown {
			add("release_disk_information_incomplete")
		}
		attached := false
		for _, id := range disk.InstanceIDs {
			attached = attached || id == cloudID
		}
		if !attached {
			continue
		}
		v := ReleaseDisk{ReleaseDiskFacts: disk, StorageKind: ReleaseStorageKind(disk.Category), Action: "unknown", AutoSnapshotAction: "unknown"}
		v.InstanceIDs = append([]string{}, disk.InstanceIDs...)
		sort.Strings(v.InstanceIDs)
		systemDisk = systemDisk || disk.Type == "system"
		if disk.Type != "system" && disk.Type != "data" {
			add("release_disk_information_incomplete")
		}
		switch v.StorageKind {
		case "cloud":
			if disk.DeleteWithInstance == nil || disk.DeleteAutoSnapshot == nil {
				add("release_disk_information_incomplete")
			}
			if disk.DeleteWithInstance != nil {
				v.Action, v.AutoSnapshotAction = "retain", "retain"
				if *disk.DeleteWithInstance {
					v.Action, v.AutoSnapshotAction = "delete", "unknown"
					if disk.DeleteAutoSnapshot != nil {
						v.AutoSnapshotAction = "retain"
						if *disk.DeleteAutoSnapshot {
							v.AutoSnapshotAction = "delete"
						}
					}
				}
			}
		case "local", "ephemeral":
			v.Action, v.AutoSnapshotAction = "delete", "not_applicable"
		default:
			add("release_disk_information_incomplete")
		}
		impact.Disks = append(impact.Disks, v)
	}
	if !systemDisk {
		add("release_disk_information_incomplete")
	}
	sort.Slice(impact.Disks, func(i, j int) bool { return impact.Disks[i].CloudID < impact.Disks[j].CloudID })
	return impact, reasons
}

// ReleasePreviewToken binds cached evidence and the account revision. It is a
// content fingerprint, not a credential or a provider idempotency token.
func ReleasePreviewToken(instanceID string, accountRevision, credentialVersion int64, fetched time.Time, impact ReleaseImpact) string {
	raw, _ := json.Marshal(struct {
		InstanceID                         string
		AccountRevision, CredentialVersion int64
		Fetched                            string
		Impact                             ReleaseImpact
	}{instanceID, accountRevision, credentialVersion, fetched.UTC().Format(time.RFC3339Nano), impact})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

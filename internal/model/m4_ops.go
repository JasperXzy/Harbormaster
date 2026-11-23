package model

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type FailoverRuleInput struct {
	Revision          int64  `json:"revision"`
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	DryRun            bool   `json:"dry_run"`
	PrimaryInstanceID string `json:"primary_instance_id"`
	StandbyInstanceID string `json:"standby_instance_id"`
}

func (in FailoverRuleInput) Validate() map[string]string {
	f := map[string]string{}
	n := strings.TrimSpace(in.Name)
	if n == "" || utf8.RuneCountInString(n) > 80 || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		f["name"] = "invalid_format"
	}
	if in.PrimaryInstanceID == "" || len(in.PrimaryInstanceID) > 128 {
		f["primary_instance_id"] = "required"
	}
	if in.StandbyInstanceID == "" || len(in.StandbyInstanceID) > 128 {
		f["standby_instance_id"] = "required"
	}
	if in.PrimaryInstanceID != "" && in.PrimaryInstanceID == in.StandbyInstanceID {
		f["standby_instance_id"] = "must_differ_from_primary"
	}
	if in.Revision < 0 {
		f["revision"] = "invalid_format"
	}
	return f
}

type FailoverRule struct {
	ID                string     `json:"id"`
	Revision          int64      `json:"revision"`
	Name              string     `json:"name"`
	Enabled           bool       `json:"enabled"`
	DryRun            bool       `json:"dry_run"`
	PrimaryInstanceID string     `json:"primary_instance_id"`
	StandbyInstanceID string     `json:"standby_instance_id"`
	LastTriggeredAt   *time.Time `json:"last_triggered_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type EIPRotateInput struct {
	BandwidthMbps      int    `json:"bandwidth_mbps"`
	InternetChargeType string `json:"internet_charge_type"` // PayByTraffic, PayByBandwidth
	BandwidthPackageID string `json:"bandwidth_package_id"`
}

func (in EIPRotateInput) Validate() map[string]string {
	f := map[string]string{}
	if in.BandwidthMbps < 1 || in.BandwidthMbps > 2000 {
		f["bandwidth_mbps"] = "out_of_range"
	}
	if in.InternetChargeType != "PayByTraffic" && in.InternetChargeType != "PayByBandwidth" {
		f["internet_charge_type"] = "invalid_format"
	}
	if len(in.BandwidthPackageID) > 128 || strings.IndexFunc(in.BandwidthPackageID, unicode.IsControl) >= 0 {
		f["bandwidth_package_id"] = "invalid_format"
	}
	return f
}

type ECSCatalogZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ECSCatalogImage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ECSCatalogInstanceType struct {
	ID        string  `json:"id"`
	CPU       int     `json:"cpu"`
	MemoryGiB float64 `json:"memory_gib"`
}

type ECSCatalogVPC struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CidrBlock string `json:"cidr_block"`
}

type ECSCatalogVSwitch struct {
	ID        string `json:"id"`
	VPCID     string `json:"vpc_id"`
	ZoneID    string `json:"zone_id"`
	Name      string `json:"name"`
	CidrBlock string `json:"cidr_block"`
}

type ECSCatalog struct {
	AccountID     string                   `json:"account_id"`
	RegionID      string                   `json:"region_id"`
	Zones         []ECSCatalogZone         `json:"zones"`
	Images        []ECSCatalogImage        `json:"images"`
	InstanceTypes []ECSCatalogInstanceType `json:"instance_types"`
	VPCs          []ECSCatalogVPC          `json:"vpcs"`
	VSwitches     []ECSCatalogVSwitch      `json:"vswitches"`
}

type ECSCreateInput struct {
	AccountID          string `json:"account_id"`
	RegionID           string `json:"region_id"`
	ZoneID             string `json:"zone_id"`
	InstanceName       string `json:"instance_name"`
	InstanceType       string `json:"instance_type"`
	ImageID            string `json:"image_id"`
	InstanceChargeType string `json:"instance_charge_type"` // PostPaid, Spot
	InternetChargeType string `json:"internet_charge_type"` // PayByTraffic, PayByBandwidth
	BandwidthOutMbps   int    `json:"bandwidth_out_mbps"`
	SystemDiskGiB      int    `json:"system_disk_gib"`
	AutoCreateNetwork  bool   `json:"auto_create_network"`
	VPCID              string `json:"vpc_id"`
	VSwitchID          string `json:"vswitch_id"`
	SecurityGroupID    string `json:"security_group_id"`
	Confirm            bool   `json:"confirm"`
}

func (in ECSCreateInput) Validate() map[string]string {
	f := map[string]string{}
	if strings.TrimSpace(in.AccountID) == "" || len(in.AccountID) > 128 {
		f["account_id"] = "required"
	}
	if !RegionPattern.MatchString(in.RegionID) || len(in.RegionID) > 64 {
		f["region_id"] = "invalid_format"
	}
	if strings.TrimSpace(in.ZoneID) == "" || len(in.ZoneID) > 64 {
		f["zone_id"] = "required"
	}
	name := strings.TrimSpace(in.InstanceName)
	if len(name) < 2 || utf8.RuneCountInString(name) > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		f["instance_name"] = "invalid_format"
	}
	if strings.TrimSpace(in.InstanceType) == "" || len(in.InstanceType) > 64 {
		f["instance_type"] = "required"
	}
	if strings.TrimSpace(in.ImageID) == "" || len(in.ImageID) > 128 {
		f["image_id"] = "required"
	}
	if in.InstanceChargeType != "PostPaid" && in.InstanceChargeType != "Spot" {
		f["instance_charge_type"] = "invalid_format"
	}
	if in.InternetChargeType != "PayByTraffic" && in.InternetChargeType != "PayByBandwidth" {
		f["internet_charge_type"] = "invalid_format"
	}
	if in.BandwidthOutMbps < 0 || in.BandwidthOutMbps > 200 {
		f["bandwidth_out_mbps"] = "out_of_range"
	}
	if in.SystemDiskGiB < 20 || in.SystemDiskGiB > 500 {
		f["system_disk_gib"] = "out_of_range"
	}
	if !in.AutoCreateNetwork {
		if strings.TrimSpace(in.VPCID) == "" {
			f["vpc_id"] = "required"
		}
		if strings.TrimSpace(in.VSwitchID) == "" {
			f["vswitch_id"] = "required"
		}
	}
	if !in.Confirm {
		f["confirm"] = "required"
	}
	return f
}

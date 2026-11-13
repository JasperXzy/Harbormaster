package model

import (
	"slices"
	"testing"
	"time"
)

func releaseFlag(v bool) *bool { return &v }

func releaseFactsFixture() ReleaseInventory {
	return ReleaseInventory{
		Instances: []ReleaseInstanceFacts{{CloudID: "i-test", Name: "exact name", Status: "Stopped", ChargeType: "PostPaid", SpotStrategy: "SpotAsPriceGo", DeletionProtection: releaseFlag(false), Locked: releaseFlag(false)}},
		Disks: []ReleaseDiskFacts{
			{CloudID: "d-system", Category: "cloud_essd", Type: "system", InstanceIDs: []string{"i-test"}, AttachmentsKnown: true, DeleteWithInstance: releaseFlag(true), DeleteAutoSnapshot: releaseFlag(false)},
			{CloudID: "d-data", Category: "cloud_ssd", Type: "data", InstanceIDs: []string{"i-test"}, AttachmentsKnown: true, DeleteWithInstance: releaseFlag(false), DeleteAutoSnapshot: releaseFlag(true)},
			{CloudID: "d-local", Category: "local_ssd_pro", Type: "data", InstanceIDs: []string{"i-test"}, AttachmentsKnown: true},
		},
	}
}

func TestReleaseImpactPreservesProviderDeletionAttributes(t *testing.T) {
	d := releaseFactsFixture()
	impact, reasons := d.ImpactFor("i-test")
	if !d.Valid() || impact == nil || len(reasons) != 0 || impact.Name != "exact name" || impact.EIPsAction != "retain" || impact.ManualSnapshotsAction != "retain" || len(impact.Disks) != 3 {
		t.Fatal(impact, reasons)
	}
	for _, disk := range impact.Disks {
		switch disk.CloudID {
		case "d-system":
			if disk.Action != "delete" || disk.AutoSnapshotAction != "retain" || !*disk.DeleteWithInstance || *disk.DeleteAutoSnapshot {
				t.Fatal(disk)
			}
		case "d-data":
			if disk.Action != "retain" || disk.AutoSnapshotAction != "retain" || *disk.DeleteWithInstance || !*disk.DeleteAutoSnapshot {
				t.Fatal("retained disk must retain snapshots despite its deletion flag", disk)
			}
		case "d-local":
			if disk.Action != "delete" || disk.AutoSnapshotAction != "not_applicable" || disk.DeleteWithInstance != nil {
				t.Fatal("local disk was treated as retainable", disk)
			}
		}
	}
}

func TestReleaseImpactBlocksUnsupportedAndIncompleteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		edit         func(*ReleaseInventory)
	}{
		{"running", "instance_not_stopped", func(d *ReleaseInventory) { d.Instances[0].Status = "Running" }},
		{"subscription", "instance_charge_type_unsupported", func(d *ReleaseInventory) { d.Instances[0].ChargeType = "PrePaid" }},
		{"missing name", "instance_name_unavailable", func(d *ReleaseInventory) { d.Instances[0].Name = "" }},
		{"deletion protection", "instance_deletion_protected", func(d *ReleaseInventory) { d.Instances[0].DeletionProtection = releaseFlag(true) }},
		{"unknown protection", "release_information_incomplete", func(d *ReleaseInventory) { d.Instances[0].DeletionProtection = nil }},
		{"lock", "instance_locked", func(d *ReleaseInventory) { d.Instances[0].Locked = releaseFlag(true) }},
		{"unknown lock", "release_information_incomplete", func(d *ReleaseInventory) { d.Instances[0].Locked = nil }},
		{"disk flag missing", "release_disk_information_incomplete", func(d *ReleaseInventory) { d.Disks[0].DeleteAutoSnapshot = nil }},
		{"unknown disk kind", "release_disk_information_incomplete", func(d *ReleaseInventory) { d.Disks[0].Category = "new_unverified_kind" }},
		{"no system disk", "release_disk_information_incomplete", func(d *ReleaseInventory) { d.Disks[0].Type = "data" }},
		{"unassigned disk might belong to target", "release_disk_information_incomplete", func(d *ReleaseInventory) {
			d.Disks = append(d.Disks, ReleaseDiskFacts{CloudID: "d-unknown", InstanceIDs: []string{}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := releaseFactsFixture()
			tc.edit(&d)
			_, reasons := d.ImpactFor("i-test")
			if !slices.Contains(reasons, tc.reason) {
				t.Fatal(reasons)
			}
		})
	}
	if impact, reasons := releaseFactsFixture().ImpactFor("i-not-found"); impact != nil || !slices.Contains(reasons, "release_information_incomplete") {
		t.Fatal(impact, reasons)
	}
}

func TestReleasePreviewFingerprintBindsAccountAndSourceEvidence(t *testing.T) {
	d := releaseFactsFixture()
	now := time.Now().UTC()
	i, _ := d.ImpactFor("i-test")
	token := ReleasePreviewToken("local-id", 1, 1, now, *i)
	slices.Reverse(d.Disks)
	again, _ := d.ImpactFor("i-test")
	if token != ReleasePreviewToken("local-id", 1, 1, now, *again) {
		t.Fatal("provider order changed the displayed impact fingerprint")
	}
	for _, changed := range []string{
		ReleasePreviewToken("other-id", 1, 1, now, *i),
		ReleasePreviewToken("local-id", 2, 1, now, *i),
		ReleasePreviewToken("local-id", 1, 2, now, *i),
		ReleasePreviewToken("local-id", 1, 1, now.Add(time.Second), *i),
	} {
		if token == changed {
			t.Fatal("confirmation did not bind its identity, account or source time")
		}
	}
	i.Name = "changed name"
	if token == ReleasePreviewToken("local-id", 1, 1, now, *i) {
		t.Fatal("name change reused confirmation")
	}
}

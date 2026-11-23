package aliyun

import (
	"context"
	"testing"
	"time"
)

func TestIdleInstanceAndEIPClassificationPreservesUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, kind, reason string
		row                map[string]any
		unknown            int
	}{
		{"stopped subscription", "ecs", "stopped_subscription", map[string]any{"Status": "Stopped", "InstanceChargeType": "PrePaid"}, 0},
		{"spot charged stop", "ecs", "stopped_keep_charging", map[string]any{"Status": "Stopped", "InstanceChargeType": "PostPaid", "SpotStrategy": "SpotAsPriceGo", "StoppedMode": "KeepCharging"}, 0},
		{"no charge exemption", "ecs", "stopped_not_charge_exempt", map[string]any{"Status": "Stopped", "InstanceChargeType": "PostPaid", "StoppedMode": "Not-applicable"}, 0},
		{"economical stop", "ecs", "", map[string]any{"Status": "Stopped", "InstanceChargeType": "PostPaid", "StoppedMode": "StopCharging"}, 0},
		{"unknown stop mode", "ecs", "", map[string]any{"Status": "Stopped", "InstanceChargeType": "PostPaid"}, 1},
		{"running", "ecs", "", map[string]any{"Status": "Running"}, 0},
		{"unknown charge", "ecs", "", map[string]any{"Status": "Stopped"}, 1},
		{"contradictory prepaid spot", "ecs", "", map[string]any{"Status": "Stopped", "InstanceChargeType": "PrePaid", "SpotStrategy": "SpotAsPriceGo"}, 1},
		{"stopped SWAS", "swas", "stopped_subscription", map[string]any{"Status": "Stopped", "ChargeType": "PrePaid"}, 0},
		{"unverified SWAS charge", "swas", "", map[string]any{"Status": "Stopped", "ChargeType": "PostPaid"}, 1},
		{"available EIP", "eip", "unbound_eip", map[string]any{"Status": "Available", "ChargeType": "PostPaid", "InstanceId": "", "IpAddress": "203.0.113.60"}, 0},
		{"EIP contradictions", "eip", "", map[string]any{"Status": "Available", "InstanceId": "i-bound"}, 1},
		{"bound EIP", "eip", "", map[string]any{"Status": "InUse", "InstanceId": "i-bound"}, 0},
		{"unknown EIP", "eip", "", map[string]any{}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "InstanceId"
			if tc.kind == "eip" {
				key = "AllocationId"
			}
			if tc.kind != "eip" || tc.row[key] == nil {
				tc.row[key] = "cloud-test"
			}
			if tc.kind == "eip" {
				tc.row["AllocationId"] = "eip-test"
			}
			o := idleObservation(testAccount(), "cn-shanghai", tc.kind, collection{rows: []map[string]any{tc.row}}, collection{}, time.Now().UTC())
			if o.Error != nil || o.Data == nil || o.Data.Scanned != 1 || len(o.Data.UnclassifiedIDs) != tc.unknown {
				t.Fatal(o)
			}
			want := 0
			if tc.reason != "" {
				want = 1
			}
			if len(o.Data.Candidates) != want || len(o.Data.NonCandidateIDs) != 1-tc.unknown-want || !o.Data.Valid() {
				t.Fatal(o.Data)
			}
			if want == 1 {
				v := o.Data.Candidates[0]
				if v.Reason != tc.reason || v.MonthlyCost != nil || v.Currency != nil {
					t.Fatal(v)
				}
			}
		})
	}
}

func TestIdleDisksUseAllAttachmentEvidenceAndIndependentScopes(t *testing.T) {
	for _, tc := range []struct {
		name, category, status, multi string
		ids                           []string
		states                        map[string]string
		failedECS                     bool
		reason                        string
		unknown                       int
	}{
		{"unattached", "cloud_essd", "Available", "Disabled", []string{}, nil, false, "unattached_cloud_disk", 0},
		{"unattached during ECS error", "cloud_essd", "Available", "Disabled", []string{}, nil, true, "unattached_cloud_disk", 0},
		{"stopped instance disk", "cloud_essd", "In_use", "Disabled", []string{"i-one"}, map[string]string{"i-one": "Stopped"}, false, "disk_on_stopped_instances", 0},
		{"running instance disk", "cloud_essd", "In_use", "Disabled", []string{"i-one"}, map[string]string{"i-one": "Running"}, false, "", 0},
		{"multi attach stopped", "cloud_ssd", "In_use", "Enabled", []string{"i-one", "i-two"}, map[string]string{"i-one": "Stopped", "i-two": "Stopped"}, false, "disk_on_stopped_instances", 0},
		{"multi attach one running", "cloud_ssd", "In_use", "Enabled", []string{"i-one", "i-two"}, map[string]string{"i-one": "Stopped", "i-two": "Running"}, false, "", 0},
		{"missing attached instance", "cloud_essd", "In_use", "Disabled", []string{"i-one"}, nil, false, "", 1},
		{"failed instance read", "cloud_essd", "In_use", "Disabled", []string{"i-one"}, map[string]string{"i-one": "Stopped"}, true, "", 1},
		{"empty ID is not unattached", "cloud_essd", "In_use", "Enabled", []string{}, nil, false, "", 1},
		{"local disk", "local_ssd_pro", "Available", "Disabled", []string{}, nil, false, "", 0},
		{"temporary disk", "elastic_ephemeral_disk_standard", "Available", "Disabled", []string{}, nil, false, "", 0},
		{"unknown category", "future_kind", "Available", "Disabled", []string{}, nil, false, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{"DiskId": "d-test", "Category": tc.category, "Status": tc.status, "MultiAttach": tc.multi, "DiskChargeType": "PostPaid", "Size": 40}
			if tc.multi == "Disabled" && len(tc.ids) == 1 {
				row["InstanceId"] = tc.ids[0]
			} else {
				rows := []any{}
				for _, id := range tc.ids {
					rows = append(rows, map[string]any{"InstanceId": id})
				}
				row["Attachments"] = map[string]any{"Attachment": rows}
			}
			ecs := collection{rows: []map[string]any{}}
			for id, state := range tc.states {
				ecs.rows = append(ecs.rows, map[string]any{"InstanceId": id, "Status": state})
			}
			if tc.failedECS {
				ecs.err = failure("permission_denied")
			}
			o := idleObservation(testAccount(), "cn-shanghai", "disk", collection{rows: []map[string]any{row}}, ecs, time.Now().UTC())
			if o.Error != nil || o.Data == nil || len(o.Data.UnclassifiedIDs) != tc.unknown {
				t.Fatal(o)
			}
			want := 0
			if tc.reason != "" {
				want = 1
			}
			if len(o.Data.Candidates) != want || len(o.Data.NonCandidateIDs) != 1-tc.unknown-want || !o.Data.Valid() {
				t.Fatal(o.Data)
			}
			if want == 1 {
				v := o.Data.Candidates[0]
				if v.Reason != tc.reason || v.SizeGiB == nil || *v.SizeGiB != 40 || v.MonthlyCost != nil {
					t.Fatal(v)
				}
			}
		})
	}
}

func TestIdleInventoryReusesExistingCallsAndRejectsWrongScope(t *testing.T) {
	calls := map[string]int{}
	inv := Inventory{Caller: callFunc(func(ctx context.Context, c Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls[action]++
		return inventoryFixture(ctx, c, action, region, q)
	})}
	got := inv.Scan(context.Background(), testAccount(), fakeCredentials().Credential, false, func() error { return nil })
	if len(got.Idle) != 4 || calls["DescribeDisks"] != 1 || calls["DescribeEipAddresses"] != 1 || calls["DescribeInstances"] != 1 || calls["ListInstances"] != 1 {
		t.Fatal(len(got.Idle), calls)
	}
	for _, o := range got.Idle {
		if o.Error != nil || o.Data == nil {
			t.Fatal(o)
		}
	}
	bad := idleObservation(testAccount(), "cn-shanghai", "eip", collection{rows: []map[string]any{{"AllocationId": "eip-wrong", "Status": "Available", "RegionId": "cn-hongkong"}}}, collection{}, time.Now().UTC())
	if bad.Error == nil || bad.Data != nil || bad.Error.Code != "invalid_response" {
		t.Fatal(bad)
	}
	failed := idleObservation(testAccount(), "cn-shanghai", "eip", collection{err: failure("pagination_incomplete")}, collection{}, time.Now().UTC())
	if failed.Error == nil || failed.Data != nil || failed.Error.Code != "pagination_incomplete" {
		t.Fatal(failed)
	}
}

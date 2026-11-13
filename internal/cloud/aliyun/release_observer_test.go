package aliyun

import (
	"context"
	"testing"
)

func TestReleaseObserverRequiresCompleteTargetedPresenceEvidence(t *testing.T) {
	row := map[string]any{"InstanceId": "i-test", "RegionId": "cn-shanghai"}
	for _, tc := range []struct {
		name    string
		body    map[string]any
		present bool
		code    string
	}{
		{"present", map[string]any{"Instances": map[string]any{"Instance": []any{row}}, "TotalCount": 1}, true, ""},
		{"absent", map[string]any{"Instances": map[string]any{"Instance": []any{}}, "TotalCount": 0}, false, ""},
		{"missing count", map[string]any{"Instances": map[string]any{"Instance": []any{}}}, false, "invalid_response"},
		{"incomplete count", map[string]any{"Instances": map[string]any{"Instance": []any{}}, "TotalCount": 1}, false, "invalid_response"},
		{"more pages", map[string]any{"Instances": map[string]any{"Instance": []any{}}, "TotalCount": 0, "NextToken": "next"}, false, "pagination_incomplete"},
		{"foreign ID", map[string]any{"Instances": map[string]any{"Instance": []any{map[string]any{"InstanceId": "i-other", "RegionId": "cn-shanghai"}}}, "TotalCount": 1}, false, "invalid_response"},
		{"wrong region", map[string]any{"Instances": map[string]any{"Instance": []any{map[string]any{"InstanceId": "i-test", "RegionId": "cn-hongkong"}}}, "TotalCount": 1}, false, "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := ReleaseObserver{Caller: callFunc(func(_ context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
				if action != "DescribeInstances" || region != "cn-shanghai" || q["RegionId"] != region || q["InstanceIds"] != `["i-test"]` || q["PageNumber"] != "1" || q["PageSize"] != "10" || len(q) != 4 {
					t.Fatal("untargeted release observation", action, q)
				}
				return tc.body, nil
			})}
			got, err := o.Observe(context.Background(), fakeCredentials(), "cn-shanghai", "i-test")
			if Code(err) != tc.code || (err == nil && (got.InstancePresent != tc.present || got.ObservedAt.IsZero())) || (err != nil && !got.ObservedAt.IsZero()) {
				t.Fatal(got, Code(err))
			}
		})
	}
}

func TestReleaseInspectorReadsDisksOnlyForExistingExactTarget(t *testing.T) {
	for _, found := range []bool{false, true} {
		calls := 0
		o := ReleaseObserver{Caller: callFunc(func(_ context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
			calls++
			if action == "DescribeInstances" {
				rows := []any{}
				if found {
					rows = append(rows, map[string]any{"InstanceId": "i-test", "RegionId": region, "InstanceName": "exact name", "Status": "Stopped", "InstanceChargeType": "PostPaid", "DeletionProtection": false, "OperationLocks": map[string]any{"LockReason": []any{}}})
				}
				return map[string]any{"Instances": map[string]any{"Instance": rows}, "TotalCount": len(rows)}, nil
			}
			if action != "DescribeDisks" || !found || q["MaxResults"] != "100" {
				t.Fatal(action, q)
			}
			return map[string]any{"Disks": map[string]any{"Disk": []any{map[string]any{"DiskId": "d-test", "InstanceId": "i-test", "RegionId": region, "Status": "In_use", "MultiAttach": "Disabled", "Category": "cloud_essd", "Type": "system", "DeleteWithInstance": true, "DeleteAutoSnapshot": false}}}, "TotalCount": 0}, nil
		})}
		got, err := o.Inspect(context.Background(), fakeCredentials(), "cn-shanghai", "i-test")
		if !found {
			if Code(err) != "instance_not_found" || calls != 1 {
				t.Fatal(Code(err), calls)
			}
		} else {
			if err != nil || got.Data == nil || calls != 2 {
				t.Fatal(got, err, calls)
			}
			impact, reasons := got.Data.ImpactFor("i-test")
			if impact == nil || len(reasons) != 0 {
				t.Fatal(impact, reasons)
			}
		}
	}
}

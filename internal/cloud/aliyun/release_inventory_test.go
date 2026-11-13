package aliyun

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReleaseDiskAttachmentEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		row   map[string]any
		known bool
		ids   string
	}{
		{"single", map[string]any{"Status": "In_use", "MultiAttach": "Disabled", "InstanceId": "i-one"}, true, "i-one"},
		{"unattached", map[string]any{"Status": "Available", "InstanceId": ""}, true, ""},
		{"empty ID is not unattached", map[string]any{"Status": "In_use", "InstanceId": ""}, false, ""},
		{"multi", map[string]any{"Status": "In_use", "MultiAttach": "Enabled", "InstanceId": "", "Attachments": map[string]any{"Attachment": []any{map[string]any{"InstanceId": "i-two"}, map[string]any{"InstanceId": "i-one"}}}}, true, "i-one,i-two"},
		{"legacy shared", map[string]any{"Status": "In_use", "MultiAttach": "LegacyShared", "MountInstances": map[string]any{"MountInstance": []any{map[string]any{"InstanceId": "i-one"}}}}, true, "i-one"},
		{"contradictory", map[string]any{"Status": "Available", "InstanceId": "i-one"}, false, "i-one"},
		{"malformed list", map[string]any{"Status": "Available", "Attachments": map[string]any{"Attachment": "bad"}}, false, ""},
		{"detaching", map[string]any{"Status": "Detaching", "MultiAttach": "Disabled", "InstanceId": "i-one"}, false, "i-one"},
		{"unknown status", map[string]any{"Status": "FutureStatus", "InstanceId": "i-one"}, false, "i-one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids, known := diskAttachments(tc.row)
			if known != tc.known || strings.Join(ids, ",") != tc.ids || ids == nil {
				t.Fatal(ids, known)
			}
		})
	}
}

func TestReleaseDiskTokenPaginationAndIncompletePages(t *testing.T) {
	for _, tc := range []struct{ name, token, code string }{
		{"complete ignores TotalCount", "", ""},
		{"repeated token", "again", "pagination_incomplete"},
		{"invalid token", " ", "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, fences := 0, 0
			inventory := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
				calls++
				if action != "DescribeDisks" || region != "cn-shanghai" || q["RegionId"] != region || q["MaxResults"] != "100" || len(q) != calls+1 {
					t.Fatal("unexpected disk read parameters", action, q)
				}
				id, next := "d-one", "again"
				if calls == 2 {
					if q["NextToken"] != "again" {
						t.Fatal(q)
					}
					id, next = "d-two", tc.token
				}
				return map[string]any{"Disks": map[string]any{"Disk": []any{map[string]any{"DiskId": id}}}, "TotalCount": 0, "NextToken": next}, nil
			})}
			got := inventory.releaseDisks(context.Background(), fakeCredentials(), "cn-shanghai", func() error { fences++; return nil })
			if Code(got.err) != tc.code || calls != 2 || fences != 2 || (tc.code == "" && len(got.rows) != 2) || (tc.code != "" && len(got.rows) != 0) {
				t.Fatal(got, calls, fences)
			}
		})
	}
}

func TestReleaseInventoryKeepsUnknownFlagsAndRejectsWrongRegion(t *testing.T) {
	instance := map[string]any{"InstanceId": "i-one", "InstanceName": "exact name", "Status": "Stopped", "InstanceChargeType": "PostPaid", "OperationLocks": map[string]any{"LockReason": []any{}}}
	disk := map[string]any{"DiskId": "d-one", "InstanceId": "i-one", "Status": "In_use", "MultiAttach": "Disabled", "RegionId": "cn-shanghai"}
	o := releaseInventory("cn-shanghai", collection{rows: []map[string]any{instance}}, collection{rows: []map[string]any{disk}}, time.Now().UTC())
	if o.Error != nil || o.Data == nil || o.Data.Instances[0].DeletionProtection != nil || o.Data.Instances[0].Locked == nil || *o.Data.Instances[0].Locked || !o.Data.Disks[0].AttachmentsKnown || o.Data.Disks[0].DeleteWithInstance != nil {
		t.Fatal(o)
	}
	disk["RegionId"] = "cn-hongkong"
	if got := releaseInventory("cn-shanghai", collection{rows: []map[string]any{instance}}, collection{rows: []map[string]any{disk}}, time.Now().UTC()); got.Data != nil || got.Error == nil || got.Error.Code != "invalid_response" {
		t.Fatal(got)
	}
}

func TestSignedDiskReadRejectsWriteAndUnapprovedParameters(t *testing.T) {
	calls := 0
	client := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Host != "ecs.cn-shanghai.aliyuncs.com" || header(r.Header, "x-acs-action") != "DescribeDisks" || header(r.Header, "Authorization") == "" {
			t.Fatal("unexpected disk request")
		}
		return powerResponse(200, `{"Disks":{"Disk":[]},"NextToken":""}`), nil
	}), 0)
	if _, err := client.Call(context.Background(), fakeCredentials(), "DescribeDisks", "cn-shanghai", map[string]string{"RegionId": "cn-shanghai", "MaxResults": "100"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Force", "DryRun", "DeleteWithInstance", "InstanceId", "Endpoint"} {
		if _, err := client.Call(context.Background(), fakeCredentials(), "DescribeDisks", "cn-shanghai", map[string]string{key: "true"}); Code(err) != "parameter_not_allowed" {
			t.Fatal(key, Code(err))
		}
	}
	if calls != 1 {
		t.Fatal("rejected parameters reached transport", calls)
	}
}

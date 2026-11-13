package aliyun

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func swasUsageRow(id string) map[string]any {
	return map[string]any{"InstanceId": id, "TrafficPackageTotal": 20, "TrafficUsed": 25, "TrafficPackageRemaining": -5, "TrafficOverflow": 5}
}

func TestSWASTrafficPreservesLiteralByteFieldsAndSignedRemaining(t *testing.T) {
	row := swasUsageRow("swas-test")
	row["TrafficPackageTotal"] = json.Number("20")
	row["TrafficUsed"] = "25"
	got, err := parseSWASTraffic(map[string]any{"InstanceTrafficPackageUsages": []any{row}}, []string{"swas-test", "omitted"})
	if err != nil || len(got) != 1 || *got["swas-test"] != (model.SWASTrafficUsage{Total: 20, Used: 25, Remaining: -5, Overflow: 5}) {
		t.Fatal("values recomputed or lost", got, err)
	}
	for _, mutation := range []func(map[string]any){
		func(r map[string]any) { delete(r, "TrafficOverflow") },
		func(r map[string]any) { r["TrafficUsed"] = 1.5 },
		func(r map[string]any) { r["TrafficUsed"] = -1 },
		func(r map[string]any) { r["TrafficOverflow"] = json.Number("9007199254740992") },
		func(r map[string]any) { r["InstanceId"] = "not-requested" },
		func(r map[string]any) { r["TrafficPackageRemaining"] = nil },
	} {
		bad := swasUsageRow("swas-test")
		mutation(bad)
		if _, err := parseSWASTraffic(map[string]any{"InstanceTrafficPackageUsages": []any{bad}}, []string{"swas-test"}); Code(err) != "swas_traffic_response_unrecognized" {
			t.Fatal("invalid traffic accepted", err)
		}
	}
	for _, body := range []map[string]any{
		{}, {"InstanceTrafficPackageUsages": nil},
		{"InstanceTrafficPackageUsages": []any{swasUsageRow("swas-test"), swasUsageRow("swas-test")}},
	} {
		if _, err := parseSWASTraffic(body, []string{"swas-test", "omitted"}); err == nil {
			t.Fatal("bad batch interpreted as empty")
		}
	}
}

func TestSWASTrafficBatchesIsolateFailuresAndFenceCalls(t *testing.T) {
	a := testAccount()
	instances := collection{}
	for i := 0; i < 51; i++ {
		instances.rows = append(instances.rows, map[string]any{"InstanceId": fmt.Sprintf("swas-%d", i), "PlanId": "plan"})
	}
	calls, fences, checks := 0, 0, 0
	c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls++
		var ids []string
		if json.Unmarshal([]byte(q["InstanceIds"]), &ids) != nil || len(ids) > 50 || action != "ListInstancesTrafficPackages" || region != "cn-shanghai" || q["RegionId"] != region || len(q) != 2 {
			t.Fatal("unexpected traffic request")
		}
		if calls == 1 {
			return nil, failure("permission_denied")
		}
		if len(ids) != 1 || ids[0] != "swas-50" {
			t.Fatal("batch scope changed")
		}
		return map[string]any{"InstanceTrafficPackageUsages": []any{swasUsageRow(ids[0])}}, nil
	})}
	before := func() error { fences++; return nil }
	observe := func(key string, _ error) {
		checks++
		if key != "swas_read" {
			t.Fatal(key)
		}
	}
	observations, present, complete := c.swasTraffic(context.Background(), a, fakeCredentials(), "cn-shanghai", instances, before, observe)
	if complete || calls != 2 || fences != 2 || checks != 2 || len(observations) != 51 || len(present) != 1 || !present["swas-50"] {
		t.Fatal("invalid batch result", calls, fences, checks)
	}
	for i, o := range observations {
		if i < 50 && (o.Error == nil || o.Error.Code != "permission_denied" || o.EntryPresent != nil || o.Usage != nil) {
			t.Fatal("failed read fabricated a package state")
		}
		if i == 50 && (o.Error != nil || o.EntryPresent == nil || !*o.EntryPresent || o.Usage == nil || o.PlanID != "plan" || o.QueriedAt.IsZero()) {
			t.Fatal(o)
		}
	}
	_, _, complete = c.swasTraffic(context.Background(), a, fakeCredentials(), "cn-shanghai", instances, func() error { return failure("account_changed") }, observe)
	if complete || calls != 2 {
		t.Fatal("lease failure reached cloud")
	}
	_, _, complete = c.swasTraffic(context.Background(), a, fakeCredentials(), "cn-shanghai", collection{}, before, observe)
	if !complete || calls != 2 || fences != 2 {
		t.Fatal("empty inventory queried packages")
	}
}

func TestSWASTrafficDiscoveryCarriesEmptyAndReportedObservations(t *testing.T) {
	for _, hasRow := range []bool{false, true} {
		c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
			if action == "ListInstancesTrafficPackages" && hasRow {
				return map[string]any{"InstanceTrafficPackageUsages": []any{swasUsageRow("swas-test")}}, nil
			}
			return inventoryFixture(ctx, cred, action, region, q)
		})}
		result := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, false, func() error { return nil })
		if result.Failure != "" || len(result.SWASTraffic) != 1 {
			t.Fatal("observation lost", result.Failure)
		}
		o := result.SWASTraffic[0]
		if o.Error != nil || o.EntryPresent == nil || *o.EntryPresent != hasRow || (o.Usage != nil) != hasRow || o.PlanID != "plan-test" {
			t.Fatal(o)
		}
	}
}

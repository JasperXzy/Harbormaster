package aliyun

import (
	"context"
	"fmt"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type callFunc func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error)

func (f callFunc) Call(ctx context.Context, c Credentials, a, r string, q map[string]string) (map[string]any, error) {
	return f(ctx, c, a, r, q)
}
func testAccount() model.Account {
	return model.Account{ID: "acct_test", Site: "china", RegionIDs: []string{"cn-shanghai"}}
}
func inventoryFixture(_ context.Context, _ Credentials, action, region string, _ map[string]string) (map[string]any, error) {
	switch action {
	case "DescribeRegions":
		return map[string]any{"Regions": map[string]any{"Region": []any{map[string]any{"RegionId": region}}}}, nil
	case "DescribeInstances":
		return map[string]any{"Instances": map[string]any{"Instance": []any{map[string]any{"InstanceId": "i-one", "InstanceName": "one", "Status": "Running", "InstanceChargeType": "PostPaid", "InstanceType": "ecs.test", "EipAddress": map[string]any{"AllocationId": "eip-one", "IpAddress": "203.0.113.10"}}, map[string]any{"InstanceId": "i-two", "InstanceName": "two", "Status": "Stopped", "InstanceChargeType": "PrePaid"}}}, "TotalCount": 2}, nil
	case "DescribeNetworkInterfaces":
		return map[string]any{"NetworkInterfaceSets": map[string]any{"NetworkInterfaceSet": []any{map[string]any{"NetworkInterfaceId": "eni-two", "InstanceId": "i-two"}}}, "TotalCount": 1}, nil
	case "DescribeEipAddresses":
		return map[string]any{"EipAddresses": map[string]any{"EipAddress": []any{map[string]any{"AllocationId": "eip-one", "InstanceId": "i-one", "BandwidthPackageId": "cbwp-test", "InternetChargeType": "PayByTraffic", "IpAddress": "203.0.113.10"}, map[string]any{"AllocationId": "eip-two", "InstanceId": "eni-two", "BandwidthPackageId": "cbwp-test", "InternetChargeType": "PayByTraffic", "IpAddress": "203.0.113.11"}}}, "TotalCount": 2}, nil
	case "DescribeCommonBandwidthPackages":
		return map[string]any{"CommonBandwidthPackages": map[string]any{"CommonBandwidthPackage": []any{map[string]any{"BandwidthPackageId": "cbwp-test", "Name": "shared", "InternetChargeType": "PayByDominantTraffic", "Bandwidth": "2000", "CreationTime": "2026-11-11T00:00:00Z", "PublicIpAddresses": map[string]any{"PublicIpAddresse": []any{map[string]any{"IpAddress": "203.0.113.10"}, map[string]any{"IpAddress": "203.0.113.11"}}}}}}, "TotalCount": 1}, nil
	case "ListInstances":
		return map[string]any{"Instances": []any{map[string]any{"InstanceId": "swas-test", "InstanceName": "light", "Status": "Running", "ChargeType": "PrePaid", "PlanId": "plan-test", "PublicIpAddress": "203.0.113.12", "ResourceSpec": map[string]any{"Bandwidth": 200}}}, "TotalCount": 1}, nil
	case "ListInstancesTrafficPackages":
		return map[string]any{"InstanceTrafficPackageUsages": []any{}}, nil
	case "DescribeMetricList":
		return map[string]any{"Success": true, "Datapoints": "[]"}, nil
	case "ListCdtInternetTraffic":
		return map[string]any{"TrafficDetails": []any{}}, nil
	case "QueryAccountBalance":
		return map[string]any{"Success": true, "Data": map[string]any{}}, nil
	default:
		return nil, failure("operation_not_allowed")
	}
}
func TestDiscoverySharedTopologyAndNoPackage(t *testing.T) {
	c := Inventory{Caller: callFunc(inventoryFixture)}
	r := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, true, func() error { return nil })
	if r.Failure != "" {
		t.Fatal(r.Failure)
	}
	instances, packages, subjects := 0, 0, 0
	for _, s := range r.Snapshots {
		switch s.Kind {
		case "ecs":
			instances++
			if s.Payload["billing_subject_kind"] != "cbwp_dominant_traffic" || s.Payload["bandwidth_mbps"] != int64(2000) || s.Payload["counts_toward_cdt"] != nil || s.Payload["realtime_protection"] != "unprotected" {
				t.Fatal("incorrect ECS topology", s.Payload)
			}
		case "swas":
			instances++
			if s.Payload["billing_subject_kind"] != "swas_no_traffic_package" || s.Payload["counts_toward_cdt"] != false {
				t.Fatal("empty package response not respected")
			}
		case "package":
			packages++
			if len(s.Payload["members"].([]map[string]any)) != 2 || s.Payload["settled_by_cdt"] != nil {
				t.Fatal("package eligibility fabricated")
			}
		case "subject":
			subjects++
		}
	}
	if instances != 3 || packages != 1 || subjects != 2 {
		t.Fatal(instances, packages, subjects)
	}
	for _, p := range r.Permissions.Items {
		if p.Key == "instance_power" || p.Key == "eip_manage" {
			if p.Result != "not_verified" {
				t.Fatal("write permission falsely verified")
			}
		} else if p.Result != "passed" {
			t.Fatal("read permission not aggregated", p)
		}
	}
}
func TestPartialCloudFailurePreservesUnknownTopology(t *testing.T) {
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, a, r string, q map[string]string) (map[string]any, error) {
		if a == "DescribeEipAddresses" || a == "ListInstancesTrafficPackages" {
			return nil, failure("permission_denied")
		}
		return inventoryFixture(ctx, cred, a, r, q)
	})}
	r := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, true, func() error { return nil })
	if r.Failure != "permission_denied" || r.CompleteScopes["subject:cn-shanghai"] {
		t.Fatal("failed scope declared complete")
	}
	for _, s := range r.Snapshots {
		if s.Kind == "ecs" && s.Payload["billing_subject_kind"] != "unknown" {
			t.Fatal("missing VPC data used as confirmed topology")
		}
		if s.Kind == "swas" && s.Payload["billing_subject_kind"] != "unknown" {
			t.Fatal("failed package query interpreted as no package")
		}
	}
}
func TestPaginationHonorsSharedBandwidthLimitAndRejectsDuplicates(t *testing.T) {
	calls := 0
	c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, _ string, q map[string]string) (map[string]any, error) {
		calls++
		if action != "DescribeCommonBandwidthPackages" || q["PageSize"] != "50" {
			t.Fatal("invalid page size")
		}
		items := []any{}
		if calls == 1 {
			for i := 0; i < 50; i++ {
				items = append(items, map[string]any{"BandwidthPackageId": fmt.Sprint(i)})
			}
		}
		return map[string]any{"CommonBandwidthPackages": map[string]any{"CommonBandwidthPackage": items}}, nil
	})}
	r := c.pages(context.Background(), fakeCredentials(), "DescribeCommonBandwidthPackages", "cn-shanghai", "CommonBandwidthPackages.CommonBandwidthPackage", 50, func() error { return nil })
	if r.err != nil || calls != 2 || len(r.rows) != 50 {
		t.Fatal("truncated pagination", calls, r.err)
	}
	dup := requiredID(collection{rows: []map[string]any{{"InstanceId": "same"}, {"InstanceId": "same"}}}, "InstanceId")
	if dup.err == nil {
		t.Fatal("duplicate cloud rows accepted as a complete scan")
	}
}
func TestMultipleExitsDoNotCollapseToOneBillingSubject(t *testing.T) {
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, a, r string, q map[string]string) (map[string]any, error) {
		body, err := inventoryFixture(ctx, cred, a, r, q)
		if a == "DescribeInstances" {
			v, _ := valueAt(body, "Instances.Instance")
			rows, _ := objects(v)
			rows[0]["PublicIpAddress"] = map[string]any{"IpAddress": []any{"203.0.113.20"}}
			rows[0]["InternetChargeType"] = "PayByBandwidth"
		}
		return body, err
	})}
	r := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, false, func() error { return nil })
	for _, s := range r.Snapshots {
		if s.Kind == "ecs" && s.Payload["cloud_instance_id"] == "i-one" {
			if len(s.Payload["billing_subject_ids"].([]string)) != 2 || s.Payload["billing_subject_id"] != nil || s.Payload["metering_rule"] != nil || s.Payload["counts_toward_cdt"] != nil {
				t.Fatal("multiple exits collapsed", s.Payload)
			}
			return
		}
	}
	t.Fatal("instance missing")
}

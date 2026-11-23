package aliyun

import (
	"context"
	"testing"
)

func TestIPv4EventCompletenessRequiresEverySource(t *testing.T) {
	for _, mode := range []string{"complete", "missing_instance_field", "eni_failed", "eip_failed", "malformed_eip", "malformed_embedded_eip", "malformed_swas"} {
		t.Run(mode, func(t *testing.T) {
			c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, query map[string]string) (map[string]any, error) {
				if (mode == "eni_failed" && action == "DescribeNetworkInterfaces" && query["InstanceId"] == "") || (mode == "eip_failed" && action == "DescribeEipAddresses") {
					return nil, failure("permission_denied")
				}
				body, err := inventoryFixture(ctx, cred, action, region, query)
				if action == "DescribeInstances" && mode != "missing_instance_field" {
					v, _ := valueAt(body, "Instances.Instance")
					rows, _ := objects(v)
					for _, row := range rows {
						row["PublicIpAddress"] = map[string]any{"IpAddress": []any{}}
						if mode == "malformed_embedded_eip" {
							row["EipAddress"] = map[string]any{"AllocationId": "eip-unresolved", "IpAddress": "invalid"}
						}
					}
				}
				if action == "DescribeEipAddresses" && mode == "malformed_eip" {
					v, _ := valueAt(body, "EipAddresses.EipAddress")
					rows, _ := objects(v)
					rows[0]["IpAddress"] = "invalid"
				}
				if action == "ListInstances" && mode == "malformed_swas" {
					rows, _ := objects(body["Instances"])
					rows[0]["NetworkAttributes"] = "invalid"
				}
				return body, err
			})}
			result := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, false, func() error { return nil })
			for _, snapshot := range result.Snapshots {
				if snapshot.Kind == "ecs" && snapshot.IPv4Complete != (mode == "complete" || mode == "malformed_swas") {
					t.Fatal("partial IPv4 topology can overwrite event baseline", mode, snapshot.ID)
				}
				if snapshot.Kind == "swas" && snapshot.IPv4Complete != (mode != "malformed_swas") {
					t.Fatal("SWAS completeness incorrect", mode)
				}
			}
		})
	}
}

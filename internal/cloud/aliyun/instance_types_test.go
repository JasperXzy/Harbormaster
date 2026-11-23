package aliyun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestInstanceTypesBatchesPaginationAndDeduplication(t *testing.T) {
	instances := collection{}
	for i := 0; i < 11; i++ {
		instances.rows = append(instances.rows, map[string]any{"InstanceType": fmt.Sprintf("ecs.test.%02d", i)})
	}
	instances.rows = append(instances.rows, instances.rows[0], map[string]any{})
	calls, fences := 0, 0
	c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, _ string, q map[string]string) (map[string]any, error) {
		calls++
		if action != "DescribeInstanceTypes" || q["MaxResults"] != "100" || q["PageNumber"] != "" || q["InstanceTypes.11"] != "" {
			t.Fatal(q)
		}
		rows := []any{}
		next := ""
		for i := 1; i <= 10; i++ {
			id := q[fmt.Sprintf("InstanceTypes.%d", i)]
			if id == "" {
				break
			}
			if calls == 1 && i > 1 {
				next = "page-two"
				continue
			}
			if calls == 2 && i == 1 {
				continue
			}
			rows = append(rows, map[string]any{"InstanceTypeId": id})
		}
		if calls == 2 && q["NextToken"] != "page-two" {
			t.Fatal("pagination token lost")
		}
		if calls == 3 && q["NextToken"] != "" {
			t.Fatal("token leaked across batches")
		}
		return map[string]any{"InstanceTypes": map[string]any{"InstanceType": rows}, "NextToken": next}, nil
	})}
	r := c.instanceTypes(context.Background(), fakeCredentials(), "cn-shanghai", instances, func() error { fences++; return nil })
	if r.err != nil || len(r.rows) != 11 || calls != 3 || fences != 3 {
		t.Fatal(r, calls, fences)
	}
	if r = c.instanceTypes(context.Background(), fakeCredentials(), "cn-shanghai", collection{}, func() error { t.Fatal("empty inventory queried catalog"); return nil }); r.err != nil || calls != 3 {
		t.Fatal(r, calls)
	}
	if r = c.instanceTypes(context.Background(), fakeCredentials(), "cn-shanghai", instances, func() error { return failure("account_changed") }); Code(r.err) != "account_changed" || calls != 3 {
		t.Fatal(r, calls)
	}
}

func TestInstanceTypesRejectPartialOrUnrequestedResults(t *testing.T) {
	for _, test := range []string{"duplicate", "unexpected", "empty_token_page", "invalid_token", "missing_list"} {
		t.Run(test, func(t *testing.T) {
			calls := 0
			c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, _, _ string, _ map[string]string) (map[string]any, error) {
				calls++
				row := map[string]any{"InstanceTypeId": "ecs.test"}
				body := map[string]any{"InstanceTypes": map[string]any{"InstanceType": []any{row}}}
				switch test {
				case "duplicate":
					body["NextToken"] = "same-token"
				case "unexpected":
					row["InstanceTypeId"] = "ecs.other"
				case "empty_token_page":
					body["InstanceTypes"] = map[string]any{"InstanceType": []any{}}
					body["NextToken"] = "next"
				case "invalid_token":
					body["NextToken"] = 42
				case "missing_list":
					delete(body, "InstanceTypes")
				}
				return body, nil
			})}
			r := c.instanceTypes(context.Background(), fakeCredentials(), "cn-shanghai", collection{rows: []map[string]any{{"InstanceType": "ecs.test"}}}, func() error { return nil })
			if r.err == nil || len(r.rows) != 0 || calls > 2 {
				t.Fatal("partial catalog accepted", r, calls)
			}
		})
	}
}

func TestCapacityUsesSharedCeilingAndKeepsUnknowns(t *testing.T) {
	for _, unavailable := range []string{"", "DescribeInstanceTypes", "DescribeEipAddresses", "DescribeCommonBandwidthPackages"} {
		t.Run(unavailable, func(t *testing.T) {
			c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
				if action == unavailable {
					return nil, failure("permission_denied")
				}
				body, err := inventoryFixture(ctx, cred, action, region, q)
				if action == "DescribeEipAddresses" {
					rows, _ := valueAt(body, "EipAddresses.EipAddress")
					for _, row := range rows.([]any) {
						row.(map[string]any)["Bandwidth"] = "5"
					}
				}
				return body, err
			})}
			r := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, true, func() error { return nil })
			if !r.CompleteScopes["ecs:cn-shanghai"] {
				t.Fatal("capacity query hid otherwise complete instance inventory")
			}
			for _, s := range r.Snapshots {
				if s.Kind == "swas" && s.Payload["network_capacity"] != nil {
					t.Fatal("SWAS spec fabricated")
				}
				if s.Kind != "ecs" {
					continue
				}
				cap := s.Payload["network_capacity"].(map[string]any)
				if text(s.Payload, "spec") == "" || unavailable != "" {
					if cap["public_outbound_exceeds_spec"] != nil {
						t.Fatal("unknown declared passing", cap)
					}
				} else if cap["public_outbound_mbps"] != int64(2000) || cap["public_outbound_exceeds_spec"] != true || cap["tx_kbps"] != int64(1000000) {
					t.Fatal("EIP old cap used instead of shared package", cap)
				}
				if unavailable == "DescribeInstanceTypes" && cap["last_error"].(*model.APIError).Code != "permission_denied" {
					t.Fatal(cap)
				}
			}
		})
	}
}

func TestCapacityComparisonBoundariesAndAmbiguousExit(t *testing.T) {
	for _, tc := range []struct {
		kind         string
		public, spec any
		want         any
	}{
		{"ecs_pay_by_traffic", int64(1000), int64(1000000), false},
		{"eip_pay_by_traffic", int64(1001), int64(1000999), true},
		{"cbwp_95", int64(1000), int64(1000999), false},
		{"ecs_fixed_bandwidth", int64(0), int64(1000000), false},
		{"cbwp_bandwidth", int64(2000), int64(0), nil},
		{"unknown", int64(2000), int64(1000000), nil},
		{"", nil, int64(1000000), nil},
	} {
		i := map[string]any{"spec": "ecs.test", "billing_subject_kind": tc.kind, "bandwidth_mbps": tc.public}
		attachNetworkCapacity([]model.Snapshot{{Kind: "ecs", Payload: i}}, collection{rows: []map[string]any{{"InstanceTypeId": "ecs.test", "InstanceBandwidthTx": tc.spec}}}, time.Now())
		if got := i["network_capacity"].(map[string]any)["public_outbound_exceeds_spec"]; got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

func TestSignedInstanceTypeFilterAndReadOnlyParameters(t *testing.T) {
	calls := 0
	c := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Host != "ecs.cn-shanghai.aliyuncs.com" || header(r.Header, "x-acs-action") != "DescribeInstanceTypes" || r.URL.Query().Get("InstanceTypes.1") != "ecs.test" || header(r.Header, "Authorization") == "" {
			t.Fatal("incorrect signed catalog request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"InstanceTypes":{"InstanceType":[]}}`))}, nil
	}), 0)
	if _, err := c.Call(context.Background(), fakeCredentials(), "DescribeInstanceTypes", "cn-shanghai", map[string]string{"InstanceTypes.1": "ecs.test", "MaxResults": "100"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Endpoint", "InstanceTypes.11", "RegionId", "Action"} {
		if _, err := c.Call(context.Background(), fakeCredentials(), "DescribeInstanceTypes", "cn-shanghai", map[string]string{key: "invalid"}); Code(err) != "parameter_not_allowed" {
			t.Fatal(key, err)
		}
	}
	if calls != 1 {
		t.Fatal("unapproved parameter reached SDK transport")
	}
}

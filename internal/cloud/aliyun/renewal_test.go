package aliyun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestRenewalQueriesOnlySubscriptionIDsAndPaginates(t *testing.T) {
	for _, kind := range []string{"ecs", "swas"} {
		t.Run(kind, func(t *testing.T) {
			instances := collection{rows: []map[string]any{{"InstanceId": "payg", "ChargeType": "PostPaid"}, {"InstanceId": "unknown"}}}
			for i := 0; i < 101; i++ {
				instances.rows = append(instances.rows, map[string]any{"InstanceId": fmt.Sprintf("subscription-%03d", i), "ChargeType": "PrePaid"})
			}
			calls, fences := 0, 0
			c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
				calls++
				idParam, pageParam, idField := "InstanceId", "PageNumber", "InstanceId"
				if kind == "swas" {
					idParam, pageParam, idField = "InstanceIDs", "PageNum", "InstanceID"
					if action != "QueryAvailableInstances" || q["Region"] != "" || q["SubscriptionType"] != "Subscription" {
						t.Fatal(q)
					}
				} else if action != "DescribeInstanceAutoRenewAttribute" || q["RegionId"] != region {
					t.Fatal(q)
				}
				ids := strings.Split(q[idParam], ",")
				if len(ids) > 100 || strings.Contains(q[idParam], "payg") || strings.Contains(q[idParam], "unknown") {
					t.Fatal("wrong renewal filter", q)
				}
				page, _ := strconv.Atoi(q[pageParam])
				start := (page - 1) * 50
				end := min(start+50, len(ids))
				rows := []any{}
				for _, id := range ids[start:end] {
					rows = append(rows, map[string]any{idField: id, "Region": region, "SubscriptionType": "Subscription"})
				}
				if kind == "swas" {
					return map[string]any{"Success": true, "Data": map[string]any{"TotalCount": len(ids), "InstanceList": rows}}, nil
				}
				return map[string]any{"TotalCount": len(ids), "InstanceRenewAttributes": map[string]any{"InstanceRenewAttribute": rows}}, nil
			})}
			r := c.renewals(context.Background(), fakeCredentials(), "cn-shanghai", kind, instances, func() error { fences++; return nil })
			if r.err != nil || len(r.rows) != 101 || calls != 3 || fences != 3 {
				t.Fatal(r.err, len(r.rows), calls, fences)
			}
			c.renewals(context.Background(), fakeCredentials(), "cn-shanghai", kind, collection{rows: instances.rows[:2]}, func() error { t.Fatal("non-subscription query"); return nil })
			if calls != 3 {
				t.Fatal("non-subscription instances queried")
			}
		})
	}
}

func TestRenewalIdentityAndMissingRecords(t *testing.T) {
	for _, outcome := range []string{"empty", "wrong_region", "wrong_charge", "extra_id", "duplicate", "no_total", "no_progress"} {
		t.Run(outcome, func(t *testing.T) {
			c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, _, _ string, _ map[string]string) (map[string]any, error) {
				row := map[string]any{"InstanceID": "swas-one", "Region": "cn-shanghai", "SubscriptionType": "Subscription"}
				rows := []any{row}
				var total any = 1
				switch outcome {
				case "empty":
					rows = []any{}
					total = 0
				case "wrong_region":
					row["Region"] = "cn-hongkong"
				case "wrong_charge":
					row["SubscriptionType"] = "PayAsYouGo"
				case "extra_id":
					row["InstanceID"] = "another"
				case "duplicate":
					rows = append(rows, row)
				case "no_total":
					total = nil
				case "no_progress":
					rows = []any{}
				}
				return map[string]any{"Success": true, "Data": map[string]any{"InstanceList": rows, "TotalCount": total}}, nil
			})}
			r := c.renewals(context.Background(), fakeCredentials(), "cn-shanghai", "swas", collection{rows: []map[string]any{{"InstanceId": "swas-one", "ChargeType": "PrePaid"}}}, func() error { return nil })
			if outcome == "empty" {
				if r.err != nil || len(r.rows) != 0 {
					t.Fatal(r)
				}
			} else if r.err == nil || len(r.rows) != 0 {
				t.Fatal("ambiguous source accepted", r)
			}
		})
	}
}

func TestRenewalStatesNeverInventDisabledOrSuccessfulPayment(t *testing.T) {
	for _, tc := range []struct {
		kind, state, want string
		enabled           any
	}{
		{"ecs", "AutoRenewal", "auto_renewal", true}, {"ecs", "Normal", "manual_renewal", false}, {"ecs", "NotRenewal", "no_renewal", false},
		{"ecs", "AutoRenewal", "unknown", false}, {"ecs", "future", "unknown", false}, {"ecs", "", "unknown", true},
		{"swas", "AutoRenewal", "auto_renewal", nil}, {"swas", "ManualRenewal", "manual_renewal", nil}, {"swas", "NotRenewal", "no_renewal", nil},
	} {
		p := map[string]any{"cloud_instance_id": "one", "charge_type": "subscription"}
		row := map[string]any{"InstanceId": "one", "InstanceID": "one", "RenewalStatus": tc.state, "RenewStatus": tc.state}
		if tc.enabled != nil {
			row["AutoRenewEnabled"] = tc.enabled
		}
		data := collection{rows: []map[string]any{row}}
		attachRenewals([]model.Snapshot{{Kind: tc.kind, Payload: p}}, data, data, time.Now())
		if p["renewal"].(map[string]any)["status"] != tc.want {
			t.Fatal(tc, p)
		}
	}
	for _, charge := range []string{"subscription", "pay_as_you_go", "spot", "unknown"} {
		p := map[string]any{"cloud_instance_id": "missing", "charge_type": charge}
		attachRenewals([]model.Snapshot{{Kind: "ecs", Payload: p}}, collection{}, collection{}, time.Now())
		want := "unknown"
		if charge == "pay_as_you_go" || charge == "spot" {
			want = "not_applicable"
		}
		if p["renewal"].(map[string]any)["status"] != want {
			t.Fatal(p)
		}
	}
}

func TestRenewalReadSigningAndWriteGate(t *testing.T) {
	for _, action := range []string{"DescribeInstanceAutoRenewAttribute", "QueryAvailableInstances"} {
		q := map[string]string{"RegionId": "cn-shanghai", "InstanceId": "i-test", "PageNumber": "1", "PageSize": "100"}
		if action == "QueryAvailableInstances" {
			q = map[string]string{"InstanceIDs": "swas-test", "SubscriptionType": "Subscription", "PageNum": "1", "PageSize": "100"}
		}
		c := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
			if header(r.Header, "x-acs-action") != action || r.Method != "GET" || header(r.Header, "Authorization") == "" {
				t.Fatal("unsigned renewal read")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Success":true,"Data":{}}`))}, nil
		}), 0)
		if _, err := c.Call(context.Background(), fakeCredentials(), action, "cn-shanghai", q); err != nil {
			t.Fatal(err)
		}
		for _, write := range []string{"RenewInstance", "ModifyInstanceAutoRenewAttribute", "SetRenewal"} {
			if _, err := c.Call(context.Background(), fakeCredentials(), write, "cn-shanghai", nil); Code(err) != "operation_not_allowed" {
				t.Fatal("renewal write escaped gate", write)
			}
		}
	}
}

func TestExpiredOrLockedSubscriptionInstancesDoNotPoisonRenewalOrAccountScan(t *testing.T) {
	now := time.Date(2026, 11, 20, 18, 50, 0, 0, time.UTC)
	instances := collection{rows: []map[string]any{
		{"InstanceId": "i-expired", "InstanceChargeType": "PrePaid", "ExpiredTime": "2026-11-20T16:00Z"},
		{"InstanceId": "i-locked", "InstanceChargeType": "PrePaid", "ExpiredTime": "2026-12-01T16:00Z", "OperationLocks": map[string]any{"LockReason": []any{map[string]any{"LockReason": "Expired"}}}},
		{"InstanceId": "i-active", "InstanceChargeType": "PrePaid", "ExpiredTime": "2026-12-20T16:00Z"},
	}}
	queried := ""
	c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, _ string, q map[string]string) (map[string]any, error) {
		if action != "DescribeInstanceAutoRenewAttribute" {
			t.Fatal(action)
		}
		queried = q["InstanceId"]
		return map[string]any{"TotalCount": 1, "InstanceRenewAttributes": map[string]any{"InstanceRenewAttribute": []any{map[string]any{"InstanceId": "i-active", "RenewalStatus": "AutoRenewal", "AutoRenewEnabled": true}}}}, nil
	})}
	res := c.renewalsAt(context.Background(), fakeCredentials(), "cn-shanghai", "ecs", instances, now, func() error { return nil })
	if res.err != nil || queried != "i-active" || len(res.rows) != 1 {
		t.Fatal("expired or locked instances were not skipped:", res.err, queried, res.rows)
	}

	// Auxiliary non-auth renewal errors stay attached to instance.renewal without failing the whole account scan.
	scanInv := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		if action == "DescribeInstanceAutoRenewAttribute" {
			return nil, failure("aliyun_unreachable")
		}
		return inventoryFixture(ctx, cred, action, region, q)
	})}
	scanRes := scanInv.Scan(context.Background(), testAccount(), fakeCredentials().Credential, true, func() error { return nil })
	if scanRes.Failure != "" {
		t.Fatal("auxiliary renewal error poisoned account scan:", scanRes.Failure)
	}
	for _, p := range scanRes.Permissions.Items {
		if p.Key == "ecs_read" && p.Result != "passed" {
			t.Fatal("ecs_read poisoned by auxiliary renewal error:", p)
		}
	}
}

package aliyun

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func instanceBillPage(token string) map[string]any {
	return map[string]any{"Success": true, "Data": map[string]any{"BillingCycle": "2026-11", "NextToken": token, "Items": []any{map[string]any{"InstanceID": "swas-example", "Item": "SubscriptionOrder", "ProductCode": "ace_eweb", "PipCode": "swas", "ProductName": "SWAS", "Currency": "CNY", "PretaxAmount": json.Number("9007199254740993.123456789012"), "AfterDiscountAmount": json.Number("9007199254740994.1")}}}}
}
func TestInstanceBillsCompleteOrNothing(t *testing.T) {
	for _, mode := range []string{"ok", "failure", "repeat", "limit", "wrong_month", "missing_rows", "bad_amount", "float_amount", "partial", "changing_total", "bad_token", "cancel", "lease"} {
		t.Run(mode, func(t *testing.T) {
			calls, fences := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
				calls++
				if action != "DescribeInstanceBill" || q["IsBillingItem"] != "false" || q["Granularity"] != "MONTHLY" || q["MaxResults"] != "300" || region != "cn-shanghai" {
					t.Fatal("request shape")
				}
				if calls == 1 && q["NextToken"] != "" || calls == 2 && q["NextToken"] != "opaque-1" {
					t.Fatal("cursor")
				}
				if mode == "failure" && calls == 2 {
					return nil, failure("permission_denied")
				}
				token := fmt.Sprintf("opaque-%d", calls)
				if calls == 2 && mode != "limit" {
					token = ""
				}
				if mode == "repeat" {
					token = "opaque-1"
				}
				b := instanceBillPage(token)
				d := child(b, "Data")
				if mode == "wrong_month" {
					d["BillingCycle"] = "2026-10"
				}
				if mode == "missing_rows" {
					delete(d, "Items")
				}
				if mode == "bad_token" {
					d["NextToken"] = true
				}
				if mode == "bad_amount" {
					d["Items"].([]any)[0].(map[string]any)["PretaxAmount"] = "NaN"
				}
				if mode == "float_amount" {
					d["Items"].([]any)[0].(map[string]any)["PretaxAmount"] = 0.1
				}
				if mode == "partial" {
					d["TotalCount"] = json.Number("3")
				}
				if mode == "changing_total" {
					d["TotalCount"] = json.Number(fmt.Sprint(calls + 1))
				}
				if mode == "cancel" {
					cancel()
				}
				return b, nil
			})}
			result, err := reader.instanceBills(ctx, fakeCredentials(), "2026-11", func() error {
				fences++
				if mode == "lease" && fences == 2 {
					return failure("account_changed")
				}
				return nil
			})
			if mode == "ok" {
				if err != nil || len(result.Items) != 2 || result.Items[0].Pretax != "9007199254740993.123456789012" || result.Items[1].PipCode != "swas" || fences != 2 {
					t.Fatal(result, err)
				}
			} else if err == nil || result != nil {
				t.Fatal("partial/invalid published", result, err)
			}
			if mode == "limit" && calls != 50 {
				t.Fatal(calls)
			}
			if (mode == "cancel" || mode == "lease") && calls != 1 {
				t.Fatal("lost fence reached cloud", calls)
			}
		})
	}
}
func TestInstanceBillsEmptyAndAbsentTerminalToken(t *testing.T) {
	c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
		return map[string]any{"Success": true, "Data": map[string]any{"BillingCycle": "2026-11", "Items": []any{}, "TotalCount": json.Number("0")}}, nil
	})}
	result, err := c.instanceBills(context.Background(), fakeCredentials(), "2026-11", func() error { return nil })
	if err != nil || result == nil || result.Items == nil || len(result.Items) != 0 {
		t.Fatal(result, err)
	}
}

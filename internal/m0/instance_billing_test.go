package m0

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInstanceBillingPagination(t *testing.T) {
	for _, mode := range []string{"complete", "repeat", "limit", "shape", "partial", "failure"} {
		t.Run(mode, func(t *testing.T) {
			p := exampleProfile()
			ops, err := plan(p, queryOptions{Check: "instance-billing", Month: "2026-11"})
			if err != nil || len(ops) != 1 || ops[0].validate(p) != nil {
				t.Fatal("invalid plan", err)
			}
			q := ops[0].Query
			if q["Granularity"] != "MONTHLY" || q["IsBillingItem"] != "false" || q["MaxResults"] != "300" {
				t.Fatal(q)
			}
			calls := 0
			results := execute(context.Background(), p, ops, func(_ context.Context, _ profile, o operation) (map[string]any, string) {
				calls++
				if calls == 1 && o.Query["NextToken"] != "" {
					t.Fatal("initial token")
				}
				if calls == 2 && o.Query["NextToken"] != "opaque-1" {
					t.Fatal("token not followed")
				}
				if calls == 2 && mode == "failure" {
					return nil, "Forbidden"
				}
				token := fmt.Sprintf("opaque-%d", calls)
				if calls == 2 && mode == "complete" {
					token = ""
				}
				if mode == "repeat" {
					token = "opaque-1"
				}
				d := map[string]any{"Items": []any{map[string]any{"InstanceID": "i-example"}}, "NextToken": token}
				if mode == "shape" {
					d["NextToken"] = 123
				}
				if mode == "partial" {
					d["NextToken"] = ""
					d["TotalCount"] = json.Number("2")
				}
				return map[string]any{"Success": true, "Data": d}, ""
			}, 0)
			r := results[0]
			if mode == "complete" {
				if !r.Complete || calls != 2 || r.ErrorCode != "" {
					t.Fatal(r)
				}
				return
			}
			expected := map[string]string{"repeat": "pagination_token_repeated", "limit": "page_limit_reached", "shape": "pagination_shape_unknown", "partial": "pagination_incomplete", "failure": "Forbidden"}[mode]
			if r.Complete || r.ErrorCode != expected {
				t.Fatal(r)
			}
		})
	}
	if _, err := plan(exampleProfile(), queryOptions{Check: "instance-billing", Month: "November"}); err == nil {
		t.Fatal("invalid month accepted")
	}
}

func TestInstanceBillingSignedReadPreservesMoney(t *testing.T) {
	p := exampleProfile()
	ops, _ := plan(p, queryOptions{Check: "instance-billing", Month: "2026-11"})
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "business.aliyuncs.com" || sdkHeader(r.Header, "x-acs-action") != "DescribeInstanceBill" {
			t.Fatal("incorrect read")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"Success":true,"Data":{"Items":[{"PretaxAmount":9007199254740993.123456789012}],"TotalCount":1}}`))}, nil
	})}
	b, code := (sdkCaller{h}).call(context.Background(), p, ops[0])
	if code != "" {
		t.Fatal(code)
	}
	raw, _ := atPath(b, "Data.Items")
	amount := raw.([]any)[0].(map[string]any)["PretaxAmount"]
	if amount != json.Number("9007199254740993.123456789012") {
		t.Fatal("money changed", amount)
	}
}

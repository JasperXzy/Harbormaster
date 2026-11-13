package m0

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestCreditSpecsProbeIsFixedSignedReadAndFollowsTokens(t *testing.T) {
	p := exampleProfile()
	p.Regions = []string{"cn-shanghai"}
	ops, err := plan(p, queryOptions{Check: "ecs-credit-specs"})
	if err != nil || len(ops) != 1 || ops[0].validate(p) != nil {
		t.Fatal("credit catalog plan", err)
	}
	o := ops[0]
	if o.Action != "DescribeInstanceTypes" || o.PageKey != "NextToken" || o.ItemsPath != "InstanceTypes.InstanceType" || o.Query["MaxResults"] != "100" || len(o.Query) != 5 {
		t.Fatal(o)
	}
	for i, id := range []string{"ecs.t5-lc2m1.nano", "ecs.t5-c1m1.large", "ecs.t6-c2m1.large", "ecs.t6-c1m1.large"} {
		if o.Query["InstanceTypes."+strconv.Itoa(i+1)] != id {
			t.Fatal("unscoped catalog request")
		}
	}
	calls := 0
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Host != "ecs.cn-shanghai.aliyuncs.com" || sdkHeader(r.Header, "x-acs-action") != "DescribeInstanceTypes" || sdkHeader(r.Header, "Authorization") == "" || r.URL.Query().Get("InstanceTypes.1") != "ecs.t5-lc2m1.nano" {
			t.Fatal("invalid catalog request")
		}
		token := ""
		if calls == 1 {
			token = "next"
		} else if r.URL.Query().Get("NextToken") != "next" {
			t.Fatal("lost cursor")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"InstanceTypes":{"InstanceType":[{"InstanceTypeId":"ecs.t6-c2m1.large","BaselineCredit":12,"InitialCredit":60,"CpuCoreCount":2}]},"NextToken":"` + token + `"}`))}, nil
	})}
	results := execute(context.Background(), p, ops, (sdkCaller{h}).call, 0)
	if calls != 2 || len(results) != 1 || !results[0].Complete || results[0].Status != "passed" {
		t.Fatal(results)
	}
	for _, key := range []string{"Action", "Endpoint", "InstanceTypes.5", "InstanceTypeFamily", "DryRun"} {
		bad := o
		bad.Query = map[string]string{key: "invalid"}
		if _, code := (sdkCaller{h}).call(context.Background(), p, bad); code != "operation_not_allowed" || calls != 2 {
			t.Fatal("parameter escaped allowlist", code)
		}
	}
	o.Action = "RunInstances"
	if _, code := (sdkCaller{h}).call(context.Background(), p, o); code != "operation_not_allowed" || calls != 2 {
		t.Fatal("cloud write escaped gate", code)
	}
	p.Regions = append(p.Regions, "cn-hongkong")
	if _, err = plan(p, queryOptions{Check: "ecs-credit-specs"}); err == nil {
		t.Fatal("catalog probe silently expanded regions")
	}
}

func TestCreditSpecsRejectsMalformedAndRepeatedTopLevelCursors(t *testing.T) {
	for _, mode := range []string{"malformed", "repeated"} {
		t.Run(mode, func(t *testing.T) {
			p := exampleProfile()
			ops, err := plan(p, queryOptions{Check: "ecs-credit-specs"})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			got := execute(context.Background(), p, ops, func(context.Context, profile, operation) (map[string]any, string) {
				calls++
				var token any = "repeated"
				if mode == "malformed" {
					token = 123
				}
				return map[string]any{"InstanceTypes": map[string]any{"InstanceType": []any{map[string]any{"InstanceTypeId": "ecs.t6-c2m1.large"}}}, "NextToken": token}, ""
			}, 0)
			want := "pagination_shape_unknown"
			count := 1
			if mode == "repeated" {
				want = "pagination_token_repeated"
				count = 2
			}
			if len(got) != 1 || got[0].Complete || got[0].ErrorCode != want || calls != count {
				t.Fatal(got, calls)
			}
		})
	}
}

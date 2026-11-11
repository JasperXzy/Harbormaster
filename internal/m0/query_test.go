package m0

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSDKUsesSignedReadOnlyHTTPS(t *testing.T) {
	p := exampleProfile()
	o := newOperation(p, "DescribeInstances", p.Regions[0], map[string]string{"RegionId": p.Regions[0], "PageNumber": "1", "PageSize": "100"}, "PageNumber", "Instances.Instance")
	calls := 0
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "ecs.cn-hongkong.aliyuncs.com" || sdkHeader(r.Header, "x-acs-action") != "DescribeInstances" {
			t.Fatal("incorrect SDK request")
		}
		if sdkHeader(r.Header, "Authorization") == "" && r.URL.Query().Get("Signature") == "" {
			t.Fatal("unsigned SDK request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"RequestId":"test","TotalCount":0,"Instances":{"Instance":[]}}`))}, nil
	})}
	body, code := (sdkCaller{h}).call(context.Background(), p, o)
	if code != "" || body == nil || calls != 1 {
		t.Fatalf("SDK response: %s calls=%d", code, calls)
	}
	o.Action = "StopInstance"
	if _, code = (sdkCaller{h}).call(context.Background(), p, o); code != "operation_not_allowed" || calls != 1 {
		t.Fatal("write reached transport")
	}
}

func TestEgressGateAndNoRawErrors(t *testing.T) {
	p := exampleProfile()
	o := newOperation(p, "DescribeRegions", p.Regions[0], map[string]string{}, "", "")
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New(p.AccessKeySecret) })}
	if _, code := (sdkCaller{h}).call(context.Background(), p, o); code != "request_failed" {
		t.Fatal("raw transport error exposed")
	}
	for _, target := range []string{"http://" + o.Endpoint + "/", "https://example.com/", "https://" + o.Endpoint + "/wrong"} {
		r, _ := http.NewRequest("GET", target, nil)
		r.Header.Set("x-acs-action", o.Action)
		r.Header.Set("x-acs-version", o.Version)
		if _, err := (gatedHTTP{h, o}).Call(r, nil); err == nil || err.Error() != "request_not_allowed" {
			t.Fatal("bad destination accepted")
		}
	}
	o.Query["Action"] = "StopInstance"
	if o.validate(p) == nil {
		t.Fatal("Action override accepted")
	}
}

func TestPaginationAndSWASFollowup(t *testing.T) {
	p := exampleProfile()
	o := newOperation(p, "ListInstances", p.Regions[0], map[string]string{"RegionId": p.Regions[0], "PageNumber": "1", "PageSize": "100"}, "PageNumber", "Instances")
	pages := 0
	trafficCalls := 0
	call := func(_ context.Context, _ profile, op operation) (map[string]any, string) {
		if op.Action == "ListInstancesTrafficPackages" {
			trafficCalls++
			var ids []string
			_ = json.Unmarshal([]byte(op.Query["InstanceIds"]), &ids)
			if len(ids) > 50 {
				t.Fatal("SWAS batch too large")
			}
			return map[string]any{"InstanceTrafficPackageUsages": []any{}}, ""
		}
		pages++
		items := []any{}
		if op.Query["PageNumber"] == "1" {
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{"InstanceId": "test-swas"})
			}
		}
		return map[string]any{"Instances": items}, ""
	}
	r := execute(context.Background(), p, []operation{o}, call, 0)
	if pages != 2 || trafficCalls != 2 || len(r) != 3 || !r[0].Complete {
		t.Fatal("pagination/followup failed")
	}
	bad := func(context.Context, profile, operation) (map[string]any, string) { return map[string]any{}, "" }
	if got := execute(context.Background(), p, []operation{o}, bad, 0); got[0].Complete || got[0].ErrorCode != "pagination_shape_unknown" {
		t.Fatal("missing list treated as empty")
	}
}

func TestResponseRedaction(t *testing.T) {
	p := exampleProfile()
	input := map[string]any{"AccessKeyId": p.AccessKeyID, "AccessKeySecret": p.AccessKeySecret, "SecurityToken": p.SecurityToken, "Message": "request included " + p.AccessKeySecret, "InstanceName": "prefix " + p.AccessKeySecret, "Nested": []any{map[string]any{"Authorization": "unknown-auth-secret", "UserData": "encoded-secret"}}, "url": "https://user:password@example.com/path?token=something"}
	b, _ := json.Marshal(scrub(input, p))
	for _, s := range []string{p.AccessKeyID, p.AccessKeySecret, p.SecurityToken, "unknown-auth-secret", "encoded-secret", "user:password", "token=something"} {
		if strings.Contains(string(b), s) {
			t.Fatal("response leaked credential")
		}
	}
}

func TestBandwidthPackagePageSize(t *testing.T) {
	p := exampleProfile()
	ops, err := plan(p, queryOptions{Check: "inventory"})
	if err != nil {
		t.Fatal(err)
	}
	var op operation
	for _, o := range ops {
		if o.Action == "DescribeCommonBandwidthPackages" {
			op = o
		}
	}
	if op.Query["PageSize"] != "50" {
		t.Fatal("API supports at most 50 rows")
	}
	calls := 0
	call := func(context.Context, profile, operation) (map[string]any, string) {
		calls++
		items := []any{}
		if calls == 1 {
			for i := 0; i < 50; i++ {
				items = append(items, map[string]any{})
			}
		}
		return map[string]any{"CommonBandwidthPackages": map[string]any{"CommonBandwidthPackage": items}}, ""
	}
	r := execute(context.Background(), p, []operation{op}, call, 0)
	if calls != 2 || !r[0].Complete {
		t.Fatal("50-row page prematurely completed")
	}
}

func TestCDTIsolatedReadProbe(t *testing.T) {
	p := exampleProfile()
	p.Regions = []string{"cn-shanghai"}
	ops, err := plan(p, queryOptions{Check: "cdt"})
	if err != nil || len(ops) != 1 {
		t.Fatal("CDT plan")
	}
	o := ops[0]
	if o.validate(p) != nil || o.Action != "ListCdtInternetTraffic" || o.Region != "cn-hongkong" {
		t.Fatal("CDT read scope")
	}
	calls := 0
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Host != "cdt.aliyuncs.com" || r.URL.Query().Get("Action") != "ListCdtInternetTraffic" || r.URL.Query().Get("Signature") == "" {
			t.Fatal("CDT SDK request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"TrafficDetails":[]}`))}, nil
	})}
	if _, code := (sdkCaller{h}).call(context.Background(), p, o); code != "" || calls != 1 {
		t.Fatal("CDT signed read failed", code)
	}
	o.Action = "OpenCdtService"
	if _, code := (sdkCaller{h}).call(context.Background(), p, o); code != "operation_not_allowed" || calls != 1 {
		t.Fatal("CDT write escaped gate")
	}
}

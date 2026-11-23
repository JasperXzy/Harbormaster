package m0

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCDTServiceProbeIsSignedAndReadOnly(t *testing.T) {
	p := exampleProfile()
	p.Regions = []string{"cn-shanghai"}
	ops, err := plan(p, queryOptions{Check: "cdt-service"})
	if err != nil || len(ops) != 3 {
		t.Fatal("CDT service plan", err)
	}
	reads := map[string]bool{"ListCdtTrafficTiers": true, "GetCdtInternetServiceStatus": true, "ListSwitchedCdtProducts": true}
	calls := 0
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != "cdt.aliyuncs.com" || !reads[r.URL.Query().Get("Action")] || r.URL.Query().Get("Signature") == "" || r.URL.Query().Get("RegionId") != "cn-hongkong" {
			t.Fatal("unexpected CDT service request")
		}
		if r.URL.Query().Get("Action") != "GetCdtInternetServiceStatus" && r.URL.Query().Get("BusinessRegionId") != "cn-shanghai" {
			t.Fatal("lost selected business region")
		}
		body := `{"RequestId":"test","Enabled":true}`
		if r.URL.Query().Get("Action") == "ListCdtTrafficTiers" {
			body = `{"RequestId":"test","TrafficTiers":[]}`
		} else if r.URL.Query().Get("Action") == "ListSwitchedCdtProducts" {
			body = `{"RequestId":"test","CdtProducts":[]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, op := range ops {
		if !reads[op.Action] || op.validate(p) != nil {
			t.Fatal("incorrect service scope")
		}
	}
	got := execute(context.Background(), p, ops, (sdkCaller{h}).call, 0)
	if len(got) != 3 || calls != 3 {
		t.Fatal("incorrect read count", calls)
	}
	for _, result := range got {
		if !result.Complete || result.Status != "passed" {
			t.Fatal("isolated read rejected", result.ErrorCode)
		}
		if result.Action != "GetCdtInternetServiceStatus" && result.BusinessRegion != "cn-shanghai" {
			t.Fatal("report lost business region")
		}
	}
	for _, action := range []string{"OpenCdtInternetService", "OpenCdtService", "SwitchToCdt", "RollbackFromCdt"} {
		bad := ops[0]
		bad.Action = action
		if _, code := (sdkCaller{h}).call(context.Background(), p, bad); code != "operation_not_allowed" || calls != 3 {
			t.Fatal("CDT write escaped gate")
		}
	}
	for _, key := range []string{"Action", "Endpoint", "DryRun", "NextToken", "ProductCode"} {
		bad := ops[0]
		bad.Query = map[string]string{key: "unexpected"}
		if _, code := (sdkCaller{h}).call(context.Background(), p, bad); code != "operation_not_allowed" || calls != 3 {
			t.Fatal("additional parameter escaped gate")
		}
	}
	bad := ops[1]
	bad.Query = map[string]string{"RegionId": "cn-hongkong", "BusinessRegionId": "eu-central-1"}
	if _, code := (sdkCaller{h}).call(context.Background(), p, bad); code != "operation_not_allowed" || calls != 3 {
		t.Fatal("business region escaped imported scope")
	}
	p.Site = "international"
	if _, err = plan(p, queryOptions{Check: "cdt-service"}); err == nil {
		t.Fatal("unverified international endpoint accepted")
	}
}

func TestCDTServiceProbePreservesTrafficAndPaginationGuards(t *testing.T) {
	p := exampleProfile()
	h := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"RequestId":"test","Enabled":true,"TrafficTiers":[],"CdtProducts":[],"NextToken":"unverified-next-page"}`))}, nil
	})}
	traffic, err := plan(p, queryOptions{Check: "cdt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, code := (sdkCaller{h}).call(context.Background(), p, traffic[0]); code != "cdt_response_shape_unknown" {
		t.Fatal("lost TrafficDetails guard", code)
	}
	ops, err := plan(p, queryOptions{Check: "cdt-service"})
	if err != nil {
		t.Fatal(err)
	}
	got := execute(context.Background(), p, ops, (sdkCaller{h}).call, 0)
	for _, result := range got {
		if result.Complete || result.Status != "check_failed" || result.ErrorCode != "continuation_not_followed" {
			t.Fatal("unverified continuation marked complete")
		}
	}
	malformed := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Enabled":"true","TrafficTiers":null,"CdtProducts":{}}`))}, nil
	})}
	for _, op := range ops {
		if _, code := (sdkCaller{malformed}).call(context.Background(), p, op); code != "cdt_service_response_shape_unknown" {
			t.Fatal("malformed service response accepted", op.Action, code)
		}
	}
}

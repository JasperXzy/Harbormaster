package m0

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestIdleResourceProbeUsesSignedReadsAndDiskCursor(t *testing.T) {
	p := exampleProfile()
	p.Regions = []string{"cn-shanghai", "cn-hongkong"}
	ops, err := plan(p, queryOptions{Check: "idle-resources"})
	if err != nil || len(ops) != 6 {
		t.Fatal("idle probe plan", err)
	}
	calls := 0
	diskPages := map[string]int{}
	h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		action, region := sdkHeader(r.Header, "x-acs-action"), r.URL.Query().Get("RegionId")
		if r.Method != "GET" || r.URL.Scheme != "https" || sdkHeader(r.Header, "Authorization") == "" || region != "cn-shanghai" && region != "cn-hongkong" {
			t.Fatal("unexpected request shape")
		}
		body := ""
		switch action {
		case "DescribeInstances":
			body = `{"Instances":{"Instance":[]},"TotalCount":0}`
		case "DescribeEipAddresses":
			if r.URL.Host != "vpc."+region+".aliyuncs.com" {
				t.Fatal("unexpected VPC endpoint")
			}
			body = `{"EipAddresses":{"EipAddress":[]},"TotalCount":0}`
		case "DescribeDisks":
			diskPages[region]++
			if r.URL.Host != "ecs."+region+".aliyuncs.com" || r.URL.Query().Get("MaxResults") != "100" || r.URL.Query().Has("PageNumber") {
				t.Fatal("unexpected disk pagination")
			}
			if diskPages[region] == 1 {
				if r.URL.Query().Has("NextToken") {
					t.Fatal("unexpected initial cursor")
				}
				body = `{"Disks":{"Disk":[{"DiskId":"d-first"}]},"TotalCount":0,"NextToken":"disk-next"}`
			} else {
				if diskPages[region] != 2 || r.URL.Query().Get("NextToken") != "disk-next" {
					t.Fatal("lost or repeated cursor")
				}
				// A credential-shaped provider value must still be redacted before
				// the private report is written, even for the newly allowed read.
				encoded, _ := json.Marshal(map[string]any{"Disks": map[string]any{"Disk": []any{map[string]any{"DiskId": "d-second", "DiskName": p.AccessKeySecret}}}, "TotalCount": 0})
				body = string(encoded)
			}
		default:
			t.Fatal("unexpected action")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for _, op := range ops {
		if err = op.validate(p); err != nil {
			t.Fatal(err)
		}
	}
	got := execute(context.Background(), p, ops, (sdkCaller{h}).call, 0)
	if len(got) != 6 || calls != 8 || diskPages["cn-shanghai"] != 2 || diskPages["cn-hongkong"] != 2 {
		t.Fatal("incomplete regional probe", calls)
	}
	for _, result := range got {
		if !result.Complete || result.Status != "passed" {
			t.Fatal("incomplete read", result.Action, result.ErrorCode)
		}
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), p.AccessKeySecret) || strings.Contains(string(raw), "disk-next") {
		t.Fatal("unscrubbed report")
	}
	for _, action := range []string{"DeleteDisk", "ReleaseEipAddress", "RunInstances"} {
		bad := ops[1]
		bad.Action = action
		if _, code := (sdkCaller{h}).call(context.Background(), p, bad); code != "operation_not_allowed" || calls != 8 {
			t.Fatal("write escaped probe gate")
		}
	}
	for _, key := range []string{"Action", "Endpoint", "DryRun", "DiskIds", "PageNumber"} {
		bad := ops[1]
		bad.Query = map[string]string{key: "unexpected"}
		if _, code := (sdkCaller{h}).call(context.Background(), p, bad); code != "operation_not_allowed" || calls != 8 {
			t.Fatal("extra parameter escaped probe gate")
		}
	}
}

func TestIdleDiskProbeRejectsIncompletePagination(t *testing.T) {
	for _, mode := range []string{"malformed", "repeated", "never_finished"} {
		t.Run(mode, func(t *testing.T) {
			p := exampleProfile()
			ops, err := plan(p, queryOptions{Check: "idle-resources"})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			got := execute(context.Background(), p, ops[1:2], func(context.Context, profile, operation) (map[string]any, string) {
				calls++
				var token any = "same"
				if mode == "malformed" {
					token = 123
				} else if mode == "never_finished" {
					token = "page-" + strconv.Itoa(calls)
				}
				return map[string]any{"Disks": map[string]any{"Disk": []any{map[string]any{"DiskId": "d-test"}}}, "TotalCount": 3, "NextToken": token}, ""
			}, 0)
			want := map[string]string{"malformed": "pagination_shape_unknown", "repeated": "pagination_token_repeated", "never_finished": "page_limit_reached"}[mode]
			count := map[string]int{"malformed": 1, "repeated": 2, "never_finished": 50}[mode]
			if len(got) != 1 || got[0].Complete || got[0].Status != "check_failed" || got[0].ErrorCode != want || calls != count {
				t.Fatal("invalid page marked complete", got, calls)
			}
		})
	}
}

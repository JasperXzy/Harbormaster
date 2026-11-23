package aliyun

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeCredentials() Credentials {
	return Credentials{Credential: model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "test-secret-not-real-at-all"}, Site: "china", Regions: []string{"cn-shanghai"}}
}
func TestSignedReadOnlyGateAndSafeErrors(t *testing.T) {
	cred := fakeCredentials()
	calls := 0
	c := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "ecs.cn-shanghai.aliyuncs.com" || r.Method != "GET" || header(r.Header, "x-acs-action") != "DescribeInstances" {
			t.Fatal("incorrect request")
		}
		if header(r.Header, "Authorization") == "" && r.URL.Query().Get("Signature") == "" {
			t.Fatal("unsigned read")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"Instances":{"Instance":[]},"TotalCount":0}`))}, nil
	}), 0)
	if _, err := c.Call(context.Background(), cred, "DescribeInstances", "cn-shanghai", map[string]string{"RegionId": "cn-shanghai", "PageNumber": "1", "PageSize": "100"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"StartInstance", "StopInstance", "CreateInstance", "DeleteInstance", "AllocateEipAddress", "RunCommand", "OpenCdtService"} {
		if _, err := c.Call(context.Background(), cred, action, "cn-shanghai", nil); Code(err) != "operation_not_allowed" {
			t.Fatal("write action escaped allowlist", action)
		}
	}
	if _, err := c.Call(context.Background(), cred, "DescribeInstances", "cn-hongkong", nil); Code(err) != "region_not_enabled" {
		t.Fatal("unconfigured region accepted")
	}
	if _, err := c.Call(context.Background(), cred, "DescribeInstances", "cn-shanghai", map[string]string{"Endpoint": "evil.example"}); Code(err) != "parameter_not_allowed" {
		t.Fatal("arbitrary parameter accepted")
	}
	if calls != 1 {
		t.Fatal("rejected calls reached transport")
	}
	c = NewClient(roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure " + cred.AccessKeyID + cred.AccessKeySecret)
	}), 0)
	_, err := c.Call(context.Background(), cred, "DescribeInstances", "cn-shanghai", nil)
	if err == nil || strings.Contains(err.Error(), cred.AccessKeyID) || strings.Contains(err.Error(), cred.AccessKeySecret) {
		t.Fatal("transport leaked credentials")
	}
	t.Setenv("DEBUG", "1")
	if _, err := c.Call(context.Background(), cred, "DescribeInstances", "cn-shanghai", nil); Code(err) != "sdk_debug_must_be_disabled" {
		t.Fatal("SDK debug enabled")
	}
}
func TestSignedCDTReadAndRedirectBlocked(t *testing.T) {
	cred := fakeCredentials()
	calls := 0
	c := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Host != "cdt.aliyuncs.com" || r.URL.Query().Get("Action") != "ListCdtInternetTraffic" || r.URL.Query().Get("Signature") == "" {
			t.Fatal("CDT signed read")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"TrafficDetails":[]}`))}, nil
	}), 0)
	if _, err := c.Call(context.Background(), cred, "ListCdtInternetTraffic", "cn-hongkong", map[string]string{"RegionId": "cn-hongkong"}); err != nil {
		t.Fatal(err)
	}
	cred.Site = "international"
	if _, err := c.Call(context.Background(), cred, "ListCdtInternetTraffic", "cn-hongkong", map[string]string{"RegionId": "cn-hongkong"}); Code(err) != "cdt_international_endpoint_unverified" {
		t.Fatal("unverified international endpoint queried")
	}
	if calls != 1 {
		t.Fatal("unexpected call")
	}
	cred.Site = "china"
	calls = 0
	c = NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://untrusted.example/"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}), 0)
	_, _ = c.Call(context.Background(), cred, "DescribeInstances", "cn-shanghai", nil)
	if calls != 1 {
		t.Fatal("redirect followed")
	}
}

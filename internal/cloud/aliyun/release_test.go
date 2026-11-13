package aliyun

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestReleaseSignedFixedRequest(t *testing.T) {
	for _, tc := range []struct{ site, region, endpoint string }{
		{"china", "cn-shanghai", "ecs.cn-shanghai.aliyuncs.com"},
		{"china", "cn-hangzhou", "ecs-cn-hangzhou.aliyuncs.com"},
		{"international", "ap-southeast-1", "ecs.ap-southeast-1.aliyuncs.com"},
	} {
		t.Run(tc.site+"/"+tc.region, func(t *testing.T) {
			calls := 0
			client := NewReleaseClient(roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != tc.endpoint || r.URL.Path != "/" || header(r.Header, "x-acs-action") != "DeleteInstance" || header(r.Header, "x-acs-version") != "2014-05-26" || header(r.Header, "Authorization") == "" {
					t.Fatal("invalid signed release request")
				}
				q := r.URL.Query()
				if len(q) != 3 || q.Get("InstanceId") != "i-test" || q.Get("Force") != "false" || q.Get("TerminateSubscription") != "false" {
					t.Fatal("release target or fixed flags changed")
				}
				return powerResponse(200, `{"RequestId":"request-test"}`), nil
			}), 0)
			cred := fakeCredentials()
			cred.Site, cred.Regions = tc.site, []string{tc.region}
			got, err := client.Execute(context.Background(), cred, ReleaseRequest{Region: tc.region, InstanceID: "i-test"})
			if err != nil || got.Outcome != "accepted" || calls != 1 {
				t.Fatal(got, err, calls)
			}
		})
	}
}

func TestReleaseValidationDoesNotSend(t *testing.T) {
	valid := ReleaseRequest{Region: "cn-shanghai", InstanceID: "i-test"}
	cases := []struct {
		name, code string
		edit       func(*ReleaseRequest, *Credentials)
	}{
		{"empty instance", "invalid_instance_id", func(r *ReleaseRequest, _ *Credentials) { r.InstanceID = "" }},
		{"instance injection", "invalid_instance_id", func(r *ReleaseRequest, _ *Credentials) { r.InstanceID = "i-test&Force=true" }},
		{"oversize instance", "invalid_instance_id", func(r *ReleaseRequest, _ *Credentials) { r.InstanceID = strings.Repeat("a", 129) }},
		{"region injection", "invalid_region", func(r *ReleaseRequest, _ *Credentials) { r.Region = "cn-shanghai.evil.example" }},
		{"oversize region", "invalid_region", func(r *ReleaseRequest, _ *Credentials) { r.Region = strings.Repeat("a", 65) }},
		{"disabled region", "region_not_enabled", func(r *ReleaseRequest, _ *Credentials) { r.Region = "cn-hongkong" }},
		{"invalid site", "invalid_site", func(_ *ReleaseRequest, c *Credentials) { c.Site = "other" }},
	}
	client := NewReleaseClient(roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid release reached transport")
		return nil, nil
	}), 0)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, cred := valid, fakeCredentials()
			tc.edit(&req, &cred)
			got, err := client.Execute(context.Background(), cred, req)
			if Code(err) != tc.code || got.Outcome != "not_sent" {
				t.Fatal(got, Code(err))
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := client.Execute(ctx, fakeCredentials(), valid); Code(err) != "cancelled" || got.Outcome != "not_sent" {
		t.Fatal(got, Code(err))
	}
	t.Setenv("DEBUG", "1")
	if got, err := client.Execute(context.Background(), fakeCredentials(), valid); Code(err) != "sdk_debug_must_be_disabled" || got.Outcome != "not_sent" {
		t.Fatal(got, Code(err))
	}
}

func TestReleaseOutcomesNeverReplayOrLeak(t *testing.T) {
	cred := fakeCredentials()
	cases := []struct {
		name                string
		status              int
		body, outcome, code string
	}{
		{"denied", 403, `{"Code":"Forbidden.RAM","Message":"SECRET","RequestId":"r"}`, "rejected", "permission_denied"},
		{"credentials", 403, `{"Code":"InvalidAccessKeyId.NotFound","Message":"SECRET"}`, "rejected", "access_key_invalid"},
		{"state", 403, `{"Code":"IncorrectInstanceStatus","RequestId":"r"}`, "rejected", "instance_state_conflict"},
		{"initializing", 403, `{"Code":"IncorrectInstanceStatus.Initializing","RequestId":"r"}`, "rejected", "instance_state_conflict"},
		{"security lock", 403, `{"Code":"InstanceLockedForSecurity","Message":"SECRET"}`, "rejected", "instance_security_locked"},
		{"deletion protection", 403, `{"Code":"InvalidOperation.DeletionProtection","Message":"SECRET"}`, "rejected", "instance_deletion_protected"},
		{"charge type", 403, `{"Code":"ChargeTypeViolation","Message":"SECRET"}`, "rejected", "instance_charge_type_unsupported"},
		{"load balancer", 400, `{"Code":"DependencyViolation.SLBConfiguring","Message":"SECRET"}`, "rejected", "instance_dependencies_block_release"},
		{"route", 400, `{"Code":"DependencyViolation.RouteEntry","Message":"SECRET"}`, "rejected", "instance_dependencies_block_release"},
		{"network interface", 403, `{"Code":"InvalidOperation.EniLinked","Message":"SECRET"}`, "rejected", "instance_dependencies_block_release"},
		{"missing is not success", 404, `{"Code":"InvalidInstanceId.NotFound","RequestId":"r"}`, "rejected", "instance_not_found"},
		{"throttled", 429, `{"Code":"Throttling","Message":"SECRET"}`, "rejected", "aliyun_throttled"},
		{"opaque rejection", 400, `{"Code":"SECRET","Message":"SECRET"}`, "rejected", "release_request_rejected"},
		{"server", 500, `{"Code":"InternalError","Message":"SECRET"}`, "unknown", "release_outcome_unknown"},
		{"timeout", 408, `{"Code":"RequestTimeout","Message":"SECRET"}`, "unknown", "release_outcome_unknown"},
		{"redirect", 302, `{}`, "unknown", "release_outcome_unknown"},
		{"malformed rejection", 403, `not-json-SECRET`, "unknown", "release_outcome_unknown"},
		{"missing request ID", 200, `{}`, "unknown", "release_outcome_unknown"},
		{"invalid request ID", 200, `{"RequestId":17}`, "unknown", "release_outcome_unknown"},
		{"empty request ID", 200, `{"RequestId":" "}`, "unknown", "release_outcome_unknown"},
		{"error body", 200, `{"RequestId":"r","Code":"SECRET"}`, "unknown", "release_outcome_unknown"},
		{"malformed success", 200, `not-json-SECRET`, "unknown", "release_outcome_unknown"},
		{"transport", 0, "", "unknown", "release_outcome_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewReleaseClient(roundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				if tc.status == 0 {
					return nil, errors.New("SECRET " + cred.AccessKeyID + cred.AccessKeySecret)
				}
				res := powerResponse(tc.status, strings.ReplaceAll(tc.body, "SECRET", cred.AccessKeyID+cred.AccessKeySecret))
				res.Header.Set("Location", "https://untrusted.example/")
				return res, nil
			}), 0)
			got, err := client.Execute(context.Background(), cred, ReleaseRequest{Region: "cn-shanghai", InstanceID: "i-test"})
			if got.Outcome != tc.outcome || Code(err) != tc.code || calls != 1 {
				t.Fatal(got, Code(err), calls)
			}
			if strings.Contains(err.Error(), cred.AccessKeyID) || strings.Contains(err.Error(), cred.AccessKeySecret) {
				t.Fatal("leaked credential")
			}
		})
	}
}

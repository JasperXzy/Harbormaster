package aliyun

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func powerResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestPowerSignedFixedActions(t *testing.T) {
	for _, kind := range []string{"ecs", "swas"} {
		for _, action := range []string{"start", "stop", "reboot"} {
			modes := []string{""}
			if kind == "ecs" && action == "stop" {
				modes = []string{"keep_charging", "stop_charging"}
			}
			for _, mode := range modes {
				t.Run(kind+"/"+action+"/"+mode, func(t *testing.T) {
					req := PowerRequest{Kind: kind, Region: "cn-shanghai", InstanceID: "i-test", Action: action, StopMode: mode}
					if kind == "swas" {
						req.ClientToken = "job_test"
					}
					calls := 0
					client := NewPowerClient(roundTrip(func(r *http.Request) (*http.Response, error) {
						calls++
						expected := map[string]string{"start": "StartInstance", "stop": "StopInstance", "reboot": "RebootInstance"}[action]
						version := "2014-05-26"
						if kind == "swas" {
							version = "2020-06-01"
						}
						if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != kind+".cn-shanghai.aliyuncs.com" || header(r.Header, "x-acs-action") != expected || header(r.Header, "x-acs-version") != version || header(r.Header, "Authorization") == "" {
							t.Fatal("invalid signed power request")
						}
						q := r.URL.Query()
						if q.Get("InstanceId") != "i-test" {
							t.Fatal("wrong target")
						}
						expectedCount := 1
						if kind == "swas" {
							expectedCount = 3
							if q.Get("ClientToken") != "job_test" || q.Get("RegionId") != "cn-shanghai" {
								t.Fatal("missing SWAS fields")
							}
						} else {
							expectedCount = 2
							key := "ForceStop"
							if action == "start" {
								key = "InitLocalDisk"
							}
							if q.Get(key) != "false" {
								t.Fatal("unsafe power flags")
							}
							if action == "stop" {
								expectedCount++
								if q.Get("StoppedMode") != map[string]string{"keep_charging": "KeepCharging", "stop_charging": "StopCharging"}[mode] {
									t.Fatal("stop mode omitted or changed")
								}
							}
						}
						if len(q) != expectedCount {
							t.Fatal("unexpected query parameters", q)
						}
						return powerResponse(200, `{"RequestId":"request-test"}`), nil
					}), 0)
					got, err := client.Execute(context.Background(), fakeCredentials(), req)
					if err != nil || got.Outcome != "accepted" || calls != 1 {
						t.Fatal(got, err, calls)
					}
				})
			}
		}
	}
}

func TestPowerValidationDoesNotSend(t *testing.T) {
	valid := PowerRequest{Kind: "ecs", Region: "cn-shanghai", InstanceID: "i-test", Action: "start"}
	cases := []struct {
		name string
		edit func(*PowerRequest, *Credentials)
	}{
		{"arbitrary action", func(r *PowerRequest, c *Credentials) { r.Action = "RunCommand" }},
		{"arbitrary kind", func(r *PowerRequest, c *Credentials) { r.Kind = "vpc" }},
		{"region injection", func(r *PowerRequest, c *Credentials) { r.Region = "cn-shanghai.evil.example" }},
		{"unconfigured region", func(r *PowerRequest, c *Credentials) { r.Region = "cn-hongkong" }},
		{"invalid site", func(r *PowerRequest, c *Credentials) { c.Site = "other" }},
		{"invalid instance", func(r *PowerRequest, c *Credentials) { r.InstanceID = "i-test&ForceStop=true" }},
		{"ECS stop needs explicit mode", func(r *PowerRequest, c *Credentials) { r.Action = "stop" }},
		{"unknown stop mode", func(r *PowerRequest, c *Credentials) { r.Action = "stop"; r.StopMode = "force" }},
		{"mode on start", func(r *PowerRequest, c *Credentials) { r.StopMode = "keep_charging" }},
		{"ECS unsupported token", func(r *PowerRequest, c *Credentials) { r.ClientToken = "job_test" }},
		{"SWAS needs persisted token", func(r *PowerRequest, c *Credentials) { r.Kind = "swas" }},
		{"SWAS stop mode unsupported", func(r *PowerRequest, c *Credentials) {
			r.Kind = "swas"
			r.Action = "stop"
			r.StopMode = "stop_charging"
			r.ClientToken = "job_test"
		}},
		{"SWAS token invalid", func(r *PowerRequest, c *Credentials) { r.Kind = "swas"; r.ClientToken = strings.Repeat("a", 65) }},
	}
	client := NewPowerClient(roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid operation reached transport")
		return nil, nil
	}), 0)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, c := valid, fakeCredentials()
			tc.edit(&r, &c)
			got, err := client.Execute(context.Background(), c, r)
			if err == nil || got.Outcome != "not_sent" {
				t.Fatal(got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := client.Execute(ctx, fakeCredentials(), valid); err == nil || got.Outcome != "not_sent" {
		t.Fatal(got, err)
	}
	t.Setenv("DEBUG", "1")
	if got, err := client.Execute(context.Background(), fakeCredentials(), valid); Code(err) != "sdk_debug_must_be_disabled" || got.Outcome != "not_sent" {
		t.Fatal(got, err)
	}
}

func TestPowerOutcomesNeverReplayOrLeak(t *testing.T) {
	cred := fakeCredentials()
	cases := []struct {
		name                string
		status              int
		body, outcome, code string
	}{
		{"denied", 403, `{"Code":"Forbidden.RAM","Message":"SECRET","RequestId":"r"}`, "rejected", "permission_denied"},
		{"state", 403, `{"Code":"IncorrectInstanceStatus","RequestId":"r"}`, "rejected", "instance_state_conflict"},
		{"missing", 404, `{"Code":"InvalidInstanceId.NotFound","RequestId":"r"}`, "rejected", "instance_not_found"},
		{"throttled", 429, `{"Code":"Throttling","RequestId":"r"}`, "rejected", "aliyun_throttled"},
		{"opaque rejection", 400, `{"Code":"SECRET","Message":"SECRET"}`, "rejected", "power_request_rejected"},
		{"server", 500, `{"Code":"InternalError","Message":"SECRET"}`, "unknown", "power_outcome_unknown"},
		{"timeout", 408, `{"Code":"RequestTimeout","Message":"SECRET"}`, "unknown", "power_outcome_unknown"},
		{"redirect", 302, `{}`, "unknown", "power_outcome_unknown"},
		{"malformed rejection", 403, `not-json-SECRET`, "unknown", "power_outcome_unknown"},
		{"missing request ID", 200, `{}`, "unknown", "power_outcome_unknown"},
		{"error body", 200, `{"RequestId":"r","Code":"SECRET"}`, "unknown", "power_outcome_unknown"},
		{"malformed success", 200, `not-json-SECRET`, "unknown", "power_outcome_unknown"},
		{"transport", 0, "", "unknown", "power_outcome_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewPowerClient(roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.status == 0 {
					return nil, errors.New("SECRET " + cred.AccessKeyID + cred.AccessKeySecret)
				}
				res := powerResponse(tc.status, strings.ReplaceAll(tc.body, "SECRET", cred.AccessKeyID+cred.AccessKeySecret))
				res.Header.Set("Location", "https://untrusted.example/")
				return res, nil
			}), 0)
			got, err := client.Execute(context.Background(), cred, PowerRequest{Kind: "ecs", Region: "cn-shanghai", InstanceID: "i-test", Action: "reboot"})
			if got.Outcome != tc.outcome || Code(err) != tc.code || calls != 1 {
				t.Fatal(got, Code(err), calls)
			}
			if strings.Contains(err.Error(), cred.AccessKeyID) || strings.Contains(err.Error(), cred.AccessKeySecret) {
				t.Fatal("leaked credential")
			}
		})
	}
}

func TestPowerGateRejectsSecondAttempt(t *testing.T) {
	calls := 0
	client := NewClient(roundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("lost response") }), 0)
	gate := powerHTTP{gate: readHTTP{client: client.http, endpoint: "ecs.cn-shanghai.aliyuncs.com", action: "RebootInstance", version: "2014-05-26", method: "POST"}}
	req, _ := http.NewRequest("POST", "https://ecs.cn-shanghai.aliyuncs.com/", nil)
	req.Header.Set("x-acs-action", "RebootInstance")
	req.Header.Set("x-acs-version", "2014-05-26")
	_, _ = gate.Call(req, nil)
	_, err := gate.Call(req, nil)
	if err == nil || err.Error() != "power_retry_not_allowed" || calls != 1 {
		t.Fatal(err, calls)
	}
}

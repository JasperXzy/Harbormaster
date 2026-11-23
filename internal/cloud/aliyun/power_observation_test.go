package aliyun

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPowerObservationUsesTargetedSignedReads(t *testing.T) {
	for _, kind := range []string{"ecs", "swas"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			caller := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Query().Get("InstanceIds") != `["i-test"]` || r.URL.Query().Get("RegionId") != "cn-shanghai" || header(r.Header, "Authorization") == "" {
					t.Fatal("untargeted or unsigned status query")
				}
				body := `{"Instances":{"Instance":[{"InstanceId":"i-test","RegionId":"cn-shanghai","Status":"Stopped","StoppedMode":"StopCharging","StartTime":"2026-01-01T01:01Z"}]}}`
				if kind == "swas" {
					body = `{"Instances":[{"InstanceId":"i-test","RegionId":"cn-shanghai","Status":"Running"}]}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}), 0)
			got, err := (PowerObserver{Caller: caller}).Observe(context.Background(), fakeCredentials(), kind, "cn-shanghai", "i-test")
			if err != nil || !got.Valid() || calls != 1 {
				t.Fatal(got, err)
			}
			if kind == "ecs" && (got.StoppedMode == nil || *got.StoppedMode != "stop_charging" || got.StartedAt == nil || got.StartedAt.Minute() != 1) {
				t.Fatal(got)
			}
			if kind == "swas" && (got.StoppedMode != nil || got.StartedAt != nil || got.Status != "running") {
				t.Fatal("fabricated SWAS fields", got)
			}
		})
	}
}

func TestPowerObservationRejectsWrongAndUnknownTargets(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"Instances":{"Instance":[]}}`,
		`{"Instances":{"Instance":[{"InstanceId":"other","RegionId":"cn-shanghai","Status":"Running"}]}}`,
		`{"Instances":{"Instance":[{"InstanceId":"i-test","RegionId":"cn-hongkong","Status":"Running"}]}}`,
		`{"Instances":{"Instance":[{"InstanceId":"i-test","RegionId":"cn-shanghai","Status":"Unexpected"}]}}`,
		`{"Instances":{"Instance":[{"InstanceId":"i-test","RegionId":"cn-shanghai"}]}}`,
		`{"Instances":{"Instance":[{},{}]}}`,
	} {
		c := NewClient(roundTrip(func(*http.Request) (*http.Response, error) { return powerResponse(200, body), nil }), 0)
		if _, err := (PowerObserver{Caller: c}).Observe(context.Background(), fakeCredentials(), "ecs", "cn-shanghai", "i-test"); err == nil {
			t.Fatal("untrusted observation accepted", body)
		}
	}
	c := NewClient(roundTrip(func(*http.Request) (*http.Response, error) {
		return powerResponse(200, `{"Instances":{"Instance":[{"InstanceId":"i-test","RegionId":"cn-shanghai","Status":"Running","StoppedMode":"new-provider-mode","StartTime":"not-a-time"}]}}`), nil
	}), 0)
	got, err := (PowerObserver{Caller: c}).Observe(context.Background(), fakeCredentials(), "ecs", "cn-shanghai", "i-test")
	if err != nil || got.StoppedMode != nil || got.StartedAt != nil {
		t.Fatal("unknown optional fields inferred", got, err)
	}
}

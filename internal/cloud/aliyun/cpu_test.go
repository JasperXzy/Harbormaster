package aliyun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func cpuBody(points []map[string]any, next string) map[string]any {
	raw, _ := json.Marshal(points)
	return map[string]any{"Success": true, "Code": "200", "Period": "60", "Datapoints": string(raw), "NextToken": next}
}
func cpuPoint(id string, at time.Time, value any) map[string]any {
	return map[string]any{"instanceId": id, "timestamp": at.UnixMilli(), "Average": value}
}

func TestCPUMetricPaginatesAndPreservesZero(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls++
		if action != "DescribeMetricList" || q["Namespace"] != "acs_ecs_dashboard" || q["MetricName"] != "CPUUtilization" || q["Length"] != "1440" || q["Dimensions"] != `[{"instanceId":"i-test"}]` {
			t.Fatal("unexpected request", action, q)
		}
		if calls == 1 {
			return cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-2*time.Minute), 23.5)}, "next"), nil
		}
		if q["NextToken"] != "next" {
			t.Fatal("continuation not followed")
		}
		return cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-time.Minute), 0)}, ""), nil
	})}
	result, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"i-test"}, "CPUUtilization", func() error { return nil })
	r := result["i-test"]
	if err != nil || calls != 2 || r.Value == nil || *r.Value != 0 || r.Status != "ok" || r.ObservedAt == nil || r.Unit != "percent" {
		t.Fatal(result, err, calls)
	}
}

func TestCPUMetricRejectsMalformedPartialAndWrongData(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		body map[string]any
	}{
		{"wrong instance", cpuBody([]map[string]any{cpuPoint("other", now.Add(-time.Minute), 12)}, "")},
		{"future sample", cpuBody([]map[string]any{cpuPoint("i-test", now.Add(time.Minute), 12)}, "")},
		{"old sample", cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-time.Hour), 12)}, "")},
		{"over 100", cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-time.Minute), 101)}, "")},
		{"negative", cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-time.Minute), -1)}, "")},
		{"missing average", cpuBody([]map[string]any{{"instanceId": "i-test", "timestamp": now.Add(-time.Minute).UnixMilli()}}, "")},
		{"conflicting samples", cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-time.Minute), 1), cpuPoint("i-test", now.Add(-time.Minute), 2)}, "")},
		{"null list", map[string]any{"Datapoints": "null"}},
		{"wrong period", map[string]any{"Datapoints": "[]", "Period": "300"}},
		{"error code", map[string]any{"Datapoints": "[]", "Code": "403"}},
		{"unsuccessful", map[string]any{"Datapoints": "[]", "Success": false}},
		{"malformed", map[string]any{"Datapoints": "["}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
				return tc.body, nil
			})}
			m, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"i-test"}, "CPUUtilization", func() error { return nil })
			if err == nil || m["i-test"].Value != nil || m["i-test"].Status != "unknown" {
				t.Fatal(m, err)
			}
		})
	}
	calls := 0
	c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
		calls++
		if calls == 1 {
			return cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-time.Minute), 4)}, "next"), nil
		}
		return nil, failure("permission_denied")
	})}
	m, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"i-test"}, "CPUUtilization", func() error { return nil })
	if err == nil || m["i-test"].Value != nil || m["i-test"].LastError.Code != "permission_denied" {
		t.Fatal("partial query published as complete", m, err)
	}
}

func TestCPUMetricEmptyUnknownAndCreditsRemainAbsolute(t *testing.T) {
	now := time.Now().UTC()
	for _, name := range []string{"CPUUtilization", "TotalCredit"} {
		c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
			return cpuBody([]map[string]any{}, ""), nil
		})}
		m, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"i-test"}, name, func() error { return nil })
		r := m["i-test"]
		if err != nil || r.Status != "unknown" || r.Value != nil || r.LastError.Code != "metric_data_unavailable" {
			t.Fatal(m, err)
		}
	}
	c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
		return cpuBody([]map[string]any{cpuPoint("i-test", now.Add(-11*time.Minute), 350.25)}, ""), nil
	})}
	m, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"i-test"}, "TotalCredit", func() error { return nil })
	r := m["i-test"]
	if err != nil || r.Value == nil || *r.Value != 350.25 || r.Unit != "count" || r.Status != "stale" {
		t.Fatal(m, err)
	}
}

func TestCPUAttachmentBatchesECSAndQueriesSWASSeparately(t *testing.T) {
	calls := 0
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls++
		if action == "DescribeMonitorData" {
			if q["InstanceId"] != "swas-test" || q["MetricName"] != "CPU_UTILIZATION" {
				t.Fatal("wrong SWAS query", q)
			}
			return cpuBody([]map[string]any{}, ""), nil
		}
		var dims []map[string]string
		if json.Unmarshal([]byte(q["Dimensions"]), &dims) != nil || len(dims) > 50 || len(dims) == 0 {
			t.Fatal("bad batch")
		}
		return cpuBody([]map[string]any{}, ""), nil
	})}
	snapshots := []model.Snapshot{}
	for i := 0; i < 51; i++ {
		snapshots = append(snapshots, model.Snapshot{Kind: "ecs", Payload: map[string]any{"cloud_instance_id": fmt.Sprintf("i-%d", i)}})
	}
	snapshots = append(snapshots, model.Snapshot{Kind: "swas", Payload: map[string]any{"cloud_instance_id": "swas-test"}})
	c.attachCPU(context.Background(), fakeCredentials(), "cn-shanghai", snapshots, func() error { return nil }, func(string, error) {})
	swas := snapshots[51].Payload["cpu"].(*model.InstanceCPU)
	if calls != 5 || swas.CreditBalance != nil || swas.Namespace != nil || swas.Usage.MetricName != "CPU_UTILIZATION" || swas.Usage.Status != "unknown" {
		t.Fatal(calls, snapshots[51])
	}
	for _, s := range snapshots[:51] {
		if s.Payload["cpu"].(*model.InstanceCPU).Usage.Status != "unknown" {
			t.Fatal("empty source became zero")
		}
	}
}

func TestSWASCPUUsesFixedSignedEndpointAndOwnInstanceID(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	client := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		if _, err := time.Parse(time.RFC3339, q.Get("StartTime")); err != nil {
			t.Fatal("SWAS time must use verified ISO format")
		}
		if r.Method != "GET" || r.URL.Host != "swas.cn-shanghai.aliyuncs.com" || header(r.Header, "x-acs-action") != "DescribeMonitorData" || header(r.Header, "Authorization") == "" || q.Get("RegionId") != "cn-shanghai" || q.Get("InstanceId") != "swas-test" || q.Get("MetricName") != "CPU_UTILIZATION" || q.Get("Namespace") != "" || q.Get("Dimensions") != "" || q.Get("Period") != "60" {
			t.Fatal("wrong signed SWAS query")
		}
		body := cpuBody([]map[string]any{cpuPoint("swas-test", now.Add(-time.Minute), 0.133)}, "")
		delete(body, "Success")
		delete(body, "Code")
		raw, _ := json.Marshal(body)
		return powerResponse(200, string(raw)), nil
	}), 0)
	c := Inventory{Caller: client}
	result, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"swas-test"}, "CPU_UTILIZATION", func() error { return nil })
	r := result["swas-test"]
	if err != nil || calls != 1 || r.Status != "ok" || r.Value == nil || *r.Value != 0.133 || r.Unit != "percent" {
		t.Fatal(result, err, calls)
	}
}

func TestSWASCPUPaginationAndInvalidSamplesRemainUnknown(t *testing.T) {
	now := time.Now().UTC()
	for _, scenario := range []string{"pagination", "wrong instance", "over 100", "empty", "repeated cursor"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
				calls++
				id := "swas-test"
				value := 1.2
				if scenario == "wrong instance" {
					id = "underlying-ecs-id"
				}
				if scenario == "over 100" {
					value = 101
				}
				if scenario == "empty" {
					return cpuBody([]map[string]any{}, ""), nil
				}
				next := ""
				if scenario == "pagination" && calls == 1 || scenario == "repeated cursor" {
					next = "next"
				}
				if calls > 1 && q["NextToken"] != "next" {
					t.Fatal("SWAS continuation lost")
				}
				return cpuBody([]map[string]any{cpuPoint(id, now.Add(-time.Minute), value)}, next), nil
			})}
			result, err := c.cpuMetric(context.Background(), fakeCredentials(), "cn-shanghai", []string{"swas-test"}, "CPU_UTILIZATION", func() error { return nil })
			r := result["swas-test"]
			if scenario == "pagination" {
				if err != nil || calls != 2 || r.Status != "ok" {
					t.Fatal(result, err, calls)
				}
			} else if scenario == "empty" {
				if err != nil || r.Value != nil || r.Status != "unknown" {
					t.Fatal(result, err)
				}
			} else if err == nil || r.Value != nil {
				t.Fatal("unsafe SWAS sample accepted", result, err)
			}
		})
	}
}

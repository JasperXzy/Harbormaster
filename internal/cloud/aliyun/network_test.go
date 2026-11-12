package aliyun

import (
	"context"
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
	"time"
)

func networkPoint(id, ip string, at time.Time, value any) map[string]any {
	p := cpuPoint(id, at, value)
	if ip != "" {
		p["ip"] = ip
	}
	return p
}
func TestNetworkMetricKeepsIPStreamsAndPaginates(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	targets := []networkTarget{{"i-test", "203.0.113.1"}, {"i-test", "203.0.113.2"}}
	calls := 0
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls++
		var dims []map[string]string
		if json.Unmarshal([]byte(q["Dimensions"]), &dims) != nil || len(dims) != 2 || dims[0]["ip"] != "203.0.113.1" || q["MetricName"] != "VPC_PublicIP_InternetInRate" || action != "DescribeMetricList" {
			t.Fatal(q)
		}
		if calls == 1 {
			return cpuBody([]map[string]any{networkPoint("i-test", "203.0.113.1", now, 0)}, "next"), nil
		}
		if q["NextToken"] != "next" {
			t.Fatal("pagination ignored")
		}
		return cpuBody([]map[string]any{networkPoint("i-test", "203.0.113.2", now, 123456)}, ""), nil
	})}
	got, err := c.networkMetric(context.Background(), fakeCredentials(), "cn-shanghai", targets, "vpc", "VPC_PublicIP_InternetInRate", func() error { return nil })
	if err != nil || calls != 2 || got[targets[0]].Value == nil || *got[targets[0]].Value != 0 || got[targets[1]].Value == nil || *got[targets[1]].Value != 123456 || got[targets[1]].Unit != "bit/s" {
		t.Fatal(got, err, calls)
	}
}
func TestNetworkMetricRejectsWrongOrPartialSeries(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	target := networkTarget{"i-test", "203.0.113.1"}
	cases := []map[string]any{
		cpuBody([]map[string]any{networkPoint("i-other", target.IP, now, 5)}, ""),
		cpuBody([]map[string]any{networkPoint(target.ID, "203.0.113.2", now, 5)}, ""),
		cpuBody([]map[string]any{networkPoint(target.ID, "", now, 5)}, ""),
		cpuBody([]map[string]any{networkPoint(target.ID, target.IP, now, -1)}, ""),
		cpuBody([]map[string]any{networkPoint(target.ID, target.IP, now.Add(time.Hour), 5)}, ""),
		cpuBody([]map[string]any{networkPoint(target.ID, target.IP, now, 5), networkPoint(target.ID, target.IP, now, 6)}, ""),
		{"Datapoints": "null"}, {"Datapoints": "["}, {"Datapoints": "[]", "Period": "300"},
	}
	for _, body := range cases {
		c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
			return body, nil
		})}
		got, err := c.networkMetric(context.Background(), fakeCredentials(), "cn-shanghai", []networkTarget{target}, "vpc", "VPC_PublicIP_InternetInRate", func() error { return nil })
		if err == nil || got[target].Value != nil {
			t.Fatal("invalid sample published", got, err)
		}
	}
	calls := 0
	c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
		calls++
		if calls == 1 {
			return cpuBody([]map[string]any{networkPoint(target.ID, target.IP, now, 99)}, "next"), nil
		}
		return nil, failure("permission_denied")
	})}
	got, err := c.networkMetric(context.Background(), fakeCredentials(), "cn-shanghai", []networkTarget{target}, "vpc", "VPC_PublicIP_InternetInRate", func() error { return nil })
	if err == nil || got[target].Value != nil || len(got[target].Samples) != 0 || got[target].LastError.Code != "permission_denied" {
		t.Fatal("partial data published", got, err)
	}
}
func TestSWASNetworkEmptyStringIsUnknown(t *testing.T) {
	target := networkTarget{ID: "swas-test"}
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		if _, err := time.Parse(time.RFC3339, q["StartTime"]); err != nil {
			t.Fatal("SWAS time must use verified ISO format")
		}
		if action != "DescribeMonitorData" || q["InstanceId"] != target.ID || q["MetricName"] != "VPC_PUBLICIP_INTERNETOUT_RATE" {
			t.Fatal(action, q)
		}
		return map[string]any{"Datapoints": "", "Period": "60"}, nil
	})}
	got, err := c.networkMetric(context.Background(), fakeCredentials(), "cn-shanghai", []networkTarget{target}, "swas", "VPC_PUBLICIP_INTERNETOUT_RATE", func() error { return nil })
	if err != nil || got[target].Status != "unknown" || got[target].Value != nil {
		t.Fatal(got, err)
	}
}
func TestNetworkAttachmentSelectsVerifiedTopology(t *testing.T) {
	snapshots := []model.Snapshot{
		{Kind: "ecs", IPv4Complete: true, Payload: map[string]any{"cloud_instance_id": "i-vpc", "public_ips": []map[string]any{{"address": "203.0.113.1"}, {"address": "203.0.113.2"}}}},
		{Kind: "ecs", IPv4Complete: true, Payload: map[string]any{"cloud_instance_id": "i-classic"}},
		{Kind: "ecs", IPv4Complete: false, Payload: map[string]any{"cloud_instance_id": "i-incomplete"}},
		{Kind: "ecs", IPv4Complete: true, Payload: map[string]any{"cloud_instance_id": "i-unknown"}},
		{Kind: "swas", Payload: map[string]any{"cloud_instance_id": "swas-test"}},
	}
	ecs := collection{rows: []map[string]any{{"InstanceId": "i-vpc", "InstanceNetworkType": "vpc"}, {"InstanceId": "i-classic", "InstanceNetworkType": "classic"}, {"InstanceId": "i-incomplete", "InstanceNetworkType": "vpc"}}}
	names := map[string]int{}
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		names[q["MetricName"]]++
		return cpuBody([]map[string]any{}, ""), nil
	})}
	c.attachNetworkRates(context.Background(), fakeCredentials(), "cn-shanghai", ecs, snapshots, func() error { return nil }, func(string, error) {})
	if len(names) != 6 {
		t.Fatal(names)
	}
	for _, count := range names {
		if count != 1 {
			t.Fatal("queried unsupported or incomplete topology", names)
		}
	}
	for i, want := range []int{2, 1, 0, 0, 1} {
		n := snapshots[i].Payload["cloud_network"].(*model.CloudNetwork)
		if len(n.Streams) != want {
			t.Fatal(i, n)
		}
	}
	if snapshots[0].Payload["cloud_network"].(*model.CloudNetwork).Streams[0].Ingress.MetricName != "VPC_PublicIP_InternetInRate" {
		t.Fatal("classic fallback chosen for VPC")
	}
}

func TestNetworkQueryRetainsAllUniqueMinutesInTimeOrder(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	target := networkTarget{"i-test", "203.0.113.1"}
	calls := 0
	c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
		calls++
		if calls == 1 {
			return cpuBody([]map[string]any{networkPoint(target.ID, target.IP, now, 5)}, "next"), nil
		}
		return cpuBody([]map[string]any{networkPoint(target.ID, target.IP, now.Add(-time.Minute), 0), networkPoint(target.ID, target.IP, now, 5)}, ""), nil
	})}
	got, err := c.networkMetric(context.Background(), fakeCredentials(), "cn-shanghai", []networkTarget{target}, "vpc", "VPC_PublicIP_InternetInRate", func() error { return nil })
	r := got[target]
	if err != nil || len(r.Samples) != 2 || r.Samples[0].Value != 0 || !r.Samples[0].SampledAt.Before(r.Samples[1].SampledAt) || r.Value == nil || *r.Value != 5 {
		t.Fatal(r, err)
	}
	raw, _ := json.Marshal(r)
	var public map[string]any
	_ = json.Unmarshal(raw, &public)
	if _, exists := public["Samples"]; exists {
		t.Fatal("internal history leaked into latest response")
	}
}

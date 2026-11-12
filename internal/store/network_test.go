package store

import (
	"context"
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
	"time"
)

func TestNetworkSnapshotsPreserveSamplesButDoNotMoveThemAcrossIPs(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sampled := now.Add(-time.Minute)
	for step := 0; step < 5; step++ {
		at := now.Add(time.Duration(step) * time.Minute)
		j, a := startIPv6Scan(t, s, at)
		ip := "203.0.113.1"
		if step == 4 {
			ip = "203.0.113.2"
		}
		n := &model.CloudNetwork{Source: "cms", NetworkType: "vpc", TopologyComplete: step != 3, CheckedAt: at, Streams: []model.CloudNetworkStream{{IP: &ip, Ingress: model.EmptyNetworkRate("VPC_PublicIP_InternetInRate", at), Egress: model.EmptyNetworkRate("VPC_PublicIP_InternetOutRate", at)}}}
		if step == 0 || step == 2 {
			value := float64(0)
			observed := sampled
			if step == 2 {
				value = 500
				observed = sampled.Add(-time.Minute)
			}
			n.Streams[0].Ingress.Value = &value
			n.Streams[0].Ingress.ObservedAt = &observed
			n.Streams[0].Ingress.Status = "ok"
			n.Streams[0].Ingress.LastError = nil
		}
		if step == 3 {
			n.Streams = nil
		}
		result := ipv6ScanFixture(at, false)
		result.Snapshots[0].Payload["cloud_network"] = n
		if err := s.SaveScan(ctx, j, a, result, at); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishJob(ctx, j, nil, "", at); err != nil {
			t.Fatal(err)
		}
		snapshots, err := s.Snapshots(ctx, []string{"ecs"}, "a")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(snapshots[0]["cloud_network"])
		var got model.CloudNetwork
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Streams) != 1 {
			t.Fatal(got)
		}
		rate := got.Streams[0].Ingress
		if step < 4 {
			want := "stale"
			if step == 0 {
				want = "ok"
			}
			if rate.Value == nil || *rate.Value != 0 || rate.ObservedAt == nil || !rate.ObservedAt.Equal(sampled) || rate.Status != want {
				t.Fatal(step, rate)
			}
		} else if rate.Value != nil || rate.Status != "unknown" || *got.Streams[0].IP != ip {
			t.Fatal("old IP sample migrated", got)
		}
		if step == 2 && rate.LastError.Code != "metric_sample_regressed" {
			t.Fatal(rate)
		}
		if step == 3 && (got.TopologyComplete || rate.LastError.Code != "metric_topology_unknown") {
			t.Fatal(got)
		}
	}
}

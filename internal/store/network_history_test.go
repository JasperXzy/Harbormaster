package store

import (
	"context"
	"github.com/JasperXzy/harbormaster/internal/model"
	"math"
	"testing"
	"time"
)

func networkHistoryFixture(at time.Time, points []model.CloudRatePoint) model.ScanResult {
	result := ipv6ScanFixture(at, false)
	ip := "203.0.113.1"
	in, out := model.NetworkMetricNames("vpc")
	stream := model.CloudNetworkStream{IP: &ip, Ingress: model.EmptyNetworkRate(in, at), Egress: model.EmptyNetworkRate(out, at)}
	stream.Ingress.Samples = points
	if len(points) > 0 {
		latest := points[len(points)-1]
		stream.Ingress.Value = &latest.Value
		stream.Ingress.ObservedAt = &latest.SampledAt
		stream.Ingress.Status = "ok"
		stream.Ingress.LastError = nil
	}
	result.Snapshots[0].Payload["cloud_network"] = &model.CloudNetwork{Source: "cms", NetworkType: "vpc", TopologyComplete: true, CheckedAt: at, Streams: []model.CloudNetworkStream{stream}}
	return result
}
func TestCloudNetworkHistoryUpsertsCorrectionsAndDoesNotInventGaps(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	first := now.Add(-2 * time.Minute)
	last := now.Add(-time.Minute)
	var id string
	for step := 0; step < 3; step++ {
		at := now.Add(time.Duration(step) * time.Minute)
		j, a := startIPv6Scan(t, s, at)
		var points []model.CloudRatePoint
		if step < 2 {
			points = []model.CloudRatePoint{{SampledAt: first, Value: 0}, {SampledAt: last, Value: float64(10 + step)}}
		}
		result := networkHistoryFixture(at, points)
		id = result.Snapshots[0].ID
		if err := s.SaveScan(ctx, j, a, result, at); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishJob(ctx, j, nil, "", at); err != nil {
			t.Fatal(err)
		}
	}
	filter := model.CloudNetworkHistoryFilter{InstanceID: id, From: now.Add(-time.Hour), To: time.Now().UTC(), Direction: "ingress", IP: "203.0.113.1"}
	rows, err := s.CloudNetworkHistory(ctx, filter, 10)
	if err != nil || len(rows) != 2 || rows[0].Value != 11 || rows[1].Value != 0 || !rows[0].SampledAt.Equal(last) {
		t.Fatal(rows, err)
	}
	if !rows[0].CheckedAt.Equal(now.Add(time.Minute)) {
		t.Fatal("failed scan rewrote original observation", rows[0])
	}
	filter.BeforeTime = rows[0].SampledAt
	filter.BeforeSequence = rows[0].Sequence
	rows, err = s.CloudNetworkHistory(ctx, filter, 10)
	if err != nil || len(rows) != 1 || !rows[0].SampledAt.Equal(first) {
		t.Fatal(rows, err)
	}
	filter.BeforeTime = time.Time{}
	filter.BeforeSequence = 0
	filter.Direction = "egress"
	rows, err = s.CloudNetworkHistory(ctx, filter, 10)
	if err != nil || len(rows) != 0 {
		t.Fatal("missing direction filled", rows, err)
	}
}
func TestCloudNetworkHistorySharesScanFenceAndRollback(t *testing.T) {
	for _, invalid := range []string{"lease", "sample"} {
		t.Run(invalid, func(t *testing.T) {
			s := powerStore(t)
			ctx := context.Background()
			now := time.Now().UTC()
			j, a := startIPv6Scan(t, s, now)
			point := model.CloudRatePoint{SampledAt: now.Add(-time.Minute), Value: 4}
			if invalid == "sample" {
				point.Value = math.Inf(1)
			}
			result := networkHistoryFixture(now, []model.CloudRatePoint{{SampledAt: now.Add(-2 * time.Minute), Value: 2}, point})
			if invalid == "sample" {
				value := float64(4)
				result.Snapshots[0].Payload["cloud_network"].(*model.CloudNetwork).Streams[0].Ingress.Value = &value
			}
			if invalid == "lease" {
				j.Owner = "stale-worker"
			}
			if err := s.SaveScan(ctx, j, a, result, now); err == nil {
				t.Fatal("invalid scan accepted")
			}
			var count int
			if err := s.DB.QueryRow("SELECT count(*) FROM cloud_network_sample").Scan(&count); err != nil || count != 0 {
				t.Fatal("sample escaped transaction", count, err)
			}
		})
	}
}
func TestCloudNetworkHistoryPreservesPreviousIPAndFiltersRange(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Minute)
	sampled := now.Add(-time.Minute)
	var id string
	for step := 0; step < 2; step++ {
		at := now.Add(time.Duration(step) * time.Second)
		j, a := startIPv6Scan(t, s, at)
		result := networkHistoryFixture(at, []model.CloudRatePoint{{SampledAt: sampled, Value: float64(step)}})
		id = result.Snapshots[0].ID
		if step == 1 {
			ip := "203.0.113.2"
			result.Snapshots[0].Payload["cloud_network"].(*model.CloudNetwork).Streams[0].IP = &ip
		}
		if err := s.SaveScan(ctx, j, a, result, at); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishJob(ctx, j, nil, "", at); err != nil {
			t.Fatal(err)
		}
	}
	f := model.CloudNetworkHistoryFilter{InstanceID: id, From: sampled, To: now}
	rows, err := s.CloudNetworkHistory(ctx, f, 1)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	f.BeforeTime = rows[0].SampledAt
	f.BeforeSequence = rows[0].Sequence
	rows, err = s.CloudNetworkHistory(ctx, f, 1)
	if err != nil || len(rows) != 1 || *rows[0].IP != "203.0.113.1" {
		t.Fatal("timestamp tie skipped", rows, err)
	}
	f.BeforeTime = time.Time{}
	f.BeforeSequence = 0
	f.From = sampled.Add(-time.Minute)
	f.To = sampled
	rows, err = s.CloudNetworkHistory(ctx, f, 10)
	if err != nil || len(rows) != 0 {
		t.Fatal("exclusive end violated", rows, err)
	}
}

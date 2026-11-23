package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestCPUSnapshotsRetainLastValueAndSampleTimeAcrossRestart(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	observed := now.Add(-time.Minute)
	for index := 0; index < 3; index++ {
		at := now.Add(time.Duration(index) * time.Minute)
		j, a := startIPv6Scan(t, s, at)
		cpu := model.NewECSCPU(at)
		if index == 0 {
			zero := float64(0)
			cpu.Usage.Value = &zero
			cpu.Usage.ObservedAt = &observed
			cpu.Usage.Status = "ok"
			cpu.Usage.LastError = nil
		}
		if index == 2 {
			old := observed.Add(-time.Minute)
			v := 99.0
			cpu.Usage.Value = &v
			cpu.Usage.ObservedAt = &old
			cpu.Usage.Status = "ok"
			cpu.Usage.LastError = nil
		}
		result := ipv6ScanFixture(at, false)
		result.Snapshots[0].Payload["cpu"] = cpu
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
		raw, _ := json.Marshal(snapshots[0]["cpu"])
		var got model.InstanceCPU
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		want := "ok"
		if index > 0 {
			want = "stale"
		}
		if got.Usage.Value == nil || *got.Usage.Value != 0 || got.Usage.Status != want || got.Usage.ObservedAt == nil || !got.Usage.ObservedAt.Equal(observed) || got.CreditBalance.Value != nil || got.CreditBalance.Status != "unknown" {
			t.Fatal(got)
		}
		if index == 2 && got.Usage.LastError.Code != "metric_sample_regressed" {
			t.Fatal(got.Usage)
		}
		if index == 0 {
			var path string
			if err = s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, filepath.Dir(path), false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
		}
	}
}

func TestSWASCPUFailurePreservesItsOwnPreviousSample(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sampled := now.Add(-time.Minute)
	for step := 0; step < 2; step++ {
		at := now.Add(time.Duration(step) * time.Minute)
		j, a := startIPv6Scan(t, s, at)
		usage := model.EmptyCPUMetric("CPU_UTILIZATION", at)
		if step == 0 {
			v := 0.133
			usage.Value = &v
			usage.ObservedAt = &sampled
			usage.Status = "ok"
			usage.LastError = nil
		} else {
			usage.LastError = model.Error("permission_denied")
		}
		cpu := &model.InstanceCPU{Source: "swas_describe_monitor_data", Usage: usage}
		result := model.ScanResult{Snapshots: []model.Snapshot{{ID: "swas", Kind: "swas", Region: "cn-shanghai", Payload: map[string]any{"id": "swas", "cloud_instance_id": "swas-uuid", "name": "light", "status": "running", "cpu": cpu}}}, CompleteScopes: map[string]bool{"swas:cn-shanghai": true}}
		if err := s.SaveScan(ctx, j, a, result, at); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishJob(ctx, j, nil, "", at); err != nil {
			t.Fatal(err)
		}
	}
	snapshots, err := s.Snapshots(ctx, []string{"swas"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshots[0]["cpu"])
	var got model.InstanceCPU
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "swas_describe_monitor_data" || got.Namespace != nil || got.CreditBalance != nil || got.Usage.Value == nil || *got.Usage.Value != 0.133 || got.Usage.Status != "stale" || !got.Usage.ObservedAt.Equal(sampled) || got.Usage.LastError.Code != "permission_denied" {
		t.Fatal(got)
	}
}

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func swasTrafficScan(plan string, at time.Time, present *bool, usage *model.SWASTrafficUsage, sourceError *model.APIError) model.ScanResult {
	return model.ScanResult{
		Snapshots:   []model.Snapshot{{ID: "swas", Kind: "swas", Region: "cn-shanghai", Payload: map[string]any{"id": "swas", "account_id": "a", "kind": "swas", "region_id": "cn-shanghai", "cloud_instance_id": "swas-fixture", "spec": plan, "name": "light fixture", "status": "running", "public_ips": []any{}, "updated_at": at.Format(time.RFC3339Nano)}}},
		SWASTraffic: []model.SWASTrafficObservation{{InstanceID: "swas", PlanID: plan, QueriedAt: at, EntryPresent: present, Usage: usage, Error: sourceError}},
	}
}

func readSWASTraffic(t *testing.T, s *Store, plan string, now time.Time) model.SWASTrafficPackage {
	t.Helper()
	values, err := s.SWASTrafficPackages(context.Background(), []model.SWASTrafficTarget{{InstanceID: "swas", PlanID: plan}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return values["swas"]
}

func TestSWASTrafficCachePreservesTimesAndClearsOnlyConfirmedEmptyUsage(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	yes, no := true, false
	usage := &model.SWASTrafficUsage{Total: 20, Used: 25, Remaining: -5, Overflow: 5}
	saveEventScan(t, s, swasTrafficScan("plan-1", now, &yes, usage, nil), now)
	v := readSWASTraffic(t, s, "plan-1", now)
	if v.Status != "ok" || v.Usage == nil || *v.Usage != *usage || v.EntryPresent == nil || !*v.EntryPresent || !v.FetchedAt.Equal(now) || v.OfficialAsOf != nil || v.NextResetAt != nil || v.Direction != "outbound" || v.ResetDay != 1 {
		t.Fatal(v)
	}
	if v := readSWASTraffic(t, s, "plan-1", now.Add(10*time.Minute)); v.Status != "ok" {
		t.Fatal("exact freshness boundary changed", v)
	}
	if v := readSWASTraffic(t, s, "plan-1", now.Add(10*time.Minute+time.Nanosecond)); v.Status != "stale" {
		t.Fatal(v)
	}
	saveEventScan(t, s, swasTrafficScan("plan-1", now.Add(time.Minute), nil, nil, model.Error("permission_denied")), now.Add(time.Minute))
	v = readSWASTraffic(t, s, "plan-1", now.Add(time.Minute))
	if v.Status != "unavailable" || v.Usage == nil || *v.Usage != *usage || !v.FetchedAt.Equal(now) || !v.LastAttemptAt.Equal(now.Add(time.Minute)) || v.LastError.Code != "permission_denied" {
		t.Fatal("failure replaced successful usage", v)
	}
	saveEventScan(t, s, swasTrafficScan("plan-1", now.Add(2*time.Minute), &no, nil, nil), now.Add(2*time.Minute))
	v = readSWASTraffic(t, s, "plan-1", now.Add(2*time.Minute))
	if v.Status != "ok" || v.EntryPresent == nil || *v.EntryPresent || v.Usage != nil || !v.FetchedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatal("empty response became zero usage", v)
	}
	saveEventScan(t, s, swasTrafficScan("plan-1", now.Add(time.Minute), &yes, usage, nil), now.Add(3*time.Minute))
	if v := readSWASTraffic(t, s, "plan-1", now.Add(3*time.Minute)); *v.EntryPresent || v.Usage != nil || !v.FetchedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatal("source time regressed", v)
	}
	saveEventScan(t, s, swasTrafficScan("plan-2", now.Add(4*time.Minute), nil, nil, model.Error("aliyun_unreachable")), now.Add(4*time.Minute))
	if v := readSWASTraffic(t, s, "plan-2", now.Add(4*time.Minute)); v.Status != "unavailable" || v.Usage != nil || v.EntryPresent != nil || v.FetchedAt != nil {
		t.Fatal("different plan reused old data", v)
	}
}

func TestSWASTrafficCredentialRotationDisableAndRollback(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	yes := true
	usage := &model.SWASTrafficUsage{Total: 50, Used: 0, Remaining: 50, Overflow: 0}
	saveEventScan(t, s, swasTrafficScan("plan", now, &yes, usage, nil), now)
	if _, err := s.UpdateAccount(ctx, "a", func(*model.Account) ([]byte, error) { return make([]byte, 32), nil }, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if v := readSWASTraffic(t, s, "plan", now.Add(time.Minute)); v.Status != "never_fetched" || v.Usage != nil || v.FetchedAt != nil || v.EntryPresent != nil {
		t.Fatal("new credentials reused old cache", v)
	}
	saveEventScan(t, s, swasTrafficScan("plan", now.Add(2*time.Minute), &yes, usage, nil), now.Add(2*time.Minute))
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now.Add(3*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if v := readSWASTraffic(t, s, "plan", now.Add(3*time.Minute)); v.Status != "disabled" || v.Usage == nil || v.Usage.Used != 0 {
		t.Fatal("disabled history lost or reported active", v)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = true; return nil, nil }, now.Add(4*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	j, a := startIPv6Scan(t, s, now.Add(5*time.Minute))
	bad := swasTrafficScan("plan", now.Add(5*time.Minute), &yes, usage, nil)
	bad.Snapshots[0].Payload["name"] = "must roll back"
	bad.SWASTraffic[0].PlanID = "wrong-plan"
	if err := s.SaveScan(ctx, j, a, bad, now.Add(5*time.Minute)); !errors.Is(err, ErrSWASTraffic) {
		t.Fatal("mismatched observation accepted", err)
	}
	var name string
	if err := s.DB.QueryRow("SELECT name FROM instance WHERE id='swas'").Scan(&name); err != nil || name != "light fixture" {
		t.Fatal("snapshot write escaped failed cache transaction", name, err)
	}
	if v := readSWASTraffic(t, s, "plan", now.Add(5*time.Minute)); !v.FetchedAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatal("failed transaction advanced source time", v)
	}
}

func TestSWASTrafficUnknownPlanDoesNotRetainFailurePayload(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	yes := true
	saveEventScan(t, s, swasTrafficScan("", now, &yes, &model.SWASTrafficUsage{}, nil), now)
	saveEventScan(t, s, swasTrafficScan("", now.Add(time.Minute), nil, nil, model.Error("aliyun_unreachable")), now.Add(time.Minute))
	v := readSWASTraffic(t, s, "", now.Add(time.Minute))
	if v.Status != "unavailable" || v.Usage != nil || v.EntryPresent != nil || v.FetchedAt != nil {
		t.Fatal("unknown plan retained unbound package data", v)
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
)

type swasTrafficAPIScanner struct {
	calls int
	at    time.Time
}

func (s *swasTrafficAPIScanner) Scan(_ context.Context, a model.Account, _ model.Credential, _ bool, before func() error) model.ScanResult {
	s.calls++
	if before() != nil {
		return model.ScanResult{Failure: "account_changed"}
	}
	yes := true
	id := a.ID + "_swas"
	return model.ScanResult{
		Snapshots: []model.Snapshot{
			{ID: id, Kind: "swas", Region: "cn-shanghai", Payload: map[string]any{"id": id, "account_id": a.ID, "cloud_instance_id": "swas-fixture", "name": "light fixture", "kind": "swas", "region_id": "cn-shanghai", "status": "running", "spec": "plan-test", "updated_at": s.at.Format(time.RFC3339Nano)}},
			{ID: a.ID + "_ecs", Kind: "ecs", Region: "cn-shanghai", Payload: map[string]any{"id": a.ID + "_ecs", "account_id": a.ID, "cloud_instance_id": "i-fixture", "name": "ECS fixture", "kind": "ecs", "region_id": "cn-shanghai", "status": "running", "updated_at": s.at.Format(time.RFC3339Nano)}},
		},
		SWASTraffic: []model.SWASTrafficObservation{{InstanceID: id, PlanID: "plan-test", QueriedAt: s.at, EntryPresent: &yes, Usage: &model.SWASTrafficUsage{Total: 100, Used: 120, Remaining: -20, Overflow: 20}}},
	}
}

func TestSWASTrafficInstanceReadsUseOnlyScopedCacheAndPreserveSourceTime(t *testing.T) {
	p := testPanel(t)
	box, err := security.NewBox(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	scanner := &swasTrafficAPIScanner{at: now}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: 5 * time.Minute}
	cookie, _ := setupPanel(t, p)
	ctx := context.Background()
	a, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{Name: "traffic fixture", Site: "china", Credential: model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "api-test-fixture-not-a-real-secret"}, RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := p.engine.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	p.now = func() time.Time { return now }
	read := func() *model.SWASTrafficPackage {
		t.Helper()
		w := panelRequest(p, "GET", "/instances", nil, cookie, "")
		var response struct {
			Items []struct {
				Kind    string                    `json:"kind"`
				Package *model.SWASTrafficPackage `json:"swas_traffic_package"`
			} `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Items) != 2 {
			t.Fatal(w.Code, w.Body)
		}
		var result *model.SWASTrafficPackage
		for _, item := range response.Items {
			if item.Kind == "swas" {
				result = item.Package
			} else if item.Package != nil {
				t.Fatal("ECS received SWAS traffic")
			}
		}
		if result == nil || scanner.calls != 1 {
			t.Fatal("GET performed discovery or lost the source")
		}
		return result
	}
	v := read()
	if v.Status != "ok" || v.EntryPresent == nil || !*v.EntryPresent || v.Usage == nil || v.Usage.Used != 120 || v.Usage.Remaining != -20 || v.Usage.Overflow != 20 || !v.FetchedAt.Equal(now) || v.OfficialAsOf != nil || v.NextResetAt != nil {
		t.Fatal(v)
	}
	p.now = func() time.Time { return now.Add(11 * time.Minute) }
	v = read()
	if v.Status != "stale" || !v.FetchedAt.Equal(now) || !v.LastAttemptAt.Equal(now) {
		t.Fatal("stale GET moved the source time", v)
	}
	var sourceTime string
	if err := p.s.DB.QueryRow("SELECT fetched_at FROM swas_traffic_cache").Scan(&sourceTime); err != nil || sourceTime != now.Format("2006-01-02T15:04:05.000000000Z") {
		t.Fatal("GET rewrote source state", sourceTime, err)
	}
	rotatedID, rotatedSecret := "TestRotatedKeyID123456", "rotated-api-fixture-not-a-real-secret"
	if _, err := p.engine.UpdateAccount(ctx, a.ID, model.AccountUpdate{AccessKeyID: &rotatedID, AccessKeySecret: &rotatedSecret}); err != nil {
		t.Fatal(err)
	}
	v = read()
	if v.Status != "never_fetched" || v.EntryPresent != nil || v.Usage != nil || v.FetchedAt != nil {
		t.Fatal("old credential values exposed", v)
	}
	if w := panelRequest(p, "GET", "/instances", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
}

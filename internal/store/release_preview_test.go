package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func releaseTestBool(v bool) *bool { return &v }

func releaseTestScan(at time.Time) model.ScanResult {
	return model.ScanResult{
		Snapshots:      []model.Snapshot{eventSnapshot("stopped", nil, nil)},
		CompleteScopes: map[string]bool{model.Scope("ecs", "cn-shanghai"): true},
		ReleaseInventory: []model.ReleaseInventoryObservation{{Region: "cn-shanghai", QueriedAt: at, Data: &model.ReleaseInventory{
			Instances: []model.ReleaseInstanceFacts{{CloudID: "i-test", Name: "exact provider name", Status: "Stopped", ChargeType: "PostPaid", DeletionProtection: releaseTestBool(false), Locked: releaseTestBool(false)}},
			Disks:     []model.ReleaseDiskFacts{{CloudID: "d-test", Category: "cloud_essd", Type: "system", InstanceIDs: []string{"i-test"}, AttachmentsKnown: true, DeleteWithInstance: releaseTestBool(true), DeleteAutoSnapshot: releaseTestBool(false)}},
		}}},
	}
}

func readReleasePreview(t *testing.T, s *Store, at time.Time) model.ReleasePreview {
	t.Helper()
	v, err := s.ReleasePreview(context.Background(), "ecs", at)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReleasePreviewFreshnessFailureAndConfirmedEmpty(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	saveEventScan(t, s, model.ScanResult{Snapshots: []model.Snapshot{eventSnapshot("stopped", nil, nil)}}, now)
	if v := readReleasePreview(t, s, now); v.Status != "never_fetched" || v.Impact != nil || v.Token != nil {
		t.Fatal(v)
	}
	saveEventScan(t, s, releaseTestScan(now), now)
	v := readReleasePreview(t, s, now)
	if v.Status != "eligible" || v.Token == nil || len(*v.Token) != 64 || v.ExecutionEnabled || v.Impact == nil || v.Impact.Name != "exact provider name" || len(v.Impact.Disks) != 1 || !v.FetchedAt.Equal(now) {
		t.Fatal(v)
	}
	if v := readReleasePreview(t, s, now.Add(model.ReleasePreviewLifetime)); v.Status != "stale" || v.Token != nil || v.Impact == nil {
		t.Fatal("expiration boundary accepted", v)
	}
	if v := readReleasePreview(t, s, now.Add(-time.Second)); v.Status != "stale" || v.Token != nil {
		t.Fatal("future evidence accepted", v)
	}
	failure := releaseTestScan(now.Add(time.Minute))
	failure.ReleaseInventory[0].Data = nil
	failure.ReleaseInventory[0].Error = model.Error("permission_denied")
	saveEventScan(t, s, failure, now.Add(time.Minute))
	v = readReleasePreview(t, s, now.Add(time.Minute))
	if v.Status != "unavailable" || v.Token != nil || v.Impact == nil || len(v.Impact.Disks) != 1 || !v.FetchedAt.Equal(now) || !v.LastAttemptAt.Equal(now.Add(time.Minute)) || v.LastError.Code != "permission_denied" {
		t.Fatal("failed read replaced the source facts/time", v)
	}
	saveEventScan(t, s, releaseTestScan(now.Add(30*time.Second)), now.Add(2*time.Minute))
	if v := readReleasePreview(t, s, now.Add(2*time.Minute)); v.Status != "unavailable" || !v.LastAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatal("older read replaced a newer attempt", v)
	}
	empty := releaseTestScan(now.Add(3 * time.Minute))
	empty.ReleaseInventory[0].Data.Disks = []model.ReleaseDiskFacts{}
	saveEventScan(t, s, empty, now.Add(3*time.Minute))
	v = readReleasePreview(t, s, now.Add(3*time.Minute))
	if v.Status != "blocked" || v.Token != nil || len(v.Impact.Disks) != 0 || !slices.Contains(v.BlockReasons, "release_disk_information_incomplete") {
		t.Fatal("complete empty disk list retained previous disks", v)
	}
}

func TestReleasePreviewCredentialAndResourceScope(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	original := *readReleasePreview(t, s, now).Token
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if v := readReleasePreview(t, s, now); v.Status != "blocked" || v.Token != nil || v.Impact == nil || !slices.Contains(v.BlockReasons, "account_disabled") {
		t.Fatal(v)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = true; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if v := readReleasePreview(t, s, now); v.Status != "eligible" || v.Token == nil || *v.Token == original {
		t.Fatal("changed account configuration reused its confirmation", v)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(*model.Account) ([]byte, error) { return make([]byte, 32), nil }, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if v := readReleasePreview(t, s, now.Add(time.Minute)); v.Status != "never_fetched" || v.Impact != nil || v.Token != nil {
		t.Fatal("credential rotation exposed predecessor facts", v)
	}
	saveEventScan(t, s, releaseTestScan(now.Add(2*time.Minute)), now.Add(2*time.Minute))
	if _, err := s.DB.Exec("UPDATE resource_snapshot SET present=0 WHERE kind='ecs' AND id='ecs'"); err != nil {
		t.Fatal(err)
	}
	if v := readReleasePreview(t, s, now.Add(2*time.Minute)); v.Status != "blocked" || v.Token != nil || !slices.Contains(v.BlockReasons, "instance_not_present") {
		t.Fatal(v)
	}
}

func TestReleasePreviewInvalidObservationRollsBackScan(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	saveEventScan(t, s, releaseTestScan(now), now)
	j, a := startIPv6Scan(t, s, now.Add(time.Minute))
	bad := releaseTestScan(now.Add(time.Minute))
	bad.Snapshots[0].Payload["name"] = "must not be saved"
	bad.ReleaseInventory = append(bad.ReleaseInventory, bad.ReleaseInventory[0])
	if err := s.SaveScan(context.Background(), j, a, bad, now.Add(time.Minute)); !errors.Is(err, ErrReleaseInventory) {
		t.Fatal(err)
	}
	v := readReleasePreview(t, s, now.Add(time.Minute))
	if !v.FetchedAt.Equal(now) {
		t.Fatal("failed transaction advanced evidence", v)
	}
	var name string
	if err := s.DB.QueryRow("SELECT name FROM instance WHERE id='ecs'").Scan(&name); err != nil || name == "must not be saved" {
		t.Fatal(name, err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

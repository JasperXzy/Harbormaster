package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func idleFixture(kind, id string, at time.Time) model.IdleCandidate {
	return model.IdleCandidate{ID: "a_" + kind + "_cn-shanghai_" + id, AccountID: "a", RegionID: "cn-shanghai", Kind: kind, CloudID: id, Name: "fixture " + id, Reason: map[string]string{"eip": "unbound_eip", "disk": "unattached_cloud_disk", "ecs": "stopped_keep_charging", "swas": "stopped_subscription"}[kind], ChargeType: "pay_as_you_go", CloudStatus: "Available", InstanceIDs: []string{}, ObservedAt: at}
}

func idleScan(kind string, at time.Time, scanned int, unknown []string, items ...model.IdleCandidate) model.ScanResult {
	if items == nil {
		items = []model.IdleCandidate{}
	}
	if unknown == nil {
		unknown = []string{}
	}
	return model.ScanResult{Idle: []model.IdleObservation{{Region: "cn-shanghai", Kind: kind, QueriedAt: at, Data: &model.IdleData{Scanned: scanned, UnclassifiedIDs: unknown, Candidates: items}}}}
}

func readIdle(t *testing.T, s *Store, kind string, at time.Time) ([]model.IdleCandidate, model.IdleSource) {
	t.Helper()
	items, sources, err := s.IdleResources(context.Background(), model.IdleFilter{AccountID: "a", Kind: kind, Limit: 200}, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if source.RegionID == "cn-shanghai" && source.Kind == kind {
			return items, source
		}
	}
	t.Fatal("configured source missing", sources)
	return nil, model.IdleSource{}
}

func TestIdleCacheRetainsUnknownEvidenceAndClearsKnownChanges(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	if items, source := readIdle(t, s, "eip", now); len(items) != 0 || source.Status != "never_fetched" || source.Scanned != nil || source.Candidates != nil {
		t.Fatal(items, source)
	}
	saveEventScan(t, s, idleScan("eip", now, 1, nil, idleFixture("eip", "eip-one", now)), now)
	items, source := readIdle(t, s, "eip", now)
	if len(items) != 1 || items[0].EvidenceStatus != "current" || items[0].MonthlyCost != nil || items[0].Currency != nil || source.Status != "ok" || *source.Candidates != 1 || *source.Unclassified != 0 {
		t.Fatal(items, source)
	}
	partialAt := now.Add(time.Minute)
	saveEventScan(t, s, idleScan("eip", partialAt, 2, []string{"eip-one"}, idleFixture("eip", "eip-two", partialAt)), partialAt)
	items, source = readIdle(t, s, "eip", partialAt)
	if len(items) != 2 || source.Status != "partial" || *source.Candidates != 2 || *source.Unclassified != 1 {
		t.Fatal(items, source)
	}
	for _, item := range items {
		if item.CloudID == "eip-one" && (item.EvidenceStatus != "retained" || !item.ObservedAt.Equal(now)) {
			t.Fatal("unknown resource was refreshed or cleared", item)
		}
		if item.CloudID == "eip-two" && (item.EvidenceStatus != "current" || !item.ObservedAt.Equal(partialAt)) {
			t.Fatal(item)
		}
	}
	knownAt := now.Add(2 * time.Minute)
	saveEventScan(t, s, idleScan("eip", knownAt, 2, nil, idleFixture("eip", "eip-two", knownAt)), knownAt)
	items, source = readIdle(t, s, "eip", knownAt)
	if len(items) != 1 || items[0].CloudID != "eip-two" || items[0].EvidenceStatus != "current" || source.Status != "ok" {
		t.Fatal(items, source)
	}
	failedAt := now.Add(3 * time.Minute)
	saveEventScan(t, s, model.ScanResult{Idle: []model.IdleObservation{{Region: "cn-shanghai", Kind: "eip", QueriedAt: failedAt, Error: model.Error("permission_denied")}}}, failedAt)
	items, source = readIdle(t, s, "eip", failedAt)
	if len(items) != 1 || items[0].EvidenceStatus != "retained" || !items[0].ObservedAt.Equal(knownAt) || source.Status != "failing" || !source.FetchedAt.Equal(knownAt) || !source.LastAttemptAt.Equal(failedAt) {
		t.Fatal(items, source)
	}
	saveEventScan(t, s, idleScan("eip", partialAt, 1, nil, idleFixture("eip", "eip-one", partialAt)), now.Add(4*time.Minute))
	items, source = readIdle(t, s, "eip", now.Add(4*time.Minute))
	if len(items) != 1 || items[0].CloudID != "eip-two" || !source.LastAttemptAt.Equal(failedAt) {
		t.Fatal("older result replaced newer evidence", items, source)
	}
	emptyAt := now.Add(5 * time.Minute)
	saveEventScan(t, s, idleScan("eip", emptyAt, 0, nil), emptyAt)
	items, source = readIdle(t, s, "eip", emptyAt)
	if len(items) != 0 || source.Status != "ok" || *source.Scanned != 0 || *source.Candidates != 0 || source.LastError != nil {
		t.Fatal("complete empty did not clear candidates", items, source)
	}
}

func TestIdleCacheCredentialsDisabledScopeAndPagination(t *testing.T) {
	s := powerStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	saveEventScan(t, s, idleScan("eip", now, 3, nil, idleFixture("eip", "eip-one", now), idleFixture("eip", "eip-two", now), idleFixture("eip", "eip-three", now)), now)
	f := model.IdleFilter{AccountID: "a", Kind: "eip", Limit: 2}
	first, _, err := s.IdleResources(ctx, f, now)
	if err != nil || len(first) != 2 || first[0].ID >= first[1].ID {
		t.Fatal(first, err)
	}
	f.After = first[1].ID
	second, _, err := s.IdleResources(ctx, f, now)
	if err != nil || len(second) != 1 || second[0].ID <= f.After {
		t.Fatal(second, err)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if items, source := readIdle(t, s, "eip", now.Add(time.Minute)); len(items) != 3 || source.Status != "disabled" || items[0].EvidenceStatus != "retained" {
		t.Fatal(items, source)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) {
		a.Enabled = true
		a.RegionIDs = []string{"cn-hongkong"}
		return nil, nil
	}, now.Add(2*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if items, source := readIdle(t, s, "eip", now.Add(2*time.Minute)); len(items) != 3 || source.Status != "disabled" || source.Enabled {
		t.Fatal("removed region remained current", items, source)
	}
	if _, err := s.UpdateAccount(ctx, "a", func(*model.Account) ([]byte, error) { return make([]byte, 32), nil }, now.Add(3*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	items, sources, err := s.IdleResources(ctx, model.IdleFilter{AccountID: "a", Kind: "eip", Limit: 50}, now.Add(3*time.Minute))
	if err != nil || len(items) != 0 || len(sources) != 1 || sources[0].RegionID != "cn-hongkong" || sources[0].Status != "never_fetched" || sources[0].FetchedAt != nil {
		t.Fatal("credential rotation exposed predecessor data", items, sources, err)
	}
}

func TestIdleCacheRollbackAndFirstFailureKeepUnknownCounts(t *testing.T) {
	s := powerStore(t)
	now := time.Now().UTC()
	saveEventScan(t, s, model.ScanResult{Idle: []model.IdleObservation{{Region: "cn-shanghai", Kind: "disk", QueriedAt: now, Error: model.Error("pagination_incomplete")}}}, now)
	if items, source := readIdle(t, s, "disk", now); len(items) != 0 || source.Status != "failing" || source.Scanned != nil || source.Candidates != nil || source.FetchedAt != nil {
		t.Fatal("first failure became empty successful data", items, source)
	}
	saveEventScan(t, s, idleScan("eip", now, 1, nil, idleFixture("eip", "eip-old", now)), now)
	j, a := startIPv6Scan(t, s, now.Add(time.Minute))
	bad := idleScan("eip", now.Add(time.Minute), 1, nil, idleFixture("eip", "eip-new", now.Add(time.Minute)))
	bad.Idle = append(bad.Idle, bad.Idle[0])
	if err := s.SaveScan(context.Background(), j, a, bad, now.Add(time.Minute)); !errors.Is(err, ErrIdleObservation) {
		t.Fatal(err)
	}
	if items, source := readIdle(t, s, "eip", now.Add(time.Minute)); len(items) != 1 || items[0].CloudID != "eip-old" || !source.FetchedAt.Equal(now) {
		t.Fatal("failed transaction published candidates", items, source)
	}
	var broken int
	if err := s.DB.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil || broken != 0 {
		t.Fatal(broken, err)
	}
}

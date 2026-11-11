package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type scanFunc func(context.Context, model.Account, model.Credential, bool, func() error) model.ScanResult

func (f scanFunc) Scan(ctx context.Context, a model.Account, c model.Credential, p bool, before func() error) model.ScanResult {
	return f(ctx, a, c, p, before)
}
func testEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := store.Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, err := security.NewBox(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s, Box: box, Workers: 2, Interval: 5 * time.Minute}, dir
}
func testCreate(name string) model.AccountCreate {
	return model.AccountCreate{Name: name, Site: "china", Credential: model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "test-secret-never-log-or-persist-plaintext"}, RegionIDs: []string{"cn-shanghai"}}
}
func oneSnapshot(a model.Account) model.ScanResult {
	id := a.ID + "_instance"
	return model.ScanResult{Snapshots: []model.Snapshot{{Kind: "ecs", ID: id, Region: "cn-shanghai", Payload: map[string]any{"id": id, "account_id": a.ID, "cloud_instance_id": "i-test", "name": "test instance", "kind": "ecs", "region_id": "cn-shanghai", "status": "running", "updated_at": time.Now().UTC().Format(time.RFC3339)}}}, CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}}
}
func TestEncryptedAccountTransactionsAndRetention(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	in := testCreate("first")
	a, j, err := e.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "queued" || a.QuotaOverseas != 200<<30 || a.AccessKeyIDMasked == in.AccessKeyID {
		t.Fatal("invalid account defaults")
	}
	if _, _, err = e.CreateAccount(ctx, in); !errors.Is(err, store.ErrNameTaken) {
		t.Fatal("duplicate name accepted", err)
	}
	for _, query := range []string{"SELECT config_json FROM account_settings", "SELECT account_ids_json FROM job"} {
		var raw string
		if err = e.Store.DB.QueryRow(query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, in.AccessKeyID) || strings.Contains(raw, in.AccessKeySecret) {
			t.Fatal("credential leaked into metadata or job")
		}
	}
	cipher, err := e.Store.AccountCipher(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(in.AccessKeySecret)) {
		t.Fatal("secret plaintext stored")
	}
	raw, err := e.Box.Decrypt(cipher, model.CredentialOwner(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	var recovered model.Credential
	if json.Unmarshal(raw, &recovered) != nil || recovered != in.Credential {
		t.Fatal("encrypted credential did not round-trip")
	}
	if _, err = e.Box.Decrypt(cipher, model.CredentialOwner("another-account")); err == nil {
		t.Fatal("row swap accepted")
	}
	e.Scanner = scanFunc(func(_ context.Context, a model.Account, c model.Credential, _ bool, before func() error) model.ScanResult {
		if c != in.Credential || before() != nil {
			t.Fatal("worker credential or lease")
		}
		return oneSnapshot(a)
	})
	if ran, err := e.RunOne(ctx); err != nil || !ran {
		t.Fatal(ran, err)
	}
	job, err := e.Store.Job(ctx, j.ID)
	if err != nil || job.Status != "succeeded" {
		t.Fatal(job, err)
	}
	list, err := e.Store.Accounts(ctx)
	if err != nil || len(list) != 1 || list[0].InstanceCount != 1 || list[0].SyncStatus != "ok" {
		t.Fatal(list, err)
	}
	off := false
	if _, err = e.UpdateAccount(ctx, a.ID, model.AccountUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Enqueue(ctx, "refresh", a.ID); !errors.Is(err, store.ErrAccountDisabled) {
		t.Fatal("disabled refresh accepted", err)
	}
	snapshots, err := e.Store.Snapshots(ctx, []string{"ecs"}, a.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatal("history removed on disable")
	}
	if err = e.Store.ScheduleScans(ctx, time.Minute, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.RunOne(ctx); err != nil || ran {
		t.Fatal("disabled account scheduled", ran, err)
	}
	newID, newSecret := "NewTestAccessKey654321", "rotated-secret-not-a-real-key"
	if _, err = e.UpdateAccount(ctx, a.ID, model.AccountUpdate{AccessKeyID: &newID}); err == nil {
		t.Fatal("half rotation accepted")
	}
	if _, err = e.UpdateAccount(ctx, a.ID, model.AccountUpdate{AccessKeyID: &newID, AccessKeySecret: &newSecret}); err != nil {
		t.Fatal(err)
	}
	rotated, err := e.Store.AccountCipher(ctx, a.ID)
	if err != nil || bytes.Equal(rotated, cipher) {
		t.Fatal("rotation not committed")
	}
}
func TestStaleWorkerCannotPublishAfterAccountDisable(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	a, j, err := e.CreateAccount(ctx, testCreate("stale"))
	if err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	e.Scanner = scanFunc(func(_ context.Context, a model.Account, _ model.Credential, _ bool, before func() error) model.ScanResult {
		close(entered)
		<-resume
		return oneSnapshot(a)
	})
	done := make(chan error, 1)
	go func() { _, err := e.RunOne(ctx); done <- err }()
	<-entered
	off := false
	if _, err = e.UpdateAccount(ctx, a.ID, model.AccountUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	close(resume)
	err = <-done
	if err != nil && !errors.Is(err, store.ErrLeaseLost) {
		t.Fatal(err)
	}
	items, err := e.Store.Snapshots(ctx, []string{"ecs"}, a.ID)
	if err != nil || len(items) != 0 {
		t.Fatal("stale result published")
	}
	job, err := e.Store.Job(ctx, j.ID)
	if err != nil || job.Status != "failed" || job.Error.Code != "account_changed" {
		t.Fatal("stale job not fenced", job, err)
	}
}
func TestJobsRecoverAndFenceAcrossStores(t *testing.T) {
	e, dir := testEngine(t)
	ctx := context.Background()
	a, j, err := e.CreateAccount(ctx, testCreate("lease"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now := time.Now().UTC()
	claimed := make(chan model.Job, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, s := range []*store.Store{e.Store, other} {
		wg.Add(1)
		go func(s *store.Store) { defer wg.Done(); job, err := s.ClaimJob(ctx, now); claimed <- job; errs <- err }(s)
	}
	wg.Wait()
	close(claimed)
	close(errs)
	var first model.Job
	count := 0
	for err := range errs {
		if err == nil {
			count++
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	}
	for v := range claimed {
		if v.Owner != "" {
			first = v
		}
	}
	if count != 1 || first.ID != j.ID {
		t.Fatal("job leased twice")
	}
	second, err := other.ClaimJob(ctx, now.Add(61*time.Second))
	if err != nil || second.ID != j.ID || second.Owner == first.Owner {
		t.Fatal("expired job not recovered", err)
	}
	if err = e.Store.FinishJob(ctx, first, nil, "", now.Add(62*time.Second)); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatal("old owner finished new lease", err)
	}
	if err = other.FinishJob(ctx, second, nil, "", now.Add(62*time.Second)); err != nil {
		t.Fatal(err)
	}
	fresh, err := e.Enqueue(ctx, "refresh", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := e.Enqueue(ctx, "refresh", a.ID)
	if err != nil || fresh.ID != duplicate.ID {
		t.Fatal("active enqueue not coalesced")
	}
}
func TestIncompleteScanRetainsPriorResources(t *testing.T) {
	e, _ := testEngine(t)
	ctx := context.Background()
	a, _, err := e.CreateAccount(ctx, testCreate("partial"))
	if err != nil {
		t.Fatal(err)
	}
	pass := 0
	e.Scanner = scanFunc(func(_ context.Context, a model.Account, _ model.Credential, _ bool, _ func() error) model.ScanResult {
		pass++
		if pass == 1 {
			return oneSnapshot(a)
		}
		return model.ScanResult{Failure: "permission_denied", CompleteScopes: map[string]bool{"ecs:cn-shanghai": false}}
	})
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Enqueue(ctx, "refresh", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := e.Store.Snapshots(ctx, []string{"ecs"}, a.ID)
	if err != nil || len(items) != 1 {
		t.Fatal("failed scan deleted prior data")
	}
	state, err := e.Store.Account(ctx, a.ID)
	if err != nil || state.ConsecutiveFailures != 1 || state.LastSyncAt == nil {
		t.Fatal("failure freshness not retained")
	}
	e.Scanner = scanFunc(func(context.Context, model.Account, model.Credential, bool, func() error) model.ScanResult {
		return model.ScanResult{CompleteScopes: map[string]bool{"ecs:cn-shanghai": true}}
	})
	if _, err = e.Enqueue(ctx, "refresh", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = e.Store.Snapshots(ctx, []string{"ecs"}, a.ID)
	if err != nil || len(items) != 0 {
		t.Fatal("confirmed missing resource still listed")
	}
	var retained int
	if err = e.Store.DB.QueryRow("SELECT count(*) FROM resource_snapshot WHERE account_id=?", a.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("historical resource row deleted")
	}
}

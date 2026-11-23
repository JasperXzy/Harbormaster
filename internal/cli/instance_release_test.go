package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestReleaseWorkerFactoryDefaultsOffAndIsIndependent(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := security.NewBox(bytes.Repeat([]byte{1}, 32))
	collector := &collect.Engine{Store: s, Box: box}
	for _, c := range []config.Config{{}, {InstancePowerEnabled: true}, {Dev: true, InstanceReleaseEnabled: true}} {
		if newReleaseEngine(c, s, collector, nil) != nil {
			t.Fatal("release enabled without its production flag")
		}
	}
	if newReleaseEngine(config.Config{InstanceReleaseEnabled: true}, s, nil, nil) != nil || newReleaseEngine(config.Config{InstanceReleaseEnabled: true}, s, &collect.Engine{Store: s}, nil) != nil {
		t.Fatal("release enabled without credentials")
	}
	e := newReleaseEngine(config.Config{InstanceReleaseEnabled: true, WorkerCount: 3}, s, collector, nil)
	if e == nil || !e.Enabled || e.Workers != 3 || e.Writer == nil || e.Observer == nil || e.Box != box {
		t.Fatal("configured release worker unavailable")
	}
	// Construction only. The real adapters are never run in this test.
}

func TestRunOnceLeavesReleaseQueuedEvenWhenServeReleaseEnabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)))
	t.Setenv("HM_MASTER_KEY_FILE", "")
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "false")
	t.Setenv("HM_INSTANCE_RELEASE_ENABLED", "true")
	t.Setenv("HM_DEV", "false")
	t.Setenv("HM_SETUP_TOKEN", "test-bootstrap-not-a-real-secret-0001")
	t.Setenv("HM_SETUP_TOKEN_FILE", "")
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := model.Account{ID: "a", Name: "test", Site: "china", RegionIDs: []string{"cn-shanghai"}, Enabled: true}
	raw, _ := json.Marshal(a)
	if _, err := s.DB.Exec("INSERT INTO account VALUES('a','test','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a',?,1)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO instance VALUES('i','a','i-test','ecs','test','cn-shanghai','stopped','stopped','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','ecs','i','cn-shanghai','{}',1,'now')"); err != nil {
		t.Fatal(err)
	}
	no, yes := false, true
	data := model.ReleaseInventory{Instances: []model.ReleaseInstanceFacts{{CloudID: "i-test", Name: "test", Status: "Stopped", ChargeType: "PostPaid", DeletionProtection: &no, Locked: &no}}, Disks: []model.ReleaseDiskFacts{{CloudID: "d-test", Category: "cloud_essd", Type: "system", InstanceIDs: []string{"i-test"}, AttachmentsKnown: true, DeleteWithInstance: &yes, DeleteAutoSnapshot: &no}}}
	raw, _ = json.Marshal(data)
	if _, err := s.DB.Exec("INSERT INTO release_inventory VALUES('a',1,'cn-shanghai',?,?,NULL,?)", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ReleasePreview(ctx, "i", now)
	if err != nil || preview.Token == nil {
		t.Fatal(preview, err)
	}
	j, err := s.EnqueueReleaseJob(ctx, "i", "test", *preview.Token, model.NewID("event_"), now)
	if err != nil {
		t.Fatal(err)
	}
	// This synthetic account deliberately has no credential and is excluded
	// from collection, so run-once cannot issue any cloud requests at all.
	if _, err := s.DB.Exec("UPDATE account_settings SET enabled=0,config_json=json_set(config_json,'$.enabled',json('false'))"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if code := Server(ctx, []string{"run-once", "--data-dir", dir}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatal(code, logs.String())
	}
	s, err = store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Job(ctx, j.ID)
	if err != nil || got.Status != "queued" || got.Attempts != 0 {
		t.Fatal("run-once consumed release", got, err)
	}
	if !strings.Contains(logs.String(), `"cloud_writes":0`) {
		t.Fatal("run-once lost its read-only declaration")
	}
}

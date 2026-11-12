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

func TestPowerWorkerFactoryDefaultsOffAndExcludesDemo(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := security.NewBox(bytes.Repeat([]byte{1}, 32))
	collector := &collect.Engine{Store: s, Box: box}
	for _, c := range []config.Config{{}, {Dev: true, InstancePowerEnabled: true}} {
		if newPowerEngine(c, s, collector, nil) != nil {
			t.Fatal("unsafe factory enable")
		}
	}
	if newPowerEngine(config.Config{InstancePowerEnabled: true}, s, nil, nil) != nil {
		t.Fatal("missing collector accepted")
	}
	e := newPowerEngine(config.Config{InstancePowerEnabled: true, WorkerCount: 3}, s, collector, nil)
	if e == nil || !e.Enabled || e.Workers != 3 || e.Writer == nil || e.Observer == nil || e.Box != box {
		t.Fatal("configured worker unavailable")
	}
	// Construct only: this test never runs the real cloud adapters.
}

func TestRunOnceDoesNotConsumePowerEvenWhenServePowerEnabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := bytes.Repeat([]byte{4}, 32)
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("HM_MASTER_KEY_FILE", "")
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "true")
	t.Setenv("HM_DEV", "false")
	t.Setenv("HM_SETUP_TOKEN", "test-bootstrap-not-a-real-secret-0001")
	t.Setenv("HM_SETUP_TOKEN_FILE", "")
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	a := model.Account{ID: "a", Name: "test", Site: "china", RegionIDs: []string{"cn-shanghai"}, Enabled: true}
	raw, _ := json.Marshal(a)
	if _, err = s.DB.Exec("INSERT INTO account VALUES('a','test','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a',?,1)", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO instance VALUES('i','a','i-test','ecs','test','cn-shanghai','running','stopped','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','ecs','i','cn-shanghai','{}',1,'now')"); err != nil {
		t.Fatal(err)
	}
	j, err := s.EnqueuePowerJob(ctx, "i", "stop", "keep_charging", model.NewID("event_"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Disable collection in this synthetic account; it has no usable credential.
	if _, err = s.DB.Exec("UPDATE account_settings SET enabled=0,config_json=json_set(config_json,'$.enabled',json('false'))"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
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
		t.Fatal("run-once consumed a cloud write", got, err)
	}
	if !strings.Contains(logs.String(), `"cloud_writes":0`) {
		t.Fatal("run-once log no longer declares its read-only result")
	}
}

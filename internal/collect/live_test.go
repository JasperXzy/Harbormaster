package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/store"
)

// Optional local acceptance check. It reads an already collected production
// database; no credentials are loaded and no cloud requests are made here.
func TestCollectedLiveInventory(t *testing.T) {
	dir := os.Getenv("HM_TEST_COLLECTED_DATA_DIR")
	if dir == "" {
		t.Skip("set HM_TEST_COLLECTED_DATA_DIR to inspect a completed local scan")
	}
	if _, err := os.Stat(filepath.Join(dir, "harbormaster.db")); err != nil {
		t.Fatal("collected database does not exist")
	}
	s, err := store.Open(context.Background(), dir, false)
	if err != nil {
		t.Fatal("cannot open collected database")
	}
	defer s.Close()
	accounts, err := s.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) == 0 {
		t.Fatal("no accounts imported")
	}
	for _, a := range accounts {
		code := ""
		if a.LastError != nil {
			code = a.LastError.Code
		}
		t.Logf("account=%s enabled=%t sync=%s instances=%d error=%s", a.Name, a.Enabled, a.SyncStatus, a.InstanceCount, code)
		if a.PermissionCheck != nil {
			for _, p := range a.PermissionCheck.Items {
				t.Logf("account=%s permission=%s result=%s", a.Name, p.Key, p.Result)
			}
		}
		items, err := s.Snapshots(context.Background(), []string{"ecs", "swas"}, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range items {
			t.Logf("instance=%s kind=%s region=%s billing=%v protection=%v", i["name"], i["kind"], i["region_id"], i["billing_subject_kind"], i["realtime_protection"])
		}
		if len(items) == 0 {
			t.Error("no instance snapshots for imported account", a.Name)
		}
		if a.SyncStatus != "ok" {
			t.Error("account scan is not fully successful", a.Name, code)
		}
	}
	if os.Getenv("HM_TEST_CDT") == "1" {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		rows, err := s.Quotas(context.Background(), accounts, loc, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		usable := 0
		for _, q := range rows {
			value := "unknown"
			if q.OfficialBytes != nil {
				value = fmt.Sprint(*q.OfficialBytes)
				usable++
			}
			t.Logf("scope=%s official_bytes=%s quality=%s status=%s cutoff_known=%t estimate_known=%t", q.Scope, value, q.OfficialQuality, q.OfficialStatus, q.OfficialAsOf != nil, q.EstimatedBytes != nil)
			if q.OfficialAsOf != nil || q.EstimatedBytes != nil || q.EffectiveBytes != nil {
				t.Fatal("unverified source presented as aligned")
			}
		}
		if usable == 0 {
			t.Fatal("no usable provisional CDT observation")
		}
	}
}

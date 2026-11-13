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

func TestReleasePreviewRequiresSessionAndReadsOnlyCache(t *testing.T) {
	p := testPanel(t)
	cookie, csrf := setupPanel(t, p)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	box, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	scanner := &apiScanner{}
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: scanner, Interval: time.Minute}
	a, _, err := p.engine.CreateAccount(context.Background(), model.AccountCreate{Name: "release preview fixture", Site: "china", AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "release-preview-fixture-never-real", RegionIDs: []string{"cn-shanghai"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec(`INSERT INTO instance(id,account_id,cloud_id,kind,name,region,status,desired_state,updated_at)
 VALUES('target',?,'i-target','ecs','fixture','cn-shanghai','stopped','stopped',?)`, a.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.DB.Exec(`INSERT INTO resource_snapshot(account_id,kind,id,region,payload_json,present,updated_at)
 VALUES(?,'ecs','target','cn-shanghai','{}',1,?)`, a.ID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	path := "/instances/target/release-preview"
	if w := panelRequest(p, "GET", path, nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	var preview model.ReleasePreview
	w := panelRequest(p, "GET", path, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Status != "never_fetched" || preview.Impact != nil || preview.Token != nil || preview.ExecutionEnabled {
		t.Fatal(w.Code, w.Body)
	}
	no, yes := false, true
	data := model.ReleaseInventory{Instances: []model.ReleaseInstanceFacts{{CloudID: "i-target", Name: "provider exact name", Status: "Stopped", ChargeType: "PostPaid", DeletionProtection: &no, Locked: &no}}, Disks: []model.ReleaseDiskFacts{{CloudID: "d-target", Category: "cloud_essd", Type: "system", InstanceIDs: []string{"i-target"}, AttachmentsKnown: true, DeleteWithInstance: &yes, DeleteAutoSnapshot: &no}}}
	raw, _ := json.Marshal(data)
	if _, err := p.s.DB.Exec("INSERT INTO release_inventory VALUES(?,1,'cn-shanghai',?,?,NULL,?)", a.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), string(raw)); err != nil {
		t.Fatal(err)
	}
	w = panelRequest(p, "GET", path, nil, cookie, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || preview.Status != "eligible" || preview.Impact == nil || preview.Impact.Name != "provider exact name" || preview.Token == nil || preview.ExecutionEnabled {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", "/instances/missing/release-preview", nil, cookie, ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	if w = panelRequest(p, "POST", path, map[string]any{}, cookie, csrf); w.Code != 405 {
		t.Fatal(w.Code, w.Body)
	}
	if scanner.calls != 0 {
		t.Fatal("preview read called the cloud scanner", scanner.calls)
	}
}

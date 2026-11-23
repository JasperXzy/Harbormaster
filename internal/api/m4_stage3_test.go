package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/m4ops"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/notify"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

type fakeM4Cloud struct {
	mu          sync.Mutex
	allocSeq    int
	allocated   []string
	associated  []string
	unassoc     []string
	released    []string
	ensuredNets int
	createdInst []string
}

func (f *fakeM4Cloud) ECSCatalog(_ context.Context, _ aliyun.Credentials, accountID, region string) (model.ECSCatalog, error) {
	return model.ECSCatalog{
		AccountID:     accountID,
		RegionID:      region,
		Zones:         []model.ECSCatalogZone{{ID: region + "a", Name: "Zone A"}},
		Images:        []model.ECSCatalogImage{{ID: "debian_12_x64", Name: "Debian 12"}},
		InstanceTypes: []model.ECSCatalogInstanceType{{ID: "ecs.e-c1m1.large", CPU: 2, MemoryGiB: 2}},
		VPCs:          []model.ECSCatalogVPC{{ID: "vpc-existing", Name: "default", CidrBlock: "172.16.0.0/12"}},
		VSwitches:     []model.ECSCatalogVSwitch{{ID: "vsw-existing", VPCID: "vpc-existing", ZoneID: region + "a", Name: "vsw-a", CidrBlock: "172.16.0.0/24"}},
	}, nil
}

func (f *fakeM4Cloud) AllocateEIP(_ context.Context, _ aliyun.Credentials, _ string, _ int, _ string) (aliyun.AllocatedEIP, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocSeq++
	id := fmt.Sprintf("eip-alloc-%d", f.allocSeq)
	ip := fmt.Sprintf("198.51.100.%d", 10+f.allocSeq)
	f.allocated = append(f.allocated, id)
	return aliyun.AllocatedEIP{AllocationID: id, EIPAddress: ip}, nil
}

func (f *fakeM4Cloud) AddEIPToBandwidthPackage(context.Context, aliyun.Credentials, string, string, string) error {
	return nil
}

func (f *fakeM4Cloud) RemoveEIPFromBandwidthPackage(context.Context, aliyun.Credentials, string, string, string) error {
	return nil
}

func (f *fakeM4Cloud) AssociateEIP(_ context.Context, _ aliyun.Credentials, _, allocationID, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.associated = append(f.associated, allocationID+"->"+instanceID)
	return nil
}

func (f *fakeM4Cloud) UnassociateEIP(_ context.Context, _ aliyun.Credentials, _, allocationID, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unassoc = append(f.unassoc, allocationID+"->"+instanceID)
	return nil
}

func (f *fakeM4Cloud) ReleaseEIP(_ context.Context, _ aliyun.Credentials, _, allocationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, allocationID)
	return nil
}

func (f *fakeM4Cloud) EnsureNetwork(_ context.Context, _ aliyun.Credentials, _, _ string) (aliyun.CreatedNetwork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensuredNets++
	return aliyun.CreatedNetwork{VPCID: "vpc-auto-1", VSwitchID: "vsw-auto-1", SecurityGroupID: "sg-auto-1"}, nil
}

func (f *fakeM4Cloud) RunECSInstance(_ context.Context, _ aliyun.Credentials, in model.ECSCreateInput, vpcID, vswitchID, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("i-created-%d", len(f.createdInst)+1)
	f.createdInst = append(f.createdInst, id+"@"+vpcID+"/"+vswitchID+"/"+in.InstanceChargeType)
	return id, nil
}

type fakeBotTransport struct {
	mu      sync.Mutex
	updates []telegram.Update
	replies []string
	buttons [][][]telegram.InlineButton
}

func (b *fakeBotTransport) GetUpdates(context.Context, telegram.Destination, int64, int) ([]telegram.Update, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.updates
	b.updates = nil
	return out, nil
}

func (b *fakeBotTransport) ReplyToChat(_ context.Context, _ telegram.Destination, _ string, text string, btns [][]telegram.InlineButton) (telegram.Receipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.replies = append(b.replies, text)
	b.buttons = append(b.buttons, btns)
	return telegram.Receipt{MessageID: 1}, nil
}

func (b *fakeBotTransport) AnswerCallback(context.Context, telegram.Destination, string, string) error {
	return nil
}

func panelIdempotentRequest(h http.Handler, method, path string, body any, cookie *http.Cookie, csrf, idemKey string) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "https://panel.example/api/v1"+path, bytes.NewReader(raw))
	r.RemoteAddr = "203.0.113.10:12345"
	r.Header.Set("Origin", "https://panel.example")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if idemKey != "" {
		r.Header.Set("Idempotency-Key", idemKey)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestM4Stage3FailoverEIPRotateAndCreateECS(t *testing.T) {
	p := testPanel(t)
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	box, _ := security.NewBox(bytes.Repeat([]byte{5}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: &apiScanner{}, Interval: time.Minute}
	fakeCloud := &fakeM4Cloud{}
	p.m4Cloud = fakeCloud
	cookie, csrf := setupPanel(t, p)
	ctx := context.Background()

	// Create two accounts for cross-account failover
	accA, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{
		Name: "PrimaryAcc", Site: "china", RegionIDs: []string{"ap-southeast-1"},
		Credential: model.Credential{AccessKeyID: "AccAAccessKeyID123456", AccessKeySecret: "synthetic-secret-acc-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	accB, _, err := p.engine.CreateAccount(ctx, model.AccountCreate{
		Name: "StandbyAcc", Site: "china", RegionIDs: []string{"cn-hongkong"},
		Credential: model.Credential{AccessKeyID: "AccBAccessKeyID123456", AccessKeySecret: "synthetic-secret-acc-b"},
	})
	if err != nil {
		t.Fatal(err)
	}

	seedECS := func(id, accID, region, cloudID, name, status, desired string, publicIPs []map[string]string) {
		t.Helper()
		snapRaw, _ := json.Marshal(map[string]any{
			"id": id, "cloud_id": cloudID, "kind": "ecs", "name": name, "region_id": region,
			"status": status, "charge_type": "pay_as_you_go", "public_ips": publicIPs,
		})
		if _, err := p.s.DB.Exec(`INSERT INTO instance(id,account_id,kind,region,cloud_id,name,status,desired_state,updated_at) VALUES(?,?,'ecs',?,?,?,?,?,?)`,
			id, accID, region, cloudID, name, status, desired, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.s.DB.Exec(`INSERT INTO resource_snapshot(account_id,kind,id,region,present,payload_json,updated_at) VALUES(?,'ecs',?,?,1,?,?)`,
			accID, id, region, string(snapRaw), now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	// Primary has an unmanaged pre-existing EIP 203.0.113.50; Standby has no public IP yet
	seedECS("inst_primary", accA.ID, "ap-southeast-1", "i-primary", "sg-primary", "stopped", "quota_stopped",
		[]map[string]string{{"address": "203.0.113.50", "type": "eip"}})
	seedECS("inst_standby", accB.ID, "cn-hongkong", "i-standby", "hk-standby", "stopped", "stopped",
		[]map[string]string{})

	// 1. Cross-account Failover CRUD & Evaluation (F6.8)
	w := panelRequest(p, "POST", "/automation/failovers", map[string]any{
		"revision": 0, "name": "SG-to-HK-Failover", "enabled": true, "dry_run": false,
		"primary_instance_id": "inst_primary", "standby_instance_id": "inst_standby",
	}, cookie, csrf)
	if w.Code != 201 {
		t.Fatal("create failover rule failed", w.Code, w.Body.String())
	}
	var createdRule model.FailoverRule
	_ = json.Unmarshal(w.Body.Bytes(), &createdRule)

	policy := model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, Timezone: "Asia/Shanghai"}
	if err := p.s.EvaluateAutomation(ctx, policy, now); err != nil {
		t.Fatal(err)
	}
	powerOp, err := p.s.ClaimPowerJob(ctx, now)
	if err != nil || powerOp.InstanceID != "inst_standby" || powerOp.Action != "start" {
		t.Fatalf("expected cross-account standby start power job, got err=%v op=%+v", err, powerOp)
	}

	// 2. EIP Rotation safety gates & strict managed_eip protection (F7.2 + F8.4)
	// While disabled -> 503 eip_manage_disabled
	w = panelIdempotentRequest(p, "POST", "/instances/inst_standby/change-eip", map[string]any{"bandwidth_mbps": 100, "internet_charge_type": "PayByTraffic"}, cookie, csrf, "idem_eip_disabled_0001")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "eip_manage_disabled") {
		t.Fatal("expected 503 eip_manage_disabled", w.Code, w.Body.String())
	}

	p.eipManageEnabled = true
	// Pre-existing unmanaged EIP on inst_primary MUST be rejected with 409 unmanaged_eip_protected!
	w = panelIdempotentRequest(p, "POST", "/instances/inst_primary/change-eip", map[string]any{"bandwidth_mbps": 100, "internet_charge_type": "PayByTraffic"}, cookie, csrf, "idem_eip_unmanaged_001")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "unmanaged_eip_protected") {
		t.Fatal("expected 409 unmanaged_eip_protected", w.Code, w.Body.String())
	}

	// Rotate on inst_standby (no existing EIP) -> allocates eip-alloc-1 (198.51.100.11) and records managed_eip
	w = panelIdempotentRequest(p, "POST", "/instances/inst_standby/change-eip", map[string]any{"bandwidth_mbps": 100, "internet_charge_type": "PayByTraffic"}, cookie, csrf, "idem_eip_standby_rot1")
	if w.Code != 202 {
		t.Fatal("expected 202 for first EIP allocation", w.Code, w.Body.String())
	}
	runner := m4ops.Runner{Store: p.s, Box: box, Cloud: fakeCloud, EIPManageEnabled: true, InstanceCreateEnabled: true, Now: func() time.Time { return now }}
	if worked, err := runner.RunOne(ctx); !worked || err != nil {
		t.Fatal("m4ops first EIP rotate failed", worked, err)
	}

	// Update inst_standby snapshot with the newly bound managed EIP 198.51.100.11, then test Telegram /changeip 2-step flow!
	snapUpdated, _ := json.Marshal(map[string]any{
		"id": "inst_standby", "cloud_id": "i-standby", "kind": "ecs", "name": "hk-standby", "region_id": "cn-hongkong",
		"status": "running", "charge_type": "pay_as_you_go", "public_ips": []map[string]string{{"address": "198.51.100.11", "type": "eip"}},
	})
	if _, err := p.s.DB.Exec(`UPDATE resource_snapshot SET payload_json=? WHERE id='inst_standby'`, string(snapUpdated)); err != nil {
		t.Fatal(err)
	}

	// Configure Telegram channel + control for user 424242 and run /changeip hk-standby -> confirm callback!
	chans := notify.Channels{Store: p.s, Box: box}
	if _, err := chans.Replace(ctx, telegram.Destination{Token: "123456:SYNTHETIC_BOT_TOKEN_1234567890", ChatID: "-100999", ProxyMode: "none"}, 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.SaveTelegramControl(ctx, model.TelegramControlSettings{Revision: 0, Enabled: true, AllowedUserIDs: []string{"424242"}}, now); err != nil {
		t.Fatal(err)
	}
	bot := &fakeBotTransport{
		updates: []telegram.Update{{
			UpdateID: 1,
			Message:  &telegram.BotMessage{From: telegram.BotUser{ID: 424242}, Chat: telegram.BotChat{ID: -100999}, Text: "/changeip hk-standby"},
		}},
	}
	br := notify.BotRunner{Channels: notify.Channels{Store: p.s, Box: box}, Bot: bot, PowerEnabled: true, EIPManageEnabled: true, Timezone: "Asia/Shanghai", Now: func() time.Time { return now }}
	br.Step(ctx, 1)
	if len(bot.buttons) != 1 || len(bot.buttons[0]) == 0 || !strings.HasPrefix(bot.buttons[0][0][0].CallbackData, "act:") {
		t.Fatalf("expected 2-step confirmation inline button for /changeip, got %+v", bot.buttons)
	}
	cbData := bot.buttons[0][0][0].CallbackData
	bot.updates = []telegram.Update{{
		UpdateID:      2,
		CallbackQuery: &telegram.BotCallbackQuery{ID: "cb1", From: telegram.BotUser{ID: 424242}, Data: cbData, Message: &telegram.BotMessage{Chat: telegram.BotChat{ID: -100999}}},
	}}
	br.Step(ctx, 1)

	// Run m4ops worker for the second rotation: must unassociate & release eip-alloc-1 and associate eip-alloc-2!
	if worked, err := runner.RunOne(ctx); !worked || err != nil {
		t.Fatal("m4ops second EIP rotate failed", worked, err)
	}
	if len(fakeCloud.released) != 1 || fakeCloud.released[0] != "eip-alloc-1" {
		t.Fatalf("expected managed eip-alloc-1 to be released after rotation, got released=%v", fakeCloud.released)
	}

	// 3. ECS Catalog & Create ECS Instance (F7.3)
	w = panelRequest(p, "GET", "/cloud/ecs-catalog?account_id="+accA.ID+"&region_id=ap-southeast-1", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ecs.e-c1m1.large"`) {
		t.Fatal("ecs catalog failed", w.Code, w.Body.String())
	}

	// Disabled gate -> 503 instance_create_disabled
	createPayload := map[string]any{
		"account_id": accA.ID, "region_id": "ap-southeast-1", "zone_id": "ap-southeast-1a",
		"instance_name": "hm-spot-node", "instance_type": "ecs.e-c1m1.large", "image_id": "debian_12_x64",
		"instance_charge_type": "Spot", "internet_charge_type": "PayByTraffic",
		"bandwidth_out_mbps": 100, "system_disk_gib": 20, "auto_create_network": true, "confirm": true,
	}
	w = panelIdempotentRequest(p, "POST", "/instances/create", createPayload, cookie, csrf, "idem_ecs_create_dis_01")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "instance_create_disabled") {
		t.Fatal("expected 503 instance_create_disabled", w.Code, w.Body.String())
	}

	p.instanceCreateEnabled = true
	w = panelIdempotentRequest(p, "POST", "/instances/create", createPayload, cookie, csrf, "idem_ecs_create_ok_001")
	if w.Code != 202 {
		t.Fatal("expected 202 for create ECS", w.Code, w.Body.String())
	}
	if worked, err := runner.RunOne(ctx); !worked || err != nil {
		t.Fatal("m4ops create ECS failed", worked, err)
	}
	if fakeCloud.ensuredNets != 1 || len(fakeCloud.createdInst) != 1 || !strings.Contains(fakeCloud.createdInst[0], "vpc-auto-1/vsw-auto-1/Spot") {
		t.Fatalf("unexpected created instance trace: nets=%d created=%v", fakeCloud.ensuredNets, fakeCloud.createdInst)
	}

	// Clean delete failover rule
	w = panelRequest(p, "DELETE", fmt.Sprintf("/automation/failovers/%s?revision=%d", createdRule.ID, createdRule.Revision), nil, cookie, csrf)
	if w.Code != 204 {
		t.Fatal("delete failover rule failed", w.Code, w.Body.String())
	}
}

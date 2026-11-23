package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func setAlertAccount(t *testing.T, s *Store, status string, failures int) {
	t.Helper()
	a := model.Account{ID: "a", Name: "account", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}, QuotaMainland: 20 << 30, QuotaOverseas: 200 << 30, ResetDay: 1, Thresholds: []int{80, 90, 95}, SyncStatus: status, ConsecutiveFailures: failures}
	raw, _ := json.Marshal(a)
	if _, err := s.DB.Exec("UPDATE account_settings SET config_json=? WHERE account_id='a'", string(raw)); err != nil {
		t.Fatal(err)
	}
}
func alertHistory(t *testing.T, s *Store) []model.Notification {
	t.Helper()
	items, err := s.Notifications(context.Background(), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	return items
}
func evaluate(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	if err := s.EvaluateAlerts(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}
func TestAccountAlertLifecycleIsDurableAndUnknownIsNotRecovery(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	setAlertAccount(t, s, "failing", 2)
	evaluate(t, s, now)
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("fired before failure threshold")
	}
	setAlertAccount(t, s, "failing", 3)
	evaluate(t, s, now)
	for range 3 {
		evaluate(t, s, now.Add(time.Second))
	}
	history := alertHistory(t, s)
	if len(history) != 1 || history[0].Kind != "alert" || history[0].DeliveryStatus != "not_sent" {
		t.Fatal(history)
	}
	if history[0].Title != "🟠 账号 account 连不上阿里云" || !strings.HasPrefix(history[0].Body, "连续 3 次失败") {
		t.Fatal("unexpected copy", history[0].Title, history[0].Body)
	}
	// A fresh configuration with no new result must not invent a recovery.
	setAlertAccount(t, s, "never_synced", 0)
	evaluate(t, s, now.Add(2*time.Second))
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("unknown produced recovery")
	}
	setAlertAccount(t, s, "ok", 0)
	evaluate(t, s, now.Add(3*time.Second))
	evaluate(t, s, now.Add(4*time.Second))
	history = alertHistory(t, s)
	if len(history) != 2 || history[0].Kind != "recovery" || history[0].Title != "🟢 账号 account 恢复连接" || history[0].Body != "" {
		t.Fatal(history)
	}
	setAlertAccount(t, s, "failing", 3)
	evaluate(t, s, now.Add(5*time.Second))
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("new failure episode not recorded")
	}
	// Disabling monitoring closes the incident without a fake recovery.
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now.Add(6*time.Second), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(7*time.Second))
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("disabled account recovered without evidence")
	}
	var active int
	if err := s.DB.QueryRow("SELECT count(*) FROM alert_state WHERE active=1").Scan(&active); err != nil || active != 0 {
		t.Fatal(active, err)
	}
}
func addCDTResult(t *testing.T, s *Store, index, version int, failed bool, now time.Time) {
	t.Helper()
	job := fmt.Sprintf("cdt-test-%d", index)
	if _, err := s.DB.Exec(`INSERT INTO job(id,type,scope,account_ids_json,status,created_at) VALUES(?,'refresh',?,'[]','succeeded',?)`, job, job, timestamp(now)); err != nil {
		t.Fatal(err)
	}
	var code any
	if failed {
		code = "aliyun_unreachable"
	}
	if _, err := s.DB.Exec(`INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,details_json,error_code) VALUES('a',?,?,1,?,'[]',?)`, version, job, timestamp(now), code); err != nil {
		t.Fatal(err)
	}
}
func TestCDTFailuresUseObservationsAndCurrentCredentialVersion(t *testing.T) {
	s := agentStore(t)
	now := time.Now().UTC()
	setAlertAccount(t, s, "ok", 0)
	addCDTResult(t, s, 1, 1, true, now)
	addCDTResult(t, s, 2, 1, true, now)
	for range 4 {
		evaluate(t, s, now)
	}
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("evaluation polling counted as failures")
	}
	addCDTResult(t, s, 3, 1, true, now)
	evaluate(t, s, now)
	history := alertHistory(t, s)
	if len(history) != 1 || *history[0].RuleKey != "cdt_official_unavailable" {
		t.Fatal(history)
	}
	if _, err := s.DB.Exec("UPDATE account_settings SET credential_version=2"); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now)
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("credential change fabricated new data")
	}
	addCDTResult(t, s, 4, 2, false, now)
	evaluate(t, s, now)
	history = alertHistory(t, s)
	if len(history) != 2 || history[0].Kind != "recovery" {
		t.Fatal(history)
	}
	addCDTResult(t, s, 5, 1, true, now)
	evaluate(t, s, now)
	if len(alertHistory(t, s)) != 2 {
		t.Fatal("old credentials contaminated current source")
	}
}
func TestAgentOfflineNeedsFreshBaselineAndRunningManagedInstance(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	setAlertAccount(t, s, "ok", 0)
	code, _, err := s.AgentGrant(ctx, "ecs", now)
	if err != nil {
		t.Fatal(err)
	}
	reg := registration(code, "i-test")
	agent, err := s.RegisterAgent(ctx, reg, now)
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(2*time.Minute))
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("pending installation counted as loss of heartbeat")
	}
	token := agentproto.DeriveToken(code, reg.Nonce)
	report := agentproto.Report{Version: 1, AgentID: agent.AgentID, CounterEpoch: "epoch", Sequence: 1, UptimeMS: 1000, SampledAt: now}
	if _, err = s.AcceptReport(ctx, token, report, now); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(29*time.Second))
	if len(alertHistory(t, s)) != 0 {
		t.Fatal("early offline alert")
	}
	evaluate(t, s, now.Add(30*time.Second))
	evaluate(t, s, now.Add(40*time.Second))
	history := alertHistory(t, s)
	if len(history) != 1 || history[0].InstanceID == nil || *history[0].InstanceID != "ecs" || history[0].Severity != "critical" {
		t.Fatal(history)
	}
	if !strings.HasPrefix(history[0].Title, "🔴 ") || !strings.HasSuffix(history[0].Title, " 的采集程序掉线了") || !strings.HasPrefix(history[0].Body, "已经 30 秒没有收到上报。") || strings.Contains(history[0].Body, "估算") {
		t.Fatal("unexpected copy", history[0].Title, history[0].Body)
	}
	if _, err = s.DB.Exec("UPDATE instance SET status='stopped' WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(45*time.Second))
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("stopped VM produced fake agent recovery")
	}
	if _, err = s.DB.Exec("UPDATE instance SET status='running' WHERE id='ecs'"); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(50*time.Second))
	if len(alertHistory(t, s)) != 2 {
		t.Fatal("new applicable incident not recorded")
	}
	report.Sequence = 2
	report.UptimeMS = 61000
	report.SampledAt = now.Add(time.Minute)
	if _, err = s.AcceptReport(ctx, token, report, report.SampledAt); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, report.SampledAt)
	history = alertHistory(t, s)
	if len(history) != 3 || history[0].Kind != "recovery" {
		t.Fatal(history)
	}
	threshold := 120
	if _, err = s.UpdateAlertRule(ctx, "agent_offline", model.AlertRuleUpdate{Params: &model.AlertParams{OfflineAfterSeconds: &threshold}}, now); err != nil {
		t.Fatal(err)
	}
	views, err := s.AgentViews(ctx, report.SampledAt.Add(60*time.Second))
	if err != nil || views["ecs"].Agent.Status != "online" {
		t.Fatal("status disagreed with configured timeout", err)
	}
	evaluate(t, s, report.SampledAt.Add(60*time.Second))
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("alert disagreed with configured timeout")
	}
	if err = s.RevokeAgent(ctx, "ecs", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	evaluate(t, s, now.Add(5*time.Minute))
	if len(alertHistory(t, s)) != 3 {
		t.Fatal("revocation produced offline notification")
	}
}
func TestDisabledRulesCancelQueuedAlerts(t *testing.T) {
	s := agentStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	channelFixture(t, s, 0, now)
	setAlertAccount(t, s, "failing", 3)
	evaluate(t, s, now)
	history := alertHistory(t, s)
	if len(history) != 1 || history[0].DeliveryStatus != "pending" {
		t.Fatal(history)
	}
	off := false
	if _, err := s.UpdateAlertRule(ctx, "account_api_failure", model.AlertRuleUpdate{Enabled: &off}, now); err != nil {
		t.Fatal(err)
	}
	history = alertHistory(t, s)
	if len(history) != 1 || history[0].DeliveryStatus != "not_sent" || history[0].LastError.Code != "alert_rule_disabled" {
		t.Fatal(history)
	}
	evaluate(t, s, now)
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("disabled rule produced an event")
	}
}

func TestAlertRecordsSurviveRestartAndProduceDistinctUpdateEvents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO account VALUES('a','account','china','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO account_settings(account_id,config_json,enabled) VALUES('a','{}',1)"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	channelFixture(t, s, 0, now)
	setAlertAccount(t, s, "failing", 3)
	evaluate(t, s, now)
	events, head, err := s.NotificationChanges(ctx, 0)
	if err != nil || len(events) != 1 || !events[0].Created {
		t.Fatal(events, err)
	}
	s.Close()
	s, err = Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evaluate(t, s, now.Add(time.Second))
	if len(alertHistory(t, s)) != 1 {
		t.Fatal("restart duplicated open incident")
	}
	off := false
	if _, err = s.UpdateAlertRule(ctx, "account_api_failure", model.AlertRuleUpdate{Enabled: &off}, now); err != nil {
		t.Fatal(err)
	}
	updates, _, err := s.NotificationChanges(ctx, head)
	if err != nil || len(updates) != 1 || updates[0].Created || updates[0].Notification.DeliveryStatus != "not_sent" {
		t.Fatal(updates, err)
	}
}

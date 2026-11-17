package power

import (
	"context"
	"encoding/json"
	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
	"time"
)

func TestQuotaThroughMockCloudConfirmationAndNotification(t *testing.T) {
	e, _ := fixture(t, "ecs", "stop", "keep_charging")
	ctx := context.Background()
	s := e.Store
	manual, err := s.ClaimPowerJob(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FailPowerJob(ctx, manual.Job, "cancelled", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	end := now.Truncate(10 * time.Second)
	base := end.Add(-2 * time.Minute)
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	cycleEnd := cycleStart.AddDate(0, 1, 0)
	if _, err = s.DB.Exec("UPDATE account_settings SET config_json=json_set(config_json,'$.quota_mainland_bytes',20000,'$.quota_overseas_bytes',20000)"); err != nil {
		t.Fatal(err)
	}
	a, err := s.Account(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO agent(id,instance_id,token_hash,registered_at,last_received_at) VALUES('agent','instance',zeroblob(32),?,?)", base.Format("2006-01-02T15:04:05.000000000Z"), end.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	for n := int64(0); n <= 12; n++ {
		at := base.Add(time.Duration(n) * 10 * time.Second).Format("2006-01-02T15:04:05.000000000Z")
		if _, err = s.DB.Exec("INSERT INTO traffic_sample VALUES('agent','epoch',?,?,?,?,?,?)", n+1, at, at, n*10000+1000, n*100, n*1000); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.Exec("INSERT INTO traffic_sample_detail(agent_id,counter_epoch,sequence,rules_version,dropped_samples,agent_version,quality,report_interval_seconds) VALUES('agent','epoch',?,'rules',0,'test','ok',10)", n+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.DB.Exec("INSERT INTO agent_counter_state(agent_id,counter_epoch,sequence,uptime_ms,rx_bytes,tx_bytes,sampled_at,received_at,rules_version,quality,report_interval_seconds) VALUES('agent','epoch',13,121000,1200,12000,?,?,'rules','ok',10)", end.Format("2006-01-02T15:04:05.000000000Z"), end.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	sub, _ := json.Marshal(map[string]any{"id": "subject", "metering_rule": "outbound_only", "counts_toward_cdt": true, "cdt_scope": "mainland", "instance_ids": []string{"instance"}, "bandwidth_mbps": 8})
	if _, err = s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','subject','subject','cn-shanghai',?,1,?)", string(sub), base.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO metering_topology VALUES('a','cn-shanghai',?,'f',?,?,1,'verified')", a.CredentialVersion, base.Format("2006-01-02T15:04:05.000000000Z"), now.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,overseas_bytes,details_json,covered_until,period_start,period_end,source_quality,source) VALUES('a',?,?,1,?,1000,0,'[]',?,?,?,'verified','fixture')", a.CredentialVersion, manual.Job.ID, now.Add(-time.Second).Format("2006-01-02T15:04:05.000000000Z"), base.Format("2006-01-02T15:04:05.000000000Z"), cycleStart.Format("2006-01-02T15:04:05.000000000Z"), cycleEnd.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	in := model.ProtectionRuleInput{Name: "quota", Enabled: true, AccountID: "a", Scope: "mainland", ThresholdKind: "bytes", Threshold: 18000, Action: "stop_notify", Targets: []string{"instance"}, StopMode: "keep_charging", ReserveSeconds: 60, DryRun: false}
	if _, err = s.SaveProtectionRule(ctx, "", in, now); err != nil {
		t.Fatal(err)
	}
	policy := model.ProtectionPolicy{PowerEnabled: true, SelfKnown: true, Timezone: "UTC"}
	e.ProtectionPolicy = policy
	if err = s.EvaluateQuotaProtection(ctx, policy, now); err != nil {
		t.Fatal(err)
	}
	calls, reads := 0, 0
	e.Writer = writerFunc(func(_ context.Context, _ aliyun.Credentials, r aliyun.PowerRequest) (aliyun.PowerResult, error) {
		calls++
		if r.Action != "stop" {
			t.Fatal(r)
		}
		var n int
		if err := s.DB.QueryRow("SELECT count(*) FROM automatic_power_action").Scan(&n); err != nil || n != 1 {
			t.Fatal("dispatch budget not committed", n, err)
		}
		return aliyun.PowerResult{Outcome: "accepted"}, nil
	})
	e.Observer = observerFunc(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
		reads++
		status := "running"
		var mode *string
		if reads > 1 {
			status = "stopped"
			mode = ptr("keep_charging")
		}
		return model.PowerObservation{Status: status, StoppedMode: mode, ObservedAt: time.Now().UTC()}, nil
	})
	began := time.Now()
	ran, err := e.RunOne(ctx)
	if err != nil || !ran || calls != 1 || time.Since(began) > 30*time.Second {
		t.Fatal(ran, err, calls)
	}
	decisions, _, err := s.QuotaDecisions(ctx, "", 0, 10)
	if err != nil || len(decisions) != 1 || len(decisions[0].Targets) != 1 || decisions[0].Targets[0].JobID == nil {
		t.Fatal(decisions, err)
	}
	j, err := s.Job(ctx, *decisions[0].Targets[0].JobID)
	if err != nil || j.Status != "succeeded" {
		t.Fatal(j, err)
	}
	if err = s.EvaluateQuotaProtection(ctx, policy, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	notices, err := s.Notifications(ctx, 0, 200)
	if err != nil || len(notices) != 2 {
		t.Fatal(notices, err)
	}
	for _, n := range notices {
		if n.DeliveryStatus != "not_sent" || !n.Urgent {
			t.Fatal(n)
		}
	}
	var desired string
	s.DB.QueryRow("SELECT desired_state FROM instance WHERE id='instance'").Scan(&desired)
	if desired != "quota_stopped" {
		t.Fatal(desired)
	}
	t.Log("mock stop accepted and confirmed; dispatches=1; urgent outbox records=2; real cloud calls=0")
}

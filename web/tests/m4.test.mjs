import test from "node:test";
import assert from "node:assert/strict";
import { ALERT_RULE, errorText, fieldErrorText, JOB_TYPE, RAM_POLICY } from "../src/api/labels.ts";

test("M3 and M4 labels cover all new job types, alert rules, RAM policy tiers, and safety gate error codes", () => {
  // M4 Alert Rules
  assert.ok(ALERT_RULE.swas_traffic_usage.title.includes("轻量套餐"));
  assert.ok(ALERT_RULE.cpu_credit_low.title.includes("积分"));
  assert.ok(ALERT_RULE.system_event.title.includes("维护"));
  // M3 Network Alert Rules
  assert.ok(ALERT_RULE.network_latency.title);
  assert.ok(ALERT_RULE.network_loss.title);
  assert.ok(ALERT_RULE.network_unreachable.title);
  assert.ok(ALERT_RULE.network_route_change.title);

  // M4 Job Types
  assert.equal(JOB_TYPE.email_test, "发送邮件测试");
  assert.equal(JOB_TYPE.webhook_test, "发送 Webhook 测试");
  assert.equal(JOB_TYPE.instance_eip_rotate, "更换弹性公网 IP");
  assert.equal(JOB_TYPE.instance_create, "创建云服务器");

  // Safety Gate Error Codes
  assert.ok(errorText("eip_manage_disabled").includes("HM_EIP_MANAGE_ENABLED=false"));
  assert.ok(errorText("instance_create_disabled").includes("HM_INSTANCE_CREATE_ENABLED=false"));
  assert.ok(errorText("instance_release_disabled").includes("HM_INSTANCE_RELEASE_ENABLED=false"));
  assert.ok(errorText("unmanaged_eip_protected").includes("安全保护"));

  // Field Validation & RAM Policies
  assert.ok(fieldErrorText("must_differ_from_primary"));
  assert.ok(fieldErrorText("mismatch"));
  assert.ok(RAM_POLICY.readonly[0]);
  assert.ok(RAM_POLICY.instance_power[0]);
  assert.ok(RAM_POLICY.resource_management[0]);
});

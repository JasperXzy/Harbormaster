import test from "node:test";
import assert from "node:assert/strict";
import { computeOverageCost, parseTiers, tiersToText } from "../src/lib/pricing.ts";
import { ALERT_RULE, DECISION_OUTCOME, JOB_TRIGGER, STOP_MODE } from "../src/api/labels.ts";
import { GB } from "../src/lib/format.ts";

const pricing = {
  revision: 1,
  china: {
    currency: "CNY",
    mainland_tiers: [{ up_to_gib: null, unit_price: "0.80" }],
    overseas_tiers: [{ up_to_gib: 10, unit_price: "0.50" }, { up_to_gib: null, unit_price: "0.40" }],
  },
  international: {
    currency: "USD",
    mainland_tiers: [{ up_to_gib: null, unit_price: "0.12" }],
    overseas_tiers: [{ up_to_gib: null, unit_price: "0.08" }],
  },
  updated_at: null,
};

test("tiered pricing parser enforces strictly ascending caps and unbounded final tier", () => {
  const parsed = parseTiers("10240:0.50, 51200:0.45, *:0.40");
  assert.ok(parsed);
  assert.equal(tiersToText(parsed), "10240:0.50, 51200:0.45, *:0.40");
  assert.equal(parseTiers("10240:0.50"), null);
  assert.equal(parseTiers("*:0.50, 10240:0.40"), null);
  assert.equal(parseTiers("100:0.50, 50:0.40, *:0.30"), null);
});

test("overage cost preserves unknown nulls and computes progressive tier slices on 1024^3 GiB", () => {
  const unknown = computeOverageCost({ scope: "overseas", quota_bytes: 200 * GB, forecast: null }, "china", pricing, null);
  assert.equal(unknown.estimated_cost, null);
  assert.equal(unknown.current_overage_bytes, null);

  // 215 GiB used against 200 GiB quota -> 15 GiB overage: 10 * 0.50 + 5 * 0.40 = 7.00 CNY
  const over = computeOverageCost(
    { scope: "overseas", quota_bytes: 200 * GB, forecast: { cycle_end_bytes: 225 * GB } },
    "china",
    pricing,
    215 * GB,
  );
  assert.equal(over.currency, "CNY");
  assert.equal(over.estimated_cost, "7.00");
  assert.equal(over.forecast_cost, "11.00");
});

test("M2 labels cover all alert rules, automation triggers, stop modes, and guard outcomes", () => {
  assert.ok(ALERT_RULE.cdt_usage.title);
  assert.ok(ALERT_RULE.cdt_forecast_exceed.title);
  assert.ok(ALERT_RULE.cdt_estimation_deviation.title);
  assert.equal(STOP_MODE.stop_charging[0], "节省停机");
  assert.equal(JOB_TRIGGER.panel, "网页手动");
  assert.equal(JOB_TRIGGER.schedule, "自动策略 / 定时");
  assert.equal(JOB_TRIGGER.telegram, "Telegram");
  assert.ok(DECISION_OUTCOME.source_unqualified);
});

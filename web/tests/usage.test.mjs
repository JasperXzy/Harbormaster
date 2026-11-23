import test from "node:test";
import assert from "node:assert/strict";
import { quotaPool, cumulativeBytes } from "../src/lib/usage.ts";
import { gb } from "../src/lib/format.ts";

const accounts = [{ id: "a", enabled: true, quota_overseas_bytes: 200, quota_mainland_bytes: 20 }, { id: "b", enabled: true, quota_overseas_bytes: 200, quota_mainland_bytes: 20 }];
const row = (id, value) => ({ account_id: id, scope: "overseas", quota_bytes: 200, effective_bytes: value });

test("an unknown or missing account usage never becomes free capacity", () => {
  assert.equal(quotaPool([row("a", 100), row("b", null)], accounts, "overseas").left, null);
  assert.equal(quotaPool([row("a", 100)], accounts, "overseas").left, null);
  assert.equal(gb(null), "待确认");
});
test("confirmed zero is distinct from unknown; disabled accounts are excluded", () => {
  assert.equal(quotaPool([row("a", 0), row("b", 50)], accounts, "overseas").left, 350);
  assert.equal(quotaPool([row("a", 100)], [accounts[0], { ...accounts[1], enabled: false }], "overseas").left, 100);
});
test("a missing day breaks the cumulative series from that day onward", () => {
  const days = [{ mainland_bytes: 1, overseas_bytes: 10 }, { mainland_bytes: 2, overseas_bytes: null }, { mainland_bytes: 3, overseas_bytes: 30 }];
  assert.deepEqual(cumulativeBytes(days, "overseas"), [10, null, null]);
  assert.deepEqual(cumulativeBytes(days, "mainland"), [1, 3, 6]);
});

test("ready unverified estimates are displayed without changing protected fields", async () => {
  const {quotaView} = await import("../src/lib/usage.ts");
  const now=Date.now();
  const q={...row("a",null),estimated_bytes:null,estimation:{mode:"unverified",status:"ready",estimated_bytes:120,increment_bytes:20,worst_case_bytes:0,calculated_at:new Date(now).toISOString(),qualified_for_execution:false,known_increment_bytes:20,lag_window_seconds:3600,reasons:[]}};
  const v=quotaView(q,now);assert.equal(v.value,120);assert.equal(v.usable,true);assert.equal(v.unverified,true);assert.equal(q.estimated_bytes,null);
  assert.equal(quotaPool([q],[accounts[0]],"overseas").left,80);
  assert.equal(quotaView(q,now+31000).usable,false);
});
test("partial estimates cannot turn a known subtotal into complete usage", async () => {
  const {quotaView} = await import("../src/lib/usage.ts");
  const q={...row("a",null),estimation:{mode:"unverified",status:"incomplete",estimated_bytes:null,known_increment_bytes:10,calculated_at:new Date().toISOString(),qualified_for_execution:false,reasons:["coverage_gap"]}};
  assert.equal(quotaView(q).value,null);assert.equal(quotaView(q).knownIncrement,10);
  assert.equal(quotaPool([q],[accounts[0]],"overseas").left,null);
});

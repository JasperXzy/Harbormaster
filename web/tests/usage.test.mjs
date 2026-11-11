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

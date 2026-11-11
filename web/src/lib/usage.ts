import type { Account, DailyTraffic, QuotaScope, QuotaStatus } from "../api/types";

// Display aggregation only. Metering, reconciliation and protection belong to Go.
export function quotaPool(quotas: QuotaStatus[], accounts: Account[], scope: QuotaScope) {
  const enabled = accounts.filter((a) => a.enabled);
  const rows = enabled.map((a) => quotas.find((q) => q.account_id === a.id && q.scope === scope));
  const quota = enabled.reduce((sum, a) => sum + (scope === "mainland" ? a.quota_mainland_bytes : a.quota_overseas_bytes), 0);
  if (rows.some((q) => !q || q.effective_bytes === null)) return { quota, left: null, over: null, best: undefined };
  const known = rows as (QuotaStatus & { effective_bytes: number })[];
  const used = known.reduce((sum, q) => sum + q.effective_bytes, 0);
  const best = [...known].sort((a, b) => (b.quota_bytes - b.effective_bytes) - (a.quota_bytes - a.effective_bytes))[0];
  return { quota, left: Math.max(0, quota - used), over: Math.max(0, used - quota), best: enabled.find((a) => a.id === best?.account_id)?.name };
}

export function cumulativeBytes(days: DailyTraffic["days"], scope: QuotaScope): (number | null)[] {
  let total: number | null = 0;
  return days.map((d) => {
    const value = scope === "mainland" ? d.mainland_bytes : d.overseas_bytes;
    total = total === null || value === null ? null : total + value;
    return total;
  });
}

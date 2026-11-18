import type { Account, DailyTraffic, QuotaScope, QuotaStatus } from "../api/types";

// Display selection only; Go remains responsible for all metering and decisions.
export function quotaView(q: QuotaStatus, now = Date.now()) {
  const e = q.estimation;
  if (e) {
    const old = e.status === "stale" || !Number.isFinite(Date.parse(e.calculated_at)) || now - Date.parse(e.calculated_at) > 30_000;
    const available = (e.status === "ready" || e.status === "stale") && e.estimated_bytes !== null;
    return { value: available ? e.estimated_bytes : null, usable: available && !old,
      increment: available ? e.increment_bytes : null, worstCase: available ? e.worst_case_bytes : null,
      unverified: e.mode === "unverified" || !e.qualified_for_execution, assumedWindow: e.mode === "unverified", stale: old,
      updatedAt: e.calculated_at, status: old ? "stale" : e.status,
      knownIncrement: e.known_increment_bytes, lagSeconds: e.lag_window_seconds, reasons: e.reasons };
  }
  const value = typeof q.effective_bytes === "number" ? q.effective_bytes : q.estimated_bytes ?? null;
  return { value, usable: value !== null, increment: q.realtime_increment_bytes ?? null,
    worstCase: q.worst_case_bytes ?? null, unverified: false, assumedWindow:false, stale: false,
    updatedAt: q.updated_at, status: value === null ? "unavailable" : "ready",
    knownIncrement: q.realtime_increment_bytes ?? 0, lagSeconds: 0, reasons: [] as string[] };
}
export function quotaState(q: QuotaStatus) {
  const v = quotaView(q);
  return v.stale ? "估算已过期" : v.usable ? v.unverified ? "未核实估算" : "实时估算" : v.status === "incomplete" ? "覆盖不完整" : "暂无估算";
}
const ESTIMATION_REASON: Record<string, string> = {
	account_data_incomplete: "部分账号的计量数据不完整或已过期",
  official_basis_missing: "还没有可用的官方累计基准",
  topology_unknown: "公网出口信息尚未采集完整",
  topology_history_incomplete: "还缺少这一时段的出口成员历史",
  unattributed_subject: "有计费对象尚无可归属的采集数据",
  traffic_attribution_ambiguous: "同一实例对应多个出口，暂时无法分摊",
  agent_data_unavailable: "部分实例没有有效的采集数据",
  coverage_gap: "采集区间存在缺口",
  public_bandwidth_unknown: "离线推算所需的公网带宽尚不明确",
  samples_not_past_basis: "正在等待基准之后的采集数据",
  metering_rule_unknown: "部分出口的计量规则尚不明确",
  scope_unknown: "部分出口的国内／海外范围尚不明确",
};
export function quotaMissingReasons(q: QuotaStatus) {
  const labels = quotaView(q).reasons.map((r) => ESTIMATION_REASON[r]).filter(Boolean);
  return [...new Set(labels)].join("；") || "正在等待完整的计量数据";
}

export function quotaPool(quotas: QuotaStatus[], accounts: Account[], scope: QuotaScope) {
  const enabled = accounts.filter((a) => a.enabled);
  const rows = enabled.map((a) => quotas.find((q) => q.account_id === a.id && q.scope === scope));
  const quota = enabled.reduce((sum, a) => sum + (scope === "mainland" ? a.quota_mainland_bytes : a.quota_overseas_bytes), 0);
  const unverified = rows.some((q) => q && quotaView(q).unverified);
  if (rows.some((q) => !q || !quotaView(q).usable)) return { quota, left: null, over: null, best: undefined, unverified };
  const known = rows.map((q) => ({ q: q!, used: quotaView(q!).value! }));
  const used = known.reduce((sum, r) => sum + r.used, 0);
  const best = [...known].sort((a, b) => (b.q.quota_bytes - b.used) - (a.q.quota_bytes - a.used))[0];
  return { quota, left: Math.max(0, quota - used), over: Math.max(0, used - quota), best: enabled.find((a) => a.id === best?.q.account_id)?.name, unverified };
}

export function cumulativeBytes(days: DailyTraffic["days"], scope: QuotaScope): (number | null)[] {
  let total: number | null = 0;
  return days.map((d) => {
    const value = scope === "mainland" ? d.mainland_bytes : d.overseas_bytes;
    total = total === null || value === null ? null : total + value;
    return total;
  });
}

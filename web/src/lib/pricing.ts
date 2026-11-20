// 超额流量阶梯单价的解析、格式化与费用计算（1 GB = 1024^3 字节）。
import type { AccountSite, PriceTier, QuotaOverageCost, QuotaStatus, TrafficPricing } from "../api/types.ts";
import { GB } from "./format.ts";

export function tiersToText(tiers: PriceTier[]): string {
  return tiers.map((t) => (t.up_to_gib === null ? `*:${t.unit_price}` : `${t.up_to_gib}:${t.unit_price}`)).join(", ");
}

export function parseTiers(raw: string): PriceTier[] | null {
  const parts = raw.split(/[,，]+/).map((s) => s.trim()).filter(Boolean);
  if (!parts.length) return null;
  const out: PriceTier[] = [];
  let prev = 0;
  for (let i = 0; i < parts.length; i++) {
    const [cap, price] = parts[i].split(":").map((s) => s?.trim());
    if (!cap || !price || !/^\d+(\.\d{1,4})?$/.test(price)) return null;
    if (cap === "*") {
      if (i !== parts.length - 1) return null;
      out.push({ up_to_gib: null, unit_price: price });
    } else {
      const n = Number(cap);
      if (!(n > prev)) return null;
      prev = n;
      out.push({ up_to_gib: n, unit_price: price });
    }
  }
  if (out.at(-1)?.up_to_gib !== null) return null;
  return out;
}

export function computeOverageCost(q: Pick<QuotaStatus, "scope" | "quota_bytes" | "forecast">, site: AccountSite, pricing: TrafficPricing, effectiveBytes: number | null, unverified = false): QuotaOverageCost {
  const profile = site === "international" ? pricing.international : pricing.china;
  const tiers = q.scope === "mainland" ? profile.mainland_tiers : profile.overseas_tiers;
  const calc = (used: number | null) => {
    if (used === null) return { over: null as number | null, cost: null as string | null };
    const over = Math.max(0, used - q.quota_bytes);
    if (over === 0) return { over: 0, cost: "0.00" };
    let rem = over / GB;
    let prev = 0;
    let total = 0;
    for (const t of tiers) {
      const cap = t.up_to_gib === null ? Infinity : Math.max(0, t.up_to_gib - prev);
      const take = Math.min(rem, cap);
      total += take * Number(t.unit_price || 0);
      rem -= take;
      if (t.up_to_gib !== null) prev = t.up_to_gib;
      if (rem <= 0) break;
    }
    return { over, cost: total.toFixed(2) };
  };
  const cur = calc(effectiveBytes);
  const fc = calc(q.forecast?.cycle_end_bytes ?? null);
  return {
    currency: profile.currency,
    current_overage_bytes: cur.over,
    estimated_cost: cur.cost,
    forecast_overage_bytes: fc.over,
    forecast_cost: fc.cost,
    unverified,
  };
}

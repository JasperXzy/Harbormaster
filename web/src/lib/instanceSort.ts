// 实例与账号排序工具：在总览页与实例页共用，自动持久化到 localStorage。
import { useEffect, useState } from "react";
import { regionName } from "../api/labels";
import type { Account, Instance, QuotaStatus } from "../api/types";
import { quotaView } from "./usage";

export type InstanceSortKey = "name" | "status" | "rate" | "bandwidth" | "account" | "region" | "expires";
export type SortDir = "asc" | "desc";
export interface InstanceSortState {
  key: InstanceSortKey;
  dir: SortDir;
}

const INSTANCE_SORT_KEY = "hm.instances.sort";
const INSTANCE_SORT_EVENT = "hm:instances-sort-change";

export const INSTANCE_SORT_OPTIONS: [InstanceSortKey, string, SortDir][] = [
  ["name", "名称", "asc"],
  ["rate", "实时速率", "desc"],
  ["status", "运行状态", "asc"],
  ["account", "所属账号", "asc"],
  ["region", "地域", "asc"],
  ["bandwidth", "公网带宽", "desc"],
  ["expires", "到期时间", "asc"],
];

const STATUS_RANK: Record<Instance["status"], number> = {
  running: 0,
  starting: 1,
  pending: 2,
  stopping: 3,
  stopped: 4,
  unknown: 5,
};

export const naturalCmp = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" }).compare;

export function totalRateBps(i: Instance): number {
  if (i.realtime) return i.realtime.tx_bytes_per_second + i.realtime.rx_bytes_per_second;
  const stream = i.cloud_network?.streams?.[0];
  if (stream && (stream.egress.value != null || stream.ingress.value != null)) {
    return ((stream.egress.value ?? 0) + (stream.ingress.value ?? 0)) / 8;
  }
  return -1;
}

export function loadInstanceSort(): InstanceSortState {
  try {
    const raw = localStorage.getItem(INSTANCE_SORT_KEY);
    if (raw) {
      const [k, d] = raw.split(":");
      if (INSTANCE_SORT_OPTIONS.some(([key]) => key === k) && (d === "asc" || d === "desc")) {
        return { key: k as InstanceSortKey, dir: d };
      }
    }
  } catch {
    // ignore storage errors
  }
  return { key: "name", dir: "asc" };
}

export function useInstanceSort() {
  const [sort, setSort] = useState<InstanceSortState>(loadInstanceSort);

  useEffect(() => {
    const onSync = () => setSort(loadInstanceSort());
    window.addEventListener(INSTANCE_SORT_EVENT, onSync);
    window.addEventListener("storage", onSync);
    return () => {
      window.removeEventListener(INSTANCE_SORT_EVENT, onSync);
      window.removeEventListener("storage", onSync);
    };
  }, []);

  const updateSort = (key: InstanceSortKey, dir?: SortDir) => {
    setSort((prev) => {
      const defaultDir = INSTANCE_SORT_OPTIONS.find(([k]) => k === key)?.[2] ?? "asc";
      const next: InstanceSortState = dir
        ? { key, dir }
        : prev.key === key
          ? { key, dir: prev.dir === "asc" ? "desc" : "asc" }
          : { key, dir: defaultDir };
      try {
        localStorage.setItem(INSTANCE_SORT_KEY, `${next.key}:${next.dir}`);
      } catch {
        // ignore storage errors
      }
      window.dispatchEvent(new Event(INSTANCE_SORT_EVENT));
      return next;
    });
  };

  return { sort, updateSort };
}

export function sortInstances(list: Instance[], sort: InstanceSortState, accName: (id: string) => string): Instance[] {
  const mul = sort.dir === "asc" ? 1 : -1;
  return [...list].sort((a, b) => {
    let cmp = 0;
    switch (sort.key) {
      case "name":
        cmp = naturalCmp(a.name, b.name);
        break;
      case "status":
        cmp = STATUS_RANK[a.status] - STATUS_RANK[b.status];
        break;
      case "rate":
        cmp = totalRateBps(a) - totalRateBps(b);
        break;
      case "bandwidth":
        cmp = (a.bandwidth_mbps ?? -1) - (b.bandwidth_mbps ?? -1);
        break;
      case "account":
        cmp = naturalCmp(accName(a.account_id), accName(b.account_id));
        break;
      case "region":
        cmp = naturalCmp(regionName(a.region_id), regionName(b.region_id));
        break;
      case "expires": {
        const ea = a.expires_at ? Date.parse(a.expires_at) : null;
        const eb = b.expires_at ? Date.parse(b.expires_at) : null;
        if (ea === null && eb === null) cmp = 0;
        else if (ea === null) return 1;
        else if (eb === null) return -1;
        else cmp = ea - eb;
        break;
      }
    }
    if (cmp !== 0) return cmp * mul;
    return naturalCmp(a.name, b.name);
  });
}

/* ---------- 总览账号卡片排序 ---------- */
export type AccountSortKey = "usage_desc" | "name_asc" | "name_desc" | "remaining_asc";
const ACCOUNT_SORT_KEY = "hm.overview.account_sort";

export const ACCOUNT_SORT_OPTIONS: [AccountSortKey, string][] = [
  ["usage_desc", "按已用占比从高到低"],
  ["remaining_asc", "按剩余额度从少到多"],
  ["name_asc", "按账号名称 A–Z"],
  ["name_desc", "按账号名称 Z–A"],
];

function loadAccountSort(): AccountSortKey {
  try {
    const raw = localStorage.getItem(ACCOUNT_SORT_KEY);
    if (ACCOUNT_SORT_OPTIONS.some(([k]) => k === raw)) return raw as AccountSortKey;
  } catch {
    // ignore storage errors
  }
  return "usage_desc";
}

export function useAccountSort() {
  const [sort, setSort] = useState<AccountSortKey>(loadAccountSort);
  const updateSort = (next: AccountSortKey) => {
    setSort(next);
    try {
      localStorage.setItem(ACCOUNT_SORT_KEY, next);
    } catch {
      // ignore storage errors
    }
  };
  return { sort, updateSort };
}

function accountRatio(a: Account, quotas: QuotaStatus[]): number {
  let maxRatio = -1;
  for (const q of quotas) {
    if (q.account_id !== a.id || q.quota_bytes <= 0) continue;
    const v = quotaView(q);
    if (v.value !== null) maxRatio = Math.max(maxRatio, v.value / q.quota_bytes);
  }
  return maxRatio;
}

function accountMinRemaining(a: Account, quotas: QuotaStatus[]): number {
  let minRem = Number.POSITIVE_INFINITY;
  for (const q of quotas) {
    if (q.account_id !== a.id || q.quota_bytes <= 0) continue;
    const v = quotaView(q);
    if (v.value !== null) minRem = Math.min(minRem, q.quota_bytes - v.value);
  }
  return minRem;
}

export function sortAccounts(accounts: Account[], quotas: QuotaStatus[], key: AccountSortKey): Account[] {
  return [...accounts].sort((a, b) => {
    if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
    let cmp = 0;
    switch (key) {
      case "usage_desc":
        cmp = accountRatio(b, quotas) - accountRatio(a, quotas);
        break;
      case "remaining_asc":
        cmp = accountMinRemaining(a, quotas) - accountMinRemaining(b, quotas);
        break;
      case "name_asc":
        cmp = naturalCmp(a.name, b.name);
        break;
      case "name_desc":
        cmp = naturalCmp(b.name, a.name);
        break;
    }
    return cmp !== 0 ? cmp : naturalCmp(a.name, b.name);
  });
}

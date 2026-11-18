// 模拟数据：按 docs/api/openapi.yaml 的结构生成，全部是示例数值，不是真实用量。
// 时间都相对"现在"生成，所以任何一天打开都像是在正常运行。
import type * as T from "../api/types";
import { GB } from "../lib/format";

export type Scene = "normal" | "slow" | "empty" | "error" | "stale" | "agent_offline" | "cutoff_unknown" | "setup" | "logged_out";

export const SCENES: [Scene, string, string][] = [
  ["normal", "正常运行", "两个账号、六台实例，其中一台未受实时保护"],
  ["cutoff_unknown", "截止时间未知", "一个账号估算可用，另一个覆盖不全；合计保留缺口"],
  ["slow", "加载很慢", "每个请求等 4 秒，用来看加载中的样子"],
  ["empty", "空数据", "刚装好，还没有添加账号"],
  ["error", "接口出错", "数据接口全部返回错误"],
  ["stale", "数据过期", "官方数据取不到、一个账号连接失败、事件流断开"],
  ["agent_offline", "采集程序掉线", "hkg-pro 掉线，按最坏情况推算"],
  ["setup", "首次安装", "还没有设置管理员密码"],
  ["logged_out", "未登录", "显示登录页"],
];

const TZ_OFFSET_MS = 8 * 3600_000; // 模拟数据固定按 Asia/Shanghai
const iso = (ms: number) => new Date(ms).toISOString();
const MBPS = 1e6 / 8; // 1 Mbps = 125000 字节每秒

// 固定种子的随机数，保证每次打开数值一致
function rng(seed: number) {
  let s = seed;
  return () => (s = (s * 9301 + 49297) % 233280) / 233280;
}

export interface Db {
  scene: Scene;
  initialized: boolean;
  settings: T.Settings;
  accounts: T.Account[];
  instances: T.Instance[];
  instancesUpdatedAt: string | null;
  quotas: T.QuotaStatus[];
  daily: Record<string, T.DailyTraffic>;
  subjects: T.BillingSubject[];
  packages: T.BandwidthPackage[];
  hourly: Record<string, T.BandwidthPackageHourly>;
  telegram: T.TelegramChannel;
  rules: T.AlertRule[];
  notifications: T.Notification[];
  jobs: Map<string, T.Job>;
}

const PERMS: T.PermissionKey[] = ["ecs_read", "swas_read", "cdt_read", "cms_read", "instance_power", "vpc_read", "eip_manage", "bss_read"];

export const noAgent = (): T.Agent => ({ status: "not_installed", version: null, registered_at: null, last_report_at: null, offline_since: null, worst_case: null });

export function buildDb(scene: Scene, now = Date.now()): Db {
  const local = new Date(now + TZ_OFFSET_MS);
  const year = local.getUTCFullYear(), month = local.getUTCMonth(), dom = local.getUTCDate();
  const cycleStart = Date.UTC(year, month, 1) - TZ_OFFSET_MS;
  const cycleEnd = Date.UTC(year, month + 1, 1) - TZ_OFFSET_MS;
  const daysInMonth = Math.round((cycleEnd - cycleStart) / 86400_000);
  const todayStart = Date.UTC(year, month, dom) - TZ_OFFSET_MS;
  const stale = scene === "stale";
  const offline = scene === "agent_offline";
  const officialAsOf = stale ? now - 3.4 * 3600_000 : Math.floor(now / 3600_000) * 3600_000 - 3600_000;
  const realtimeAt = stale ? now - 12 * 60_000 : now - 4000;
  const scanAt = stale ? now - 2.2 * 3600_000 : now - 3 * 60_000;
  const dateStr = (d: number) => `${year}-${String(month + 1).padStart(2, "0")}-${String(d).padStart(2, "0")}`;

  const settings: T.Settings = {
    business_timezone: "Asia/Shanghai",
 retention: {latency_probe_days:30},
    collection: { cdt_poll_interval_seconds: 300, agent_report_interval_seconds: 10, agent_offline_after_seconds: 30, resource_scan_interval_seconds: 600 },
  };
  const rules: T.AlertRule[] = [
    { key: "cdt_usage", enabled: true, urgent: true, params: { thresholds_percent: [80, 90, 95] } },
    { key: "cdt_forecast_exceed", enabled: true, urgent: false, params: {} },
    { key: "agent_offline", enabled: true, urgent: true, params: { offline_after_seconds: 30 } },
    { key: "account_api_failure", enabled: true, urgent: false, params: { consecutive_failures: 3 } },
    { key: "cdt_official_unavailable", enabled: true, urgent: false, params: { consecutive_failures: 3 } },
    { key: "instance_expiring", enabled: true, urgent: false, params: { before_days: 7 } },
    { key: "spec_bandwidth_insufficient", enabled: true, urgent: false, params: {} },
    { key: "instance_public_ip_changed", enabled: true, urgent: false, params: {} },
  ];
  const base: Db = {
    scene, initialized: scene !== "setup", settings, accounts: [], instances: [], instancesUpdatedAt: null, quotas: [], daily: {}, subjects: [], packages: [], hourly: {},
    telegram: { bot_token_set: false, chat_id: "", proxy_mode: "none", proxy_url: "", status: "not_configured", last_error: null, last_success_at: null },
    rules, notifications: [], jobs: new Map(),
  };
  if (scene === "empty" || scene === "setup") return base;

  /* ---------- 账号 ---------- */
  const perm = (over: Partial<Record<T.PermissionKey, T.PermissionResult>> = {}): T.PermissionCheck => ({
    checked_at: iso(now - 26 * 3600_000),
    items: PERMS.map((key) => ({ key, result: over[key] ?? "passed" })),
  });
  const acct = (id: string, name: string, masked: string, regions: string[], n: number, p: T.PermissionCheck): T.Account => ({
    id, name, site: "china", access_key_id_masked: masked, region_ids: regions, enabled: true,
    quota_mainland_bytes: 20 * GB, quota_overseas_bytes: 200 * GB, reset_day: 1, alert_thresholds_percent: [80, 90, 95],
    sync_status: "ok", last_sync_at: iso(scanAt), last_error: null, consecutive_failures: 0, permission_check: p, instance_count: n, updated_at: iso(scanAt),
  });
  const a1 = acct("acc_jasper", "jasperxzy", "LTAI5t…pgu", ["cn-shanghai", "cn-hongkong", "ap-southeast-1"], 4, perm());
  const a2 = acct("acc_winne", "winnemjw", "LTAI5t…JG7", ["cn-shanghai", "cn-hangzhou", "cn-hongkong", "ap-southeast-1"], 2, perm({ vpc_read: "not_verified", eip_manage: "missing" }));
  if (stale) {
    a2.sync_status = "failing";
    a2.consecutive_failures = 14;
    a2.last_sync_at = iso(now - 2.2 * 3600_000);
    a2.last_error = { code: "access_key_invalid", message: "InvalidAccessKeyId.NotFound" };
  }

  /* ---------- 实例 ---------- */
  const online = (ver: string, regDays: number): T.Agent => ({ status: "online", version: ver, registered_at: iso(now - regDays * 86400_000), last_report_at: iso(realtimeAt), offline_since: null, worst_case: null });
  const rt = (rx: number, tx: number): T.InstanceRealtime => ({ rx_bytes_per_second: Math.round(rx * MBPS), tx_bytes_per_second: Math.round(tx * MBPS), sampled_at: iso(realtimeAt) });
  const offlineSince = now - 4 * 60_000 - 20_000;
  const worstRate = Math.round(182 * MBPS);
  const hkgAgent: T.Agent = offline
    ? { status: "offline", version: "0.1.0", registered_at: iso(now - 9 * 86400_000), last_report_at: iso(offlineSince), offline_since: iso(offlineSince), worst_case: { basis: "recent_peak", rate_bytes_per_second: worstRate, accumulated_bytes: Math.round((worstRate * (now - offlineSince)) / 1000) } }
    : online("0.1.0", 9);

  const inst = (o: Partial<T.Instance> & Pick<T.Instance, "id" | "name" | "account_id" | "kind" | "region_id" | "spec">): T.Instance => ({
    cloud_instance_id: "i-" + o.id.replace("ins_", "") + "0example", zone_id: o.region_id + "-a", status: "running", public_ips: [], charge_type: "pay_as_you_go", expires_at: null,
    billing_subject_id: null, billing_subject_kind: null, metering_rule: null, bandwidth_mbps: null, bandwidth_package_name: null, counts_toward_cdt: false, cdt_scope: null,
    agent: noAgent(), realtime_protection: "not_applicable", realtime: null, updated_at: iso(scanAt), ...o,
  });
  const instances: T.Instance[] = [
    inst({ id: "ins_sin", name: "sin-pro", account_id: a1.id, kind: "ecs", region_id: "ap-southeast-1", spec: "ecs.e-c4m1.large", public_ips: [{ address: "8.219.235.129", type: "eip" }],
      billing_subject_id: "sub_sgshare", billing_subject_kind: "cbwp_dominant_traffic", metering_rule: "max_in_out_per_period", bandwidth_mbps: 2000, bandwidth_package_name: "sg-share",
      counts_toward_cdt: true, cdt_scope: "overseas", agent: online("0.1.0", 9), realtime_protection: "protected", realtime: stale ? null : rt(36.2, 34.8) }),
    inst({ id: "ins_hkg", name: "hkg-pro", account_id: a1.id, kind: "ecs", region_id: "cn-hongkong", spec: "ecs.e-c4m1.large", public_ips: [{ address: "47.243.157.55", type: "eip" }],
      billing_subject_id: "sub_hkg", billing_subject_kind: "cbwp_dominant_traffic", metering_rule: "max_in_out_per_period", bandwidth_mbps: 2000, bandwidth_package_name: "hk-share",
      counts_toward_cdt: true, cdt_scope: "overseas", agent: hkgAgent, realtime_protection: offline ? "worst_case" : "protected", realtime: offline || stale ? null : rt(27.4, 26.1) }),
    inst({ id: "ins_spot", name: "demo-spot", account_id: a1.id, kind: "ecs", region_id: "cn-hongkong", spec: "ecs.t6-c1m1.large", charge_type: "spot", public_ips: [{ address: "47.242.18.66", type: "eip" }],
      billing_subject_id: "sub_spot", billing_subject_kind: "eip_pay_by_traffic", metering_rule: "outbound_only", bandwidth_mbps: 100,
      counts_toward_cdt: true, cdt_scope: "overseas", realtime_protection: "unprotected" }),
    inst({ id: "ins_web", name: "web-sh", account_id: a1.id, kind: "ecs", region_id: "cn-shanghai", spec: "ecs.e-c1m1.large", charge_type: "subscription", expires_at: iso(Date.UTC(year + 1, 2, 2) - TZ_OFFSET_MS),
      public_ips: [{ address: "47.116.26.14", type: "instance_public_ip" }], billing_subject_id: "sub_web", billing_subject_kind: "ecs_fixed_bandwidth", metering_rule: "not_metered", bandwidth_mbps: 5 }),
    inst({ id: "ins_sha", name: "sha-pro", account_id: a2.id, kind: "swas", region_id: "cn-shanghai", spec: "2 核 2 GB · 40 GB 硬盘 · 峰值 200 Mbps", charge_type: "subscription", expires_at: iso(Date.UTC(year + 2, 4, 11) - TZ_OFFSET_MS),
      public_ips: [{ address: "8.133.189.21", type: "instance_public_ip" }], billing_subject_id: "sub_sha", billing_subject_kind: "swas_no_traffic_package", metering_rule: "not_metered", bandwidth_mbps: 200 }),
    inst({ id: "ins_lite", name: "sha-pro-lite", account_id: a2.id, kind: "ecs", region_id: "cn-shanghai", spec: "ecs.e-c2m1.large", status: "stopped", public_ips: [{ address: "47.117.174.245", type: "instance_public_ip" }],
      billing_subject_id: "sub_lite", billing_subject_kind: "ecs_pay_by_traffic", metering_rule: "outbound_only", bandwidth_mbps: 100, counts_toward_cdt: true, cdt_scope: "mainland", realtime_protection: "unprotected" }),
  ];

  /* ---------- 每日用量与额度 ---------- */
  const series = (seed: number, totalGb: number) => {
    const r = rng(seed);
    const raw = Array.from({ length: dom }, (_, i) => (0.75 + r() * 0.5) * (i === dom - 1 ? (now - todayStart) / 86400_000 : 1));
    const sum = raw.reduce((x, y) => x + y, 0);
    return raw.map((v) => Math.round((v / sum) * totalGb * GB));
  };
  const mk = (a: T.Account, seed: number, overseasGb: number, mainlandGb: number, incO: number, incM: number, worst: number) => {
    const o = series(seed, overseasGb + incO), m = series(seed + 7, mainlandGb + incM);
    base.daily[a.id] = {
      account_id: a.id, data_timezone: "Asia/Shanghai", cycle_start: iso(cycleStart), cycle_end: iso(cycleEnd), updated_at: iso(realtimeAt),
      source_status: "partial",
      official_points: { mainland:[],overseas:[] },
      days: o.map((v,i)=>{
        const from=cycleStart+i*86400_000,until=Math.min(from+86400_000,realtimeAt),today=i===dom-1;
        const detail=(value:number)=>({bytes:today?null:value,known_bytes:value,has_data:true,source:"agent_estimate" as const,status:today?"partial" as const:"complete" as const,is_complete_day:!today,from_at:iso(from),until_at:iso(Math.min(now,from+86400_000)),observed_from:iso(from),observed_until:iso(until),reasons:today?["day_in_progress","coverage_gap"]:["topology_observed_only"]});
        return {date:dateStr(i+1),overseas_bytes:today?null:v,mainland_bytes:today?null:m[i],provisional:true,scopes:{overseas:detail(v),mainland:detail(m[i])}};
      }),
    };
    const q = (scope: T.QuotaScope, usedGb: number, incGb: number, quota: number, daily: number[], wc: number): T.QuotaStatus => {
      const official = Math.round(usedGb * GB), inc = Math.round(incGb * GB) + wc, est = official + inc;
      const last7 = daily.slice(-8, -1);
      const avg = last7.length ? Math.ceil(last7.reduce((x, y) => x + y, 0) / last7.length) : 0;
      const points=Array.from({length:daysInMonth-dom+1},(_,i)=>({at:iso(cycleStart+(dom+i)*86400_000),date:dateStr(dom+i),bytes:est+Math.ceil(avg*(cycleStart+(dom+i)*86400_000-realtimeAt)/86400_000)}));
      const end=points.at(-1)?.bytes??est;
      base.daily[a.id].official_points![scope]=Array.from({length:Math.min(5,dom)},(_,i)=>({at:iso(now-i*86400_000),date:dateStr(dom-i),bytes:Math.max(0,official-i*avg)})).reverse();
      const exceedOn=est>quota?dateStr(dom):points.find(p=>p.bytes>quota)?.date??null;
      return {
        account_id: a.id, scope, cycle_start: iso(cycleStart), cycle_end: iso(cycleEnd), quota_bytes: quota,
        official_bytes: official, official_as_of: iso(officialAsOf), official_status: stale ? "unavailable" : "ok",
        realtime_increment_bytes: inc, worst_case_bytes: wc, estimated_bytes: est, effective_bytes: est, thresholds_percent: a.alert_thresholds_percent,
        forecast_status:dom>=2?"ready":"insufficient_data",
        forecast: dom >= 2 ? { daily_average_bytes:avg,cycle_end_bytes:end,exceeds_on:exceedOn,already_exceeded:est>quota,sample_days:last7.length,sample_dates:Array.from({length:last7.length},(_,i)=>dateStr(dom-last7.length+i)),source:"agent_estimate",unverified:true,baseline_bytes:est,baseline_at:iso(realtimeAt),baseline_source:"realtime_estimate",baseline_at_assumed:false,calculated_at:iso(now),points:[{at:iso(realtimeAt),date:dateStr(dom),bytes:est},...points],reasons:last7.length<7?["fewer_than_seven_complete_days"]:[] } : null,
        updated_at: iso(realtimeAt),
      };
    };
    base.quotas.push(q("overseas", overseasGb, incO, a.quota_overseas_bytes, o, worst), q("mainland", mainlandGb, incM, a.quota_mainland_bytes, m, 0));
  };
  mk(a1, 11, 164.8, 0.9, 3.6, 0.0, offline ? hkgAgent.worst_case!.accumulated_bytes : 0);
  mk(a2, 23, 0.7, 5.6, 0.0, 0.1, 0);

  /* ---------- 共享带宽与计费对象 ---------- */
  const hours = Math.floor((now - todayStart) / 3600_000) + 1;
  const IN = [0.42, 0.31, 0.22, 0.18, 0.15, 0.17, 0.24, 0.33, 0.41, 0.45, 0.48, 0.47, 0.46, 0.5, 0.52, 0.55, 0.58, 0.56, 0.6, 0.63, 0.66, 0.61, 0.55, 0.48];
  const OUT = [0.4, 0.3, 0.21, 0.17, 0.14, 0.16, 0.23, 0.31, 0.39, 0.43, 0.46, 0.45, 0.44, 0.48, 0.49, 0.53, 0.56, 0.57, 0.58, 0.6, 0.67, 0.62, 0.53, 0.46];
  const frac = (now - todayStart - (hours - 1) * 3600_000) / 3600_000;
  const periods = Array.from({ length: hours }, (_, h) => {
    const k = h === hours - 1 ? frac : 1;
    const rx = Math.round(IN[h] * k * GB), tx = Math.round(OUT[h] * k * GB);
    return { start: iso(todayStart + h * 3600_000), rx_bytes: rx, tx_bytes: tx, metered_bytes: Math.max(rx, tx) };
  });
  const sumBy = (f: (p: (typeof periods)[number]) => number) => periods.reduce((s, p) => s + f(p), 0);
  const pkgRx = sumBy((p) => p.rx_bytes), pkgTx = sumBy((p) => p.tx_bytes), pkgMetered = sumBy((p) => p.metered_bytes);
  base.packages = [{
    id: "cbwp_sg", cloud_id: "cbwp-t4n0example", name: "sg-share", account_id: a1.id, region_id: "ap-southeast-1", bandwidth_mbps: 2000, kind: "cbwp_dominant_traffic", settled_by_cdt: true,
    members: [{ eip_address: "8.219.235.129", instance_id: "ins_sin", instance_name: "sin-pro" }],
    today_rx_bytes: pkgRx, today_tx_bytes: pkgTx, today_metered_bytes: pkgMetered, dominant_direction: pkgRx >= pkgTx ? "inbound" : "outbound", updated_at: iso(realtimeAt),
  }];
  base.hourly["cbwp_sg"] = { package_id: "cbwp_sg", period_seconds: 3600, periods, updated_at: iso(realtimeAt) };
  const hkPeriods = periods.map((p) => ({ ...p, rx_bytes: Math.round(p.rx_bytes / 2), tx_bytes: Math.round(p.tx_bytes / 2), metered_bytes: Math.max(Math.round(p.rx_bytes / 2), Math.round(p.tx_bytes / 2)) }));
  const hkRx = hkPeriods.reduce((s, p) => s + p.rx_bytes, 0), hkTx = hkPeriods.reduce((s, p) => s + p.tx_bytes, 0), hkMetered = hkPeriods.reduce((s, p) => s + p.metered_bytes, 0);
  base.packages.push({ ...base.packages[0], id: "cbwp_hk", cloud_id: "cbwp-hk-example", name: "hk-share", region_id: "cn-hongkong", members: [{ eip_address: "47.243.157.55", instance_id: "ins_hkg", instance_name: "hkg-pro" }], today_rx_bytes: hkRx, today_tx_bytes: hkTx, today_metered_bytes: hkMetered });
  base.hourly["cbwp_hk"] = { package_id: "cbwp_hk", period_seconds: 3600, periods: hkPeriods, updated_at: iso(realtimeAt) };

  const dayFrac = (now - todayStart) / 86400_000;
  const sub = (id: string, name: string, account_id: string, instId: string, src: T.BillingSubject["data_source"], rxGb: number | null, txGb: number | null): T.BillingSubject => {
    const i = instances.find((x) => x.id === instId)!;
    const rx = rxGb === null ? null : Math.round(rxGb * dayFrac * GB), tx = txGb === null ? null : Math.round(txGb * dayFrac * GB);
    return {
      id, name, account_id, kind: i.billing_subject_kind!, metering_rule: i.metering_rule!, counts_toward_cdt: i.counts_toward_cdt, cdt_scope: i.cdt_scope,
      public_ips: i.public_ips.map((p) => p.address), instance_ids: [instId], bandwidth_mbps: i.bandwidth_mbps, data_source: src,
      today_rx_bytes: rx, today_tx_bytes: tx, today_metered_bytes: i.metering_rule === "outbound_only" ? tx : null, updated_at: src === "none" ? null : iso(src === "agent" ? realtimeAt : now - 90_000),
    };
  };
  base.subjects = [
    { ...sub("sub_sgshare", "sg-share", a1.id, "ins_sin", "agent", 0, 0), today_rx_bytes: pkgRx, today_tx_bytes: pkgTx, today_metered_bytes: pkgMetered },
    { ...sub("sub_hkg", "hk-share", a1.id, "ins_hkg", "agent", 0, 0), today_rx_bytes: hkRx, today_tx_bytes: hkTx, today_metered_bytes: hkMetered },
    sub("sub_spot", "demo-spot", a1.id, "ins_spot", "cloud_monitor", 0.3, 0.4),
    sub("sub_lite", "sha-pro-lite", a2.id, "ins_lite", "none", null, null),
    sub("sub_web", "web-sh", a1.id, "ins_web", "cloud_monitor", 1.1, 2.7),
    sub("sub_sha", "sha-pro", a2.id, "ins_sha", "none", null, null),
  ];

  /* ---------- 通知 ---------- */
  base.telegram = { bot_token_set: true, chat_id: "-1000000000000", proxy_mode: "none", proxy_url: "", status: stale ? "failing" : "ok", last_error: stale ? { code: "telegram_unreachable", message: "dial tcp: i/o timeout" } : null, last_success_at: iso(now - (stale ? 5 : 0.6) * 3600_000) };
  let nid = 100;
  const note = (minsAgo: number, severity: T.Severity, rule: T.AlertRuleKey | null, title: string, body: string, kind: T.Notification["kind"] = "alert", account: string | null = null, instance: string | null = null, delivery: T.Notification["delivery_status"] = "sent"): T.Notification => ({
    id: "ntf_" + nid--, created_at: iso(now - minsAgo * 60_000), kind, severity, rule_key: rule, title, body, account_id: account, instance_id: instance, probe_plan_id:null, delivery_status: delivery,
  });
  base.notifications = [
    ...(offline ? [note(4, "critical", "agent_offline", "hkg-pro 的采集程序掉线了", "已经 30 秒没有收到上报。掉线期间按之前 10 分钟的最高速度（182 Mbps）估算用量。", "alert", a1.id, "ins_hkg")] : []),
    ...(stale ? [
      note(96, "serious", "account_api_failure", "账号 winnemjw 连接失败", "连续 3 次调用阿里云接口失败：AccessKey 不存在或已被禁用。", "alert", a2.id, null, "failed"),
      note(131, "warning", "cdt_official_unavailable", "取不到 CDT 官方数据", "连续 3 次拉取失败，正沿用最近一次官方值继续估算。", "alert", a1.id, null, "failed"),
    ] : []),
    note(37, "serious", "cdt_usage", "jasperxzy 的海外免费额度已用超过 80%", "实时估算 168.4 GB / 200 GB（官方截至上一个整点为 164.8 GB）。", "alert", a1.id),
    note(190, "warning", "cdt_forecast_exceed", "jasperxzy 的海外额度预计月底前会超出", "按最近 7 天日均 15.1 GB 推算，再过两三天就会超出 200 GB 免费额度。", "alert", a1.id),
    note(26 * 60, "info", "agent_offline", "sin-pro 的采集程序恢复上报", "掉线 48 秒后恢复，已用真实数值替换推算值。", "recovery", a1.id, "ins_sin"),
    note(26 * 60 + 1, "critical", "agent_offline", "sin-pro 的采集程序掉线了", "已经 30 秒没有收到上报。掉线期间按之前 10 分钟的最高速度估算用量。", "alert", a1.id, "ins_sin"),
    note(3 * 24 * 60, "info", null, "测试消息", "如果你在 Telegram 里看到这条消息，说明通知已经配置好了。", "test"),
  ];

  base.accounts = [a1, a2];
  // 续费方式、规格带宽、IPv6（M2 字段）
  for (const i of instances) {
    i.renewal = { status: i.charge_type !== "subscription" ? "not_applicable" : i.id === "ins_web" ? "auto_renewal" : "manual_renewal", source: i.charge_type === "subscription" ? "bss_query_available_instances" : null, checked_at: iso(scanAt), last_error: null };
    i.ipv6 = { addresses: i.id === "ins_hkg" ? ["240b:4001:27a:5c00::1f"] : [], status: "ok", source: "ecs_instances", checked_at: iso(scanAt), observed_at: iso(scanAt), last_error: null };
    if (i.kind === "ecs" && i.bandwidth_mbps) {
      const tx = i.id === "ins_sin" ? 450_000 : 3_000_000; // sin-pro：买了 2000 Mbps，规格只能跑约 450 Mbps
      i.network_capacity = { source: "ecs_describe_instance_types", checked_at: iso(scanAt), rx_kbps: tx, tx_kbps: tx, public_outbound_mbps: i.bandwidth_mbps, public_outbound_exceeds_spec: i.bandwidth_mbps * 1000 > tx, last_error: null };
    }
  }
  base.instances = instances;
  base.instancesUpdatedAt = iso(scanAt);
  if (scene === "cutoff_unknown") {
    for (const q of base.quotas) {
      const complete=q.scope==="overseas" && q.account_id===a1.id;
      q.estimation={mode:"unverified",status:complete?"ready":"incomplete",basis_id:"demo-basis",from_at:iso(now-3600_000),to_at:iso(realtimeAt),calculated_at:iso(realtimeAt),lag_window_seconds:3600,increment_bytes:complete?q.realtime_increment_bytes:null,known_increment_bytes:complete?q.realtime_increment_bytes??0:150_000_000,worst_case_bytes:complete?q.worst_case_bytes:null,estimated_bytes:complete?q.estimated_bytes:null,current_rate_bytes_per_second:100000,qualified_for_execution:false,reasons:complete?["lag_window_assumption","official_cutoff_or_unit_unverified"]:["coverage_gap","unattributed_subject"]};
      Object.assign(q,{official_as_of:null,official_status:"cutoff_unknown",official_quality:"inferred",fetched_at:iso(now),realtime_increment_bytes:null,worst_case_bytes:null,estimated_bytes:null,effective_bytes:null,forecast:null,forecast_status:"insufficient_data"});
    }
    for (const d of Object.values(base.daily)) {
      d.source_status="partial";
      for (const [index,day] of d.days.entries()) {
        for (const scope of ["mainland","overseas"] as const) {
          const detail=day.scopes![scope],recent=index>=d.days.length-2;
          detail.bytes=null;detail.is_complete_day=false;detail.has_data=recent;detail.known_bytes=recent?Math.round((scope==="overseas"?1.84:0.07)*GB):0;
          detail.status=recent?"partial":"unavailable";detail.source=recent?"agent_estimate":"unavailable";detail.reasons=["topology_history_incomplete",...(index===d.days.length-1?["day_in_progress"]:[])];
          if(!recent){detail.observed_from=null;detail.observed_until=null;}
        }
        day.mainland_bytes=null;day.overseas_bytes=null;
      }
    }
    for (const i of base.instances) if (i.counts_toward_cdt) { i.realtime_protection = "unprotected"; i.traffic_estimation=base.quotas.find((q)=>q.account_id===i.account_id&&q.scope===i.cdt_scope)?.estimation; }
  }
  return base;
}

export const REGIONS: Record<T.AccountSite, T.Region[]> = {
  china: ["cn-hangzhou", "cn-shanghai", "cn-beijing", "cn-shenzhen", "cn-guangzhou", "cn-chengdu", "cn-qingdao", "cn-zhangjiakou"].map((id) => ({ id, mainland: true }))
    .concat(["cn-hongkong", "ap-southeast-1", "ap-northeast-1", "ap-southeast-5", "us-west-1", "us-east-1", "eu-central-1"].map((id) => ({ id, mainland: false }))),
  international: ["cn-hongkong", "ap-southeast-1", "ap-southeast-3", "ap-southeast-5", "ap-northeast-1", "ap-northeast-2", "us-west-1", "us-east-1", "eu-central-1", "eu-west-1", "me-east-1"].map((id) => ({ id, mainland: false })),
};

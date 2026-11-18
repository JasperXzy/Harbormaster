// 模拟后端：在浏览器里按契约应答，不发网络请求。只在 VITE_API_MODE=mock 时被加载。
import type * as T from "../api/types";
import type { EventName } from "../api/types";
import { GB } from "../lib/format";
import { buildDb, noAgent, REGIONS, type Db, type Scene, SCENES } from "./data";

const SCENE_KEY = "hm-mock-scene";
const AUTH_KEY = "hm-mock-auth";

export function getScene(): Scene {
  try {
    const s = localStorage.getItem(SCENE_KEY) as Scene | null;
    if (s && SCENES.some(([k]) => k === s)) return s;
  } catch {
    /* 忽略 */
  }
  return "normal";
}
export function setScene(s: Scene) {
  try {
    localStorage.setItem(SCENE_KEY, s);
    sessionStorage.removeItem(AUTH_KEY);
  } catch {
    /* 忽略 */
  }
  location.assign(s === "setup" ? "/setup" : s === "logged_out" ? "/login" : "/");
}

const scene = getScene();
const db: Db = buildDb(scene);
let authed = scene !== "setup" && scene !== "logged_out";
try {
  const saved = sessionStorage.getItem(AUTH_KEY);
  if (saved) authed = saved === "1";
} catch {
  /* 忽略 */
}
const setAuthed = (v: boolean) => {
  authed = v;
  try {
    sessionStorage.setItem(AUTH_KEY, v ? "1" : "0");
  } catch {
    /* 忽略 */
  }
};

/* ---------- 事件流 ---------- */
type Emit = (name: EventName, data: unknown) => void;
const emitters = new Set<Emit>();
const emit: Emit = (n, d) => emitters.forEach((fn) => fn(n, structuredClone(d)));
let ticker: ReturnType<typeof setInterval> | null = null;
const iso = (ms = Date.now()) => new Date(ms).toISOString();
const TICK_SECONDS = 10;

function tick() {
  const now = Date.now();
  const touched = new Set<T.QuotaStatus>();
  for (const i of db.instances) {
    const quota = db.quotas.find((q) => q.account_id === i.account_id && q.scope === i.cdt_scope);
    if (i.agent.status === "online" && i.realtime) {
      const jitter = () => 0.85 + Math.random() * 0.3;
      i.realtime = { rx_bytes_per_second: Math.round(i.realtime.rx_bytes_per_second * jitter()), tx_bytes_per_second: Math.round(i.realtime.tx_bytes_per_second * jitter()), sampled_at: iso(now) };
      i.agent.last_report_at = iso(now);
      emit("instance.realtime", { instance_id: i.id, realtime: i.realtime });
      if (quota && quota.realtime_increment_bytes !== null) {
        const rate = i.metering_rule === "max_in_out_per_period" ? Math.max(i.realtime.rx_bytes_per_second, i.realtime.tx_bytes_per_second) : i.realtime.tx_bytes_per_second;
        quota.realtime_increment_bytes += rate * TICK_SECONDS;
        touched.add(quota);
      }
    } else if (i.agent.status === "offline" && i.agent.worst_case && quota && quota.realtime_increment_bytes !== null && quota.worst_case_bytes !== null) {
      const add = i.agent.worst_case.rate_bytes_per_second * TICK_SECONDS;
      i.agent.worst_case.accumulated_bytes += add;
      quota.realtime_increment_bytes += add;
      quota.worst_case_bytes += add;
      touched.add(quota);
    }
  }
  for (const q of touched) {
    if (q.official_bytes === null || q.realtime_increment_bytes === null) continue;
    q.estimated_bytes = q.official_bytes + q.realtime_increment_bytes;
    q.effective_bytes = Math.max(q.official_bytes, q.estimated_bytes);
    q.updated_at = iso(now);
    emit("quota.updated", q);
  }
  emit("ping", {});
}

export function subscribeMock(fn: Emit, onState: (open: boolean) => void): () => void {
  emitters.add(fn);
  const live = scene !== "stale" && scene !== "error";
  setTimeout(() => onState(live), 300);
  if (live && !ticker) ticker = setInterval(tick, TICK_SECONDS * 1000);
  return () => {
    emitters.delete(fn);
    if (!emitters.size && ticker) {
      clearInterval(ticker);
      ticker = null;
    }
  };
}

/* ---------- 任务 ---------- */
let jobSeq = 0xa100;
function startJob(type: T.Job["type"], ms: number, run: () => { result?: Record<string, unknown>; error?: { code: string; message: string } }): T.JobAccepted {
  const job: T.Job = { id: "job_" + (jobSeq++).toString(16), type, status: "queued", created_at: iso(), started_at: null, finished_at: null, error: null, result: null };
  db.jobs.set(job.id, job);
  setTimeout(() => {
    job.status = "running";
    job.started_at = iso();
    emit("job.updated", job);
  }, 200);
  setTimeout(() => {
    const out = run();
    job.status = out.error ? "failed" : "succeeded";
    job.error = out.error ?? null;
    job.result = out.result ?? null;
    job.finished_at = iso();
    emit("job.updated", job);
  }, ms);
  return { job_id: job.id };
}

/* ---------- 应答 ---------- */
interface Res {
  status: number;
  body: unknown;
}
const ok = (body: unknown, status = 200): Res => ({ status, body: body === undefined ? null : structuredClone(body) });
const fail = (status: number, code: string, extra: object = {}): Res => ({ status, body: { error: { code, message: `mock: ${code}`, ...extra } } });
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const session = (): T.Session => ({ csrf_token: "mock-csrf", expires_at: iso(Date.now() + 7 * 86400_000) });

function page<X>(items: X[], q: URLSearchParams) {
  const limit = Math.min(200, Number(q.get("limit")) || 50), from = Number(q.get("cursor")) || 0;
  const slice = items.slice(from, from + limit);
  return { items: slice, next_cursor: from + limit < items.length ? String(from + limit) : null };
}

const apiKeys: T.APIKey[] = scene === "empty" || scene === "setup" ? [] : [
  { id: "key_widget", name: "手机小组件", scopes: ["summary:read"], created_at: iso(Date.now() - 20 * 86400_000), last_used_at: iso(Date.now() - 4 * 60_000), revoked_at: null },
  { id: "key_old", name: "旧脚本", scopes: ["summary:read", "cron:run"], created_at: iso(Date.now() - 80 * 86400_000), last_used_at: iso(Date.now() - 41 * 86400_000), revoked_at: iso(Date.now() - 40 * 86400_000) },
];

let failedLogins = 0;

export async function handle(method: string, fullPath: string, body: unknown): Promise<Res> {
  await sleep(scene === "slow" ? 4000 : 180 + Math.random() * 220);
  const [path, query = ""] = fullPath.split("?");
  const q = new URLSearchParams(query);
  const b = (body ?? {}) as Record<string, unknown>;
  const seg = path.split("/").filter(Boolean);
  const is = (m: string, p: string) => method === m && path === p;

  /* 公开接口 */
  if (is("GET", "/setup/status")) return ok({ initialized: db.initialized, password_min_length: 10 } satisfies T.SetupStatus);
  if (is("POST", "/setup")) {
    if (db.initialized) return fail(409, "already_initialized");
    if (b.setup_token === "wrong") return fail(403, "invalid_setup_token");
    if (String(b.password ?? "").length < 10) return fail(422, "validation_failed", { fields: { password: "too_short" } });
    db.initialized = true;
    setAuthed(true);
    return ok(session(), 201);
  }
  if (is("POST", "/session")) {
    if (b.password === "wrong" || String(b.password ?? "").length < 4) {
      failedLogins++;
      if (failedLogins >= 4) return fail(429, "rate_limited", { retry_after_seconds: 30 });
      return fail(401, "invalid_credentials");
    }
    failedLogins = 0;
    setAuthed(true);
    return ok(session());
  }
  if (!authed) return fail(401, "unauthenticated");
  if (is("GET", "/session")) return ok(session());
  if (is("DELETE", "/session")) {
    setAuthed(false);
    return ok(undefined, 204);
  }

  /* 出错场景：登录正常，数据接口全部失败 */
  if (scene === "error" && !(seg[0] === "settings" || seg[0] === "system")) return fail(500, "internal_error");

  if (is("GET", "/system/info")) return ok({ version: "0.1.0-mock", commit: "0000000", built_at: iso(Date.now() - 86400_000), latest_agent_version: "0.1.0", data_mode: "demo", read_only: false } satisfies T.SystemInfo);
  if (is("GET", "/settings")) return ok(db.settings);
  if (is("PATCH", "/settings")) {
    if (typeof b.business_timezone === "string") db.settings.business_timezone = b.business_timezone;
    return ok(db.settings);
  }
  if (is("GET", "/regions")) return ok({ items: REGIONS[(q.get("site") as T.AccountSite) ?? "china"] ?? REGIONS.china });
  if (seg[0] === "jobs" && method === "GET") {
    const job = db.jobs.get(seg[1]);
    return job ? ok(job) : fail(404, "not_found");
  }

  /* 账号 */
  if (is("GET", "/accounts")) return ok({ items: db.accounts });
  if (is("POST", "/accounts")) {
    const c = b as unknown as T.AccountCreate;
    const fields: Record<string, string> = {};
    if (!c.name?.trim()) fields.name = "required";
    if (!c.access_key_id?.trim()) fields.access_key_id = "required";
    if (!c.access_key_secret?.trim()) fields.access_key_secret = "required";
    if (!c.region_ids?.length) fields.region_ids = "required";
    if (Object.keys(fields).length) return fail(422, "validation_failed", { fields });
    if (db.accounts.some((a) => a.name === c.name)) return fail(409, "account_name_taken");
    if (c.access_key_id === "wrong") return fail(422, "access_key_invalid");
    const account: T.Account = {
      id: "acc_" + Math.random().toString(36).slice(2, 8), name: c.name, site: c.site, access_key_id_masked: c.access_key_id.slice(0, 6) + "…" + c.access_key_id.slice(-3), region_ids: c.region_ids, enabled: true,
      quota_mainland_bytes: c.quota_mainland_bytes ?? 20 * GB, quota_overseas_bytes: c.quota_overseas_bytes ?? 200 * GB, reset_day: c.reset_day ?? 1, alert_thresholds_percent: c.alert_thresholds_percent ?? [80, 90, 95],
      sync_status: "never_synced", last_sync_at: null, last_error: null, consecutive_failures: 0, permission_check: null, instance_count: 0, updated_at: iso(),
    };
    db.accounts.push(account);
    const job = startJob("permission_check", 2200, () => {
      account.permission_check = { checked_at: iso(), items: (["ecs_read", "swas_read", "cdt_read", "cms_read", "instance_power", "vpc_read", "eip_manage", "bss_read"] as T.PermissionKey[]).map((key) => ({ key, result: key === "eip_manage" ? "missing" : key === "instance_power" ? "not_verified" : "passed" })) };
      account.sync_status = "ok";
      account.last_sync_at = iso();
      return { result: account.permission_check as unknown as Record<string, unknown> };
    });
    return ok({ account, permission_check_job_id: job.job_id }, 201);
  }
  if (seg[0] === "accounts" && seg[1]) {
    const a = db.accounts.find((x) => x.id === seg[1]);
    if (!a) return fail(404, "not_found");
    if (method === "PATCH" && seg.length === 2) {
      const u = b as T.AccountUpdate;
      if (u.name !== undefined && !u.name.trim()) return fail(422, "validation_failed", { fields: { name: "required" } });
      if (u.access_key_id) a.access_key_id_masked = u.access_key_id.slice(0, 6) + "…" + u.access_key_id.slice(-3);
      const { access_key_id: _id, access_key_secret: _secret, ...rest } = u;
      Object.assign(a, rest, { updated_at: iso() });
      for (const qt of db.quotas.filter((x) => x.account_id === a.id)) {
        qt.quota_bytes = qt.scope === "mainland" ? a.quota_mainland_bytes : a.quota_overseas_bytes;
        qt.thresholds_percent = a.alert_thresholds_percent;
      }
      return ok(a);
    }
    if (method === "DELETE" && seg.length === 2) {
      a.enabled = false;
      a.updated_at = iso();
      return ok(undefined, 204);
    }
    if (method === "POST" && seg[2] === "permission-check")
      return ok(startJob("permission_check", 1800, () => {
        const prev = a.permission_check;
        a.permission_check = { checked_at: iso(), items: prev?.items ?? [] };
        return { result: a.permission_check as unknown as Record<string, unknown> };
      }), 202);
  }

  /* 实例与采集程序 */
  if (is("GET", "/instances")) {
    const items = db.instances.filter((i) => (!q.get("account_id") || i.account_id === q.get("account_id")) && (!q.get("kind") || i.kind === q.get("kind")));
    return ok({ ...page(items, q), updated_at: db.instancesUpdatedAt });
  }
  if (is("POST", "/instances/refresh"))
    return ok(startJob("refresh", 2400, () => {
      if (scene === "stale") return { error: { code: "aliyun_unreachable", message: "mock" } };
      db.instancesUpdatedAt = iso();
      db.instances.forEach((i) => (i.updated_at = iso()));
      db.accounts.forEach((a) => (a.last_sync_at = iso()));
      return {};
    }), 202);
  if (seg[0] === "instances" && seg[2] === "agent") {
    const i = db.instances.find((x) => x.id === seg[1]);
    if (!i) return fail(404, "not_found");
    if (method === "POST" && seg[3] === "install-command") {
      const code = Math.random().toString(36).slice(2, 10);
      // 模拟：命令生成约 12 秒后，采集程序"装好并上线"
      setTimeout(() => {
        i.agent = { status: "online", version: "0.1.0", registered_at: iso(), last_report_at: iso(), offline_since: null, worst_case: null };
        if (i.counts_toward_cdt) i.realtime_protection = "protected";
        i.realtime = { rx_bytes_per_second: 420_000, tx_bytes_per_second: 610_000, sampled_at: iso() };
        emit("agent.status", { instance_id: i.id, agent: i.agent, realtime_protection: i.realtime_protection });
        emit("instance.realtime", { instance_id: i.id, realtime: i.realtime });
      }, 12_000);
      return ok({ command: `curl -fsSL https://harbor.example.com/install-agent.sh | sudo sh -s -- --server https://harbor.example.com --code ${code}`, expires_at: iso(Date.now() + 15 * 60_000) } satisfies T.AgentInstallCommand, 201);
    }
    if (method === "DELETE" && seg[3] === "token") {
      i.agent = noAgent();
      i.realtime = null;
      if (i.counts_toward_cdt) i.realtime_protection = "unprotected";
      emit("agent.status", { instance_id: i.id, agent: i.agent, realtime_protection: i.realtime_protection });
      return ok(undefined, 204);
    }
  }

  /* 流量 */
  const byAccount = <X extends { account_id: string }>(xs: X[]) => xs.filter((x) => !q.get("account_id") || x.account_id === q.get("account_id"));
  if (is("GET", "/traffic/quotas")) return ok({ items: byAccount(db.quotas) });
  if (is("GET", "/traffic/observations")) {
    const id = q.get("account_id");
    if (!db.accounts.some((a) => a.id === id)) return fail(404, "not_found");
    const known = (scope: T.QuotaScope, i: number) => { const v = db.quotas.find((v) => v.account_id === id && v.scope === scope)?.official_bytes; return v == null ? null : Math.round(v * (1 - i * 0.002)); };
    return ok({ items: Array.from({ length: 8 }, (_, i) => ({ id: String(8 - i), account_id: id, fetched_at: iso(Date.now() - i * 300_000), mainland_bytes: known("mainland", i), overseas_bytes: known("overseas", i), official_quality: "inferred", error: null })), next_cursor: null });
  }
  if (is("GET", "/traffic/daily")) {
    const d = db.daily[q.get("account_id") ?? ""];
    return d ? ok(d) : fail(404, "not_found");
  }
  if (is("GET", "/traffic/billing-subjects")) return ok({ items: byAccount(db.subjects) });
  if (is("GET", "/bandwidth-packages")) return ok({ items: db.packages });
  if (seg[0] === "bandwidth-packages" && seg[2] === "hourly") {
    const h = db.hourly[seg[1]];
    return h ? ok(h) : fail(404, "not_found");
  }

  /* 通知 */
  if (is("GET", "/notify/telegram")) return ok(db.telegram);
  if (is("PUT", "/notify/telegram")) {
    const u = b as unknown as T.TelegramChannelUpdate;
    const fields: Record<string, string> = {};
    if (!u.bot_token && !db.telegram.bot_token_set) fields.bot_token = "required";
    if (!u.chat_id?.trim()) fields.chat_id = "required";
    if (u.proxy_mode !== "none" && !u.proxy_url?.trim()) fields.proxy_url = "required";
    if (Object.keys(fields).length) return fail(422, "validation_failed", { fields });
    db.telegram = { ...db.telegram, bot_token_set: true, chat_id: u.chat_id, proxy_mode: u.proxy_mode, proxy_url: u.proxy_mode === "none" ? "" : u.proxy_url, status: db.telegram.status === "not_configured" ? "ok" : db.telegram.status };
    return ok(db.telegram);
  }
  if (is("POST", "/notify/telegram/test")) {
    if (!db.telegram.bot_token_set) return fail(409, "telegram_not_configured");
    return ok(startJob("telegram_test", 1500, () => {
      if (scene === "stale") return { error: { code: "telegram_unreachable", message: "mock" } };
      const n: T.Notification = { id: "ntf_" + Date.now(), created_at: iso(), kind: "test", severity: "info", rule_key: null, title: "测试消息", body: "如果你在 Telegram 里看到这条消息，说明通知已经配置好了。", account_id: null, instance_id: null, delivery_status: "sent" };
      db.notifications.unshift(n);
      db.telegram.status = "ok";
      db.telegram.last_error = null;
      db.telegram.last_success_at = iso();
      emit("notification.created", n);
      return {};
    }), 202);
  }
  if (is("GET", "/alert-rules")) return ok({ items: db.rules });
  if (seg[0] === "alert-rules" && method === "PATCH") {
    const r = db.rules.find((x) => x.key === seg[1]);
    if (!r) return fail(404, "not_found");
    const u = b as T.AlertRuleUpdate;
    if (u.enabled !== undefined) r.enabled = u.enabled;
    if (u.params) r.params = { ...r.params, ...u.params };
    return ok(r);
  }
  if (is("GET", "/notifications")) return ok(page(db.notifications, q));

  /* ---------- M2：费用、API Key、RAM 策略、实例变化、备份 ---------- */
  if (seg[0] === "accounts" && seg[2] === "finance" && method === "GET") {
    const a = db.accounts.find((x) => x.id === seg[1]);
    if (!a) return fail(404, "not_found");
    const now = Date.now(), cycle = new Date(now + 8 * 3600_000).toISOString().slice(0, 7);
    const src = { status: "ok" as const, fetched_at: iso(now - 9 * 60_000), last_attempt_at: iso(now - 9 * 60_000), last_error: null };
    const jasper = a.name === "jasperxzy";
    const item = (bill_type: string, product_code: string, product_name: string, amount: string): T.BillOverviewItem => ({ bill_type, product_code, product_name, currency: "CNY", pretax_amount: amount, payment_amount: amount, outstanding_amount: "0.00" });
    const failing = a.sync_status === "failing";
    return ok({
      account_id: a.id, enabled: a.enabled, billing_cycle: cycle, updated_at: src.fetched_at,
      balance: failing ? { status: "failing", fetched_at: iso(now - 3 * 3600_000), last_attempt_at: iso(now - 60_000), last_error: { code: "access_key_invalid", message: "mock" }, data: null }
        : { ...src, data: { currency: "CNY", available_amount: jasper ? "412.08" : "36.40", available_cash_amount: jasper ? "412.08" : "36.40", credit_amount: "0.00" } },
      bill: failing ? { status: "failing", fetched_at: null, last_attempt_at: iso(now - 60_000), last_error: { code: "access_key_invalid", message: "mock" }, data: null }
        : { ...src, data: { billing_cycle: cycle, items: jasper
            ? [item("PayAsYouGoBill", "ecs", "云服务器 ECS", "79.80"), item("PayAsYouGoBill", "eip", "弹性公网 IP", "3.26"), item("SubscriptionOrder", "ecs", "云服务器 ECS", "24.00"), item("PayAsYouGoBill", "cdt", "云数据传输", "0.00")]
            : [item("SubscriptionOrder", "swas", "轻量应用服务器", "24.00"), item("PayAsYouGoBill", "ecs", "云服务器 ECS", "8.15")] } },
    } as unknown as T.AccountFinance);
  }
  if (is("GET", "/api-keys")) return ok({ items: apiKeys, next_cursor: null });
  if (is("POST", "/api-keys")) {
    const name = String(b.name ?? "").trim(), scopes = (b.scopes ?? []) as T.APIKeyScope[];
    const fields: Record<string, string> = {};
    if (!name) fields.name = "required";
    if (!scopes.length) fields.scopes = "required";
    if (Object.keys(fields).length) return fail(422, "validation_failed", { fields });
    const api_key: T.APIKey = { id: "key_" + Math.random().toString(36).slice(2, 8), name, scopes, created_at: iso(), last_used_at: null, revoked_at: null };
    apiKeys.unshift(api_key);
    return ok({ api_key, token: "hm_mock_" + Math.random().toString(36).slice(2, 14) + Math.random().toString(36).slice(2, 14) }, 201);
  }
  if (seg[0] === "api-keys" && seg[1] && method === "DELETE") {
    const k = apiKeys.find((x) => x.id === seg[1]);
    if (!k) return fail(404, "not_found");
    k.revoked_at ??= iso();
    return ok(undefined, 204);
  }
  if (is("GET", "/ram-policies")) {
    const doc = (Action: string[]) => ({ Version: "1" as const, Statement: [{ Effect: "Allow" as const, Action, Resource: "*" }] });
    const read = ["ecs:Describe*", "swas-open:List*", "vpc:Describe*", "cms:DescribeMetricList", "bss:Query*", "cdt:ListCdtInternetTraffic"];
    const power = [...read, "ecs:StartInstance", "ecs:StopInstance", "ecs:RebootInstance", "swas-open:StartInstance", "swas-open:StopInstance", "swas-open:RebootInstance"];
    const items: T.RAMPolicyDraft[] = [
      { id: "readonly", reviewed_on: "2026-11-12", verification: "draft", cloud_write_permissions: false, limitations: ["cdt_action_unverified", "resource_scope_not_account_bound"], document: doc(read) },
      { id: "instance_power", reviewed_on: "2026-11-12", verification: "draft", cloud_write_permissions: true, limitations: ["cdt_action_unverified", "resource_scope_not_account_bound", "runtime_writes_unimplemented"], document: doc(power) },
      { id: "resource_management", reviewed_on: "2026-11-12", verification: "draft", cloud_write_permissions: true, limitations: ["cdt_action_unverified", "resource_scope_not_account_bound", "runtime_writes_unimplemented", "creation_dependencies_pending"], document: doc([...power, "vpc:AllocateEipAddress", "vpc:AssociateEipAddress", "vpc:UnassociateEipAddress", "vpc:ReleaseEipAddress", "ecs:RunInstances", "ecs:DeleteInstance"]) },
    ];
    return ok({ items });
  }
  if (is("GET", "/instance-events")) {
    const now = Date.now();
    const ev = (n: number, minsAgo: number, id: string, kind: T.InstanceEvent["kind"], before: T.InstanceEvent["before"], after: T.InstanceEvent["after"]): T.InstanceEvent | null => {
      const i = db.instances.find((x) => x.id === id);
      return i ? { id: "evt_" + n, account_id: i.account_id, instance_id: i.id, cloud_instance_id: i.cloud_instance_id, instance_kind: i.kind, region_id: i.region_id, instance_name: i.name, kind, observed_at: iso(now - minsAgo * 60_000), before, after } : null;
    };
    const all = [
      ev(5, 3 * 60, "ins_spot", "public_ipv4_changed", { addresses: ["47.242.18.21"] }, { addresses: ["47.242.18.66"] }),
      ev(4, 26 * 60, "ins_lite", "status_changed", { status: "running" }, { status: "stopped" }),
      ev(3, 3 * 24 * 60, "ins_hkg", "ipv6_changed", { addresses: [] }, { addresses: ["240b:4001:27a:5c00::1f"] }),
      ev(2, 9 * 24 * 60, "ins_sin", "discovered", null, { present: true, status: "running" }),
      ev(1, 9 * 24 * 60, "ins_hkg", "discovered", null, { present: true, status: "running" }),
    ].filter((x): x is T.InstanceEvent => !!x).filter((x) => !q.get("instance_id") || x.instance_id === q.get("instance_id"));
    return ok(page(all, q));
  }
  if (is("POST", "/backups")) return fail(501, "not_implemented");

  return fail(404, "not_found");
}

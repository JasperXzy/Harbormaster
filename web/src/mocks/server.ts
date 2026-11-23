import { mockQuotaTotals, mockDailyTotal } from "./traffic-aggregate";
// 模拟后端：在浏览器里按契约应答，不发网络请求。只在 VITE_API_MODE=mock 时被加载。
import type * as T from "../api/types";
import type { EventName } from "../api/types";
import { GB } from "../lib/format";
import { buildDb, computeOverageCost, noAgent, REGIONS, type Db, type Scene, SCENES } from "./data";

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
    const acc = db.accounts.find((a) => a.id === q.account_id);
    q.overage_cost = computeOverageCost(q, acc?.site ?? "china", db.pricing, q.effective_bytes);
    emit("quota.updated", q);
  }
  for (const q of db.quotas) {
    q.updated_at = iso(now);
    const e = q.estimation;
    if (!e) continue;
    e.calculated_at = iso(now);
    e.to_at = iso(now);
    if (e.status === "ready" && e.increment_bytes !== null && q.official_bytes !== null) {
      e.increment_bytes += (e.current_rate_bytes_per_second ?? 0) * TICK_SECONDS;
      e.known_increment_bytes = e.increment_bytes;
      e.estimated_bytes = q.official_bytes + e.increment_bytes;
    }
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
function startJob(
  type: T.Job["type"],
  ms: number,
  run: () => { result?: Record<string, unknown>; error?: { code: string; message: string } },
  meta?: { account_ids?: string[]; instance_id?: string | null; trigger?: T.JobTrigger },
): T.JobAccepted {
  const job: T.Job = {
    id: "job_" + (jobSeq++).toString(16),
    type,
    account_ids: meta?.account_ids ?? [],
    instance_id: meta?.instance_id ?? null,
    trigger: meta?.trigger ?? "panel",
    status: "queued",
    created_at: iso(),
    started_at: null,
    finished_at: null,
    error: null,
    result: null,
  };
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
let pendingCeremonyName = "安全密钥";

export async function handle(method: string, fullPath: string, body: unknown, _headers?: Record<string, string>): Promise<Res> {
  await sleep(scene === "slow" ? 4000 : 120 + Math.random() * 140);
  const [path, query = ""] = fullPath.split("?");
  const q = new URLSearchParams(query);
  const b = (body ?? {}) as Record<string, unknown>;
  const seg = path.split("/").filter(Boolean);
  const is = (m: string, p: string) => method === m && path === p;

  /* 公开接口 */
  if (is("GET", "/public/branding")) return ok({ name: db.branding.name, has_logo: Boolean(db.branding.logo), updated_at: db.branding.updated_at } satisfies T.PublicBranding);
  if (is("GET", "/setup/status")) return ok({ initialized: db.initialized, password_min_length: 10 } satisfies T.SetupStatus);
  if (is("POST", "/setup")) {
    if (db.initialized) return fail(409, "already_initialized");
    if (b.setup_token === "wrong") return fail(403, "invalid_setup_token");
    if (String(b.password ?? "").length < 10) return fail(422, "validation_failed", { fields: { password: "too_short" } });
    db.initialized = true;
    setAuthed(true);
    return ok(session(), 201);
  }
  if (is("GET", "/session/methods")) return ok({ password_enabled: db.initialized && db.passwordEnabled, passkey_enabled: db.initialized && db.passkeys.length > 0 } satisfies T.LoginMethods);
  if (is("POST", "/session/passkey/options")) {
    if (!db.passkeys.length) return fail(401, "invalid_passkey");
    return ok({ ceremony_id: "c".repeat(64), expires_at: iso(Date.now() + 300_000), options: { publicKey: { challenge: "bW9jaw" } } } satisfies T.PasskeyOptions);
  }
  if (is("POST", "/session/passkey/finish")) {
    setAuthed(true);
    if (db.passkeys[0]) db.passkeys[0].last_used_at = iso();
    return ok(session());
  }
  if (is("POST", "/session")) {
    if (!db.passwordEnabled) return fail(403, "password_login_disabled");
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
  if (is("PATCH", "/session/password")) {
    const enabled = Boolean(b.enabled);
    if (!enabled && !db.passkeys.length) return fail(409, "passkey_required");
    db.passwordEnabled = enabled;
    return ok({ password_enabled: db.passwordEnabled });
  }
  if (is("GET", "/passkeys")) return ok({ items: db.passkeys });
  if (is("POST", "/passkeys/registration/options")) {
    const name = String(b.name ?? "").trim();
    if (!name) return fail(422, "validation_failed", { fields: { name: "required" } });
    pendingCeremonyName = name;
    return ok({ ceremony_id: "r".repeat(64), expires_at: iso(Date.now() + 300_000), options: { publicKey: { challenge: "bW9jaw", user: { id: "dXNlcg", name: "administrator", displayName: "Harbormaster" } } } } satisfies T.PasskeyOptions);
  }
  if (is("POST", "/passkeys/registration/finish")) {
    const pk: T.Passkey = { id: "pk_" + Math.random().toString(36).slice(2, 8), name: pendingCeremonyName, created_at: iso(), last_used_at: null };
    db.passkeys.unshift(pk);
    return ok(pk, 201);
  }
  if (seg[0] === "passkeys" && seg[1] && method === "DELETE") {
    if (!db.passwordEnabled && db.passkeys.length <= 1) return fail(409, "last_passkey");
    db.passkeys = db.passkeys.filter((x) => x.id !== seg[1]);
    return ok(undefined, 204);
  }

  /* 出错场景：登录正常，数据接口全部失败 */
  if (scene === "error" && !(seg[0] === "settings" || seg[0] === "system")) return fail(500, "internal_error");

  if (is("GET", "/system/info")) return ok({ version: "0.4.0-mock", commit: "6530cb3", built_at: iso(Date.now() - 86400_000), latest_agent_version: "0.4.0", data_mode: "demo", read_only: false, instance_power_enabled: true, instance_release_enabled: false, eip_manage_enabled: false, instance_create_enabled: false } satisfies T.SystemInfo);
  if (is("GET", "/system/version-center") || is("POST", "/system/version-center/check")) {
    return ok({
      server_version: "0.4.0-mock",
      server_commit: "6530cb3",
      server_build_time: iso(Date.now() - 86400_000),
      bundled_agent_version: "0.4.0",
      deployment_mode: "demo",
      checked_at: method === "POST" ? iso() : null,
      latest_version: method === "POST" ? "0.4.0" : null,
      release_url: "https://github.com/JasperXzy/harbormaster/releases",
      release_notes: null,
      update_available: method === "POST" ? false : null,
      last_error: null,
      upgrade_commands: {
        docker_compose: "docker compose pull hm-server && docker compose up -d hm-server",
        binary_hint: "systemctl restart harbormaster-server",
        agent_hint: "在实例详情点击升级命令并在机器上执行",
      },
    } satisfies T.VersionCenter);
  }
  if (is("GET", "/settings/branding")) return ok(db.branding);
  if (is("PUT", "/settings/branding")) {
    const name = String(b.name ?? "").trim();
    if (!name || name.length > 40) return fail(422, "validation_failed", { fields: { name: "invalid_format" } });
    db.branding = { ...db.branding, revision: db.branding.revision + 1, name, updated_at: iso() };
    return ok(db.branding);
  }
  if (is("PUT", "/settings/branding/logo")) {
    db.branding = { ...db.branding, revision: db.branding.revision + 1, logo: { url: "/favicon.svg", media_type: "image/png", width: 64, height: 64, bytes: 1024 }, updated_at: iso() };
    return ok(db.branding);
  }
  if (is("DELETE", "/settings/branding/logo")) {
    db.branding = { ...db.branding, revision: db.branding.revision + 1, logo: null, updated_at: iso() };
    return ok(db.branding);
  }
  if (is("GET", "/settings")) return ok(db.settings);
  if (is("PATCH", "/settings")) {
    if (typeof b.business_timezone === "string") db.settings.business_timezone = b.business_timezone;
    if (b.retention && typeof b.retention === "object") Object.assign(db.settings.retention!, b.retention);
    return ok(db.settings);
  }
  if (is("GET", "/settings/heartbeat")) return ok(db.heartbeat);
  if (is("PUT", "/settings/heartbeat")) {
    const urlStr = typeof b.url === "string" ? b.url.trim() : undefined;
    const urlSet = urlStr !== undefined ? urlStr !== "" : db.heartbeat.url_configured;
    if (Boolean(b.enabled) && !urlSet) return fail(422, "validation_failed", { fields: { url: "required" } });
    db.heartbeat = {
      ...db.heartbeat,
      revision: db.heartbeat.revision + 1,
      enabled: Boolean(b.enabled),
      interval_seconds: Number(b.interval_seconds) || 60,
      url_configured: urlSet,
      updated_at: iso(),
    };
    return ok(db.heartbeat);
  }
  if (is("POST", "/settings/heartbeat/test")) {
    if (!db.heartbeat.url_configured) return fail(409, "heartbeat_not_configured");
    return ok(startJob("heartbeat_test", 1000, () => {
      db.heartbeat.last_attempt_at = iso();
      db.heartbeat.last_success_at = iso();
      db.heartbeat.last_error = null;
      return {};
    }), 202);
  }
  if (is("GET", "/audit-log")) {
    const items = db.auditLog.filter((a) =>
      (!q.get("actor") || a.actor.kind === q.get("actor")) &&
      (!q.get("target_type") || a.target_type === q.get("target_type")) &&
      (!q.get("account_id") || a.account_id === q.get("account_id")),
    );
    return ok(page(items, q));
  }
  if (is("GET", "/regions")) return ok({ items: REGIONS[(q.get("site") as T.AccountSite) ?? "china"] ?? REGIONS.china });
  if (is("GET", "/jobs")) {
    const all = [...db.jobs.values()].reverse().filter((j) =>
      (!q.get("account_id") || j.account_ids?.includes(q.get("account_id")!)) &&
      (!q.get("instance_id") || j.instance_id === q.get("instance_id")) &&
      (!q.get("type") || j.type === q.get("type")) &&
      (!q.get("status") || j.status === q.get("status")) &&
      (!q.get("trigger") || j.trigger === q.get("trigger")),
    );
    return ok(page(all, q));
  }
  if (seg[0] === "jobs" && seg[1] && method === "GET") {
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
      track_mainland: c.track_mainland ?? true, track_overseas: c.track_overseas ?? true,
      cdt_confirmed: c.cdt_confirmed ?? false, cdt_subject_overrides: {},
      quota_mainland_bytes: c.quota_mainland_bytes ?? 20 * GB, quota_overseas_bytes: c.quota_overseas_bytes ?? 200 * GB, reset_day: c.reset_day ?? 1, alert_thresholds_percent: c.alert_thresholds_percent ?? [80, 90, 95],
      sync_status: "never_synced", last_sync_at: null, last_error: null, consecutive_failures: 0, permission_check: null, instance_count: 0, updated_at: iso(),
    };
    db.accounts.push(account);
    const job = startJob("permission_check", 2200, () => {
      account.permission_check = { checked_at: iso(), items: (["ecs_read", "swas_read", "cdt_read", "cms_read", "instance_power", "vpc_read", "eip_manage", "bss_read"] as T.PermissionKey[]).map((key) => ({ key, result: key === "eip_manage" ? "missing" : key === "instance_power" ? "not_verified" : "passed" })) };
      account.sync_status = "ok";
      account.last_sync_at = iso();
      return { result: account.permission_check as unknown as Record<string, unknown> };
    }, { account_ids: [account.id] });
    return ok({ account, permission_check_job_id: job.job_id }, 201);
  }
  if (seg[0] === "accounts" && seg[1]) {
    const a = db.accounts.find((x) => x.id === seg[1]);
    if (!a) return fail(404, "not_found");
    if (method === "PATCH" && seg.length === 2) {
      const u = b as T.AccountUpdate;
      if (u.name !== undefined && !u.name.trim()) return fail(422, "validation_failed", { fields: { name: "required" } });
      if (u.access_key_id) a.access_key_id_masked = u.access_key_id.slice(0, 6) + "…" + u.access_key_id.slice(-3);
      const { access_key_id: _id, access_key_secret: _secret, cdt_subject_overrides: overrides, ...rest } = u;
      Object.assign(a, rest, { updated_at: iso() });
      if (overrides) {
        a.cdt_subject_overrides = { ...(a.cdt_subject_overrides ?? {}) };
        for (const [sid, val] of Object.entries(overrides)) {
          if (val === null) delete a.cdt_subject_overrides[sid];
          else if (typeof val === "boolean") a.cdt_subject_overrides[sid] = val;
        }
      }
      for (const s of db.subjects.filter((x) => x.account_id === a.id)) {
        if (s.metering_rule === "not_metered" || s.kind.startsWith("swas_") || s.kind === "unknown") continue;
        const ov = a.cdt_subject_overrides?.[s.id];
        s.counts_toward_cdt = ov !== undefined ? ov : a.cdt_confirmed ? true : null;
      }
      for (const ins of db.instances.filter((x) => x.account_id === a.id && x.billing_subject_id)) {
        const sub = db.subjects.find((x) => x.id === ins.billing_subject_id);
        if (sub) ins.counts_toward_cdt = sub.counts_toward_cdt;
      }
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
      }, { account_ids: [a.id] }), 202);
  }

  /* 实例与采集程序 */
  if (is("GET", "/instances")) {
    const items = db.instances.filter((i) => (!q.get("account_id") || i.account_id === q.get("account_id")) && (!q.get("kind") || i.kind === q.get("kind")));
    return ok({ ...page(items, q), updated_at: db.instancesUpdatedAt });
  }
  if (is("POST", "/instances/refresh"))
    return ok(startJob("refresh", 1800, () => {
      if (scene === "stale") return { error: { code: "aliyun_unreachable", message: "mock" } };
      db.instancesUpdatedAt = iso();
      db.instances.forEach((i) => (i.updated_at = iso()));
      db.accounts.forEach((a) => (a.last_sync_at = iso()));
      return {};
    }), 202);

  if (seg[0] === "instances" && seg[1]) {
    const i = db.instances.find((x) => x.id === seg[1]);
    if (!i) return fail(404, "not_found");
    if (method === "PATCH" && seg[2] === "metadata") {
      const u = b as T.InstanceMetadataUpdate;
      if (u.note !== undefined) i.note = u.note ?? "";
      if (u.group !== undefined) i.group = (u.group ?? "").trim();
      if (u.tags !== undefined) i.tags = (u.tags ?? []).map((t) => t.trim()).filter(Boolean);
      const updated_at = iso();
      i.updated_at = updated_at;
      return ok({ instance_id: i.id, note: i.note ?? "", group: i.group ?? "", tags: i.tags ?? [], updated_at });
    }
    if (method === "POST" && seg[2] === "power") {
      const action = b.action as "start" | "stop" | "reboot";
      const stopMode = b.stop_mode as "keep_charging" | "stop_charging" | undefined;
      if (action === "stop" && stopMode === "stop_charging" && !i.supports_stop_charging) return fail(422, "validation_failed", { fields: { stop_mode: "invalid_for_instance" } });
      i.status = action === "stop" ? "stopping" : "starting";
      return ok(startJob("instance_power", 1400, () => {
        i.status = action === "stop" ? "stopped" : "running";
        i.desired_state = i.status;
        i.desired_state_updated_at = iso();
        i.pending_intent_confirmation = null;
        return { result: { action, stop_mode: stopMode ?? null, target_status: i.status, stage: "confirmed" } };
      }, { account_ids: [i.account_id], instance_id: i.id }), 202);
    }
    if (seg[2] === "protection-exemption") {
      if (method === "PUT") {
        const secs = Number(b.duration_seconds) || 3600;
        const expires_at = iso(Date.now() + secs * 1000);
        i.quota_exemption_until = expires_at;
        return ok({ instance_id: i.id, expires_at });
      }
      if (method === "DELETE") {
        i.quota_exemption_until = null;
        return ok(undefined, 204);
      }
    }
    if (method === "POST" && seg[2] === "intent-confirmation") {
      const decision = b.decision as "keep_stopped" | "resume";
      i.pending_intent_confirmation = null;
      if (decision === "keep_stopped") {
        i.desired_state = "stopped";
        i.desired_state_updated_at = iso();
        return ok({ instance_id: i.id, decision, job_id: null });
      }
      const job = startJob("instance_power", 1400, () => {
        i.status = "running";
        i.desired_state = "running";
        i.desired_state_updated_at = iso();
        return { result: { action: "start", target_status: "running", stage: "confirmed" } };
      }, { account_ids: [i.account_id], instance_id: i.id, trigger: "panel" });
      return ok({ instance_id: i.id, decision, job_id: job.job_id });
    }
    if (method === "GET" && seg[2] === "agent-hourly-history") {
      const fromMs = Date.parse(q.get("from") ?? iso(Date.now() - 24 * 3600_000));
      const toMs = Date.parse(q.get("to") ?? iso());
      if (i.agent.status === "not_installed") return ok({ items: [], from: iso(fromMs), to: iso(toMs) });
      const startHour = Math.floor(fromMs / 3600_000) * 3600_000;
      const count = Math.min(200, Math.max(1, Math.floor((toMs - startHour) / 3600_000)));
      const items: T.AgentHourlyBucket[] = Array.from({ length: count }, (_, k) => {
        const hs = startHour + k * 3600_000;
        const wave = 0.5 + 0.4 * Math.sin(k / 3.5);
        const rx = Math.round(wave * 0.45 * GB);
        const tx = Math.round(wave * 0.52 * GB);
        return { hour_start: iso(hs), rx_bytes: rx, tx_bytes: tx, rx_bytes_per_second: Math.floor(rx / 3600), tx_bytes_per_second: Math.floor(tx / 3600), covered_seconds: 3600, sample_count: 360, estimated: true };
      });
      return ok({ items, from: iso(fromMs), to: iso(toMs) });
    }
    if (method === "GET" && seg[2] === "agent-network-history") {
      const fromMs = Date.parse(q.get("from") ?? iso(Date.now() - 24 * 3600_000));
      const toMs = Date.parse(q.get("to") ?? iso());
      if (i.agent.status === "not_installed") return ok({ items: [], next_cursor: null, from: iso(fromMs), to: iso(toMs) });
      const step = Math.max(600_000, Math.floor((toMs - fromMs) / 48));
      const items: T.AgentNetworkSample[] = Array.from({ length: 48 }, (_, k) => {
        const end = toMs - k * step;
        const wave = 0.5 + 0.4 * Math.cos(k / 4);
        const rxRate = Math.round(wave * 4_200_000);
        const txRate = Math.round(wave * 5_100_000);
        return {
          counter_epoch: "ep1",
          sequence: 1000 - k,
          sampled_at: iso(end),
          received_at: iso(end),
          uptime_ms: (1000 - k) * step,
          rx_bytes: (1000 - k) * rxRate * 10,
          tx_bytes: (1000 - k) * txRate * 10,
          quality: "ok",
          rules_version: "1",
          report_interval_seconds: 10,
          dropped_samples: 0,
          interval_status: "continuous",
          interval_unavailable_reason: null,
          interval: { previous_sequence: 999 - k, start_at: iso(end - step), end_at: iso(end), duration_ms: step, rx_bytes: rxRate * Math.round(step / 1000), tx_bytes: txRate * Math.round(step / 1000), rx_bytes_per_second: rxRate, tx_bytes_per_second: txRate },
        };
      });
      return ok({ items, next_cursor: null, from: iso(fromMs), to: iso(toMs) });
    }
    if (method === "GET" && seg[2] === "cloud-network-history") {
      const fromMs = Date.parse(q.get("from") ?? iso(Date.now() - 24 * 3600_000));
      const toMs = Date.parse(q.get("to") ?? iso());
      const step = Math.max(600_000, Math.floor((toMs - fromMs) / 36));
      const items: T.CloudNetworkSample[] = [];
      for (let k = 0; k < 36; k++) {
        const at = iso(toMs - k * step);
        const wave = 0.4 + 0.35 * Math.sin(k / 3);
        items.push(
          { source: i.kind === "swas" ? "swas_describe_monitor_data" : "cms", network_type: i.kind === "swas" ? "swas" : "vpc", ip: i.public_ips[0]?.address ?? null, direction: "egress", metric_name: "InternetOutRate", unit: "bit/s", period_seconds: 60, value: Math.round(wave * 28_000_000), sampled_at: at, checked_at: at },
          { source: i.kind === "swas" ? "swas_describe_monitor_data" : "cms", network_type: i.kind === "swas" ? "swas" : "vpc", ip: i.public_ips[0]?.address ?? null, direction: "ingress", metric_name: "InternetInRate", unit: "bit/s", period_seconds: 60, value: Math.round(wave * 19_000_000), sampled_at: at, checked_at: at },
        );
      }
      return ok({ items, next_cursor: null, from: iso(fromMs), to: iso(toMs) });
    }
    if (seg[2] === "agent") {
      if (method === "POST" && seg[3] === "install-command") {
        const code = Math.random().toString(36).slice(2, 10);
        setTimeout(() => {
          i.agent = { status: "online", version: "0.2.0", update_available: false, registered_at: iso(), last_report_at: iso(), offline_since: null, worst_case: null };
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
  }

  /* 流量与计费单价 */
  const byAccount = <X extends { account_id: string }>(xs: X[]) => xs.filter((x) => !q.get("account_id") || x.account_id === q.get("account_id"));
  if (is("GET", "/traffic/quotas")) {
    const tracked = db.quotas.filter((qt) => {
      const acc = db.accounts.find((a) => a.id === qt.account_id);
      return acc ? (qt.scope === "mainland" ? acc.track_mainland !== false : acc.track_overseas !== false) : true;
    });
    return ok({ items: q.get("account_id") === "all" ? mockQuotaTotals(db) : byAccount(tracked) });
  }
  if (is("GET", "/traffic/pricing")) return ok(db.pricing);
  if (is("PUT", "/traffic/pricing")) {
    const next = b as unknown as T.TrafficPricing;
    db.pricing = { ...next, revision: db.pricing.revision + 1, updated_at: iso() };
    for (const qt of db.quotas) {
      const acc = db.accounts.find((a) => a.id === qt.account_id);
      qt.overage_cost = computeOverageCost(qt, acc?.site ?? "china", db.pricing, qt.effective_bytes);
    }
    return ok(db.pricing);
  }
  if (is("GET", "/traffic/reconciliations")) {
    const items = db.reconciliations.filter((r) => (!q.get("account_id") || r.account_id === q.get("account_id")) && (!q.get("scope") || r.scope === q.get("scope")));
    return ok({ items, next_before: null });
  }
  if (is("GET", "/traffic/observations")) {
    const id = q.get("account_id");
    if (!db.accounts.some((a) => a.id === id)) return fail(404, "not_found");
    const known = (scope: T.QuotaScope, i: number) => { const v = db.quotas.find((v) => v.account_id === id && v.scope === scope)?.official_bytes; return v == null ? null : Math.round(v * (1 - i * 0.002)); };
    return ok({ items: Array.from({ length: 8 }, (_, i) => ({ id: String(8 - i), account_id: id, fetched_at: iso(Date.now() - i * 300_000), mainland_bytes: known("mainland", i), overseas_bytes: known("overseas", i), official_quality: "inferred", error: null })), next_cursor: null });
  }
  if (is("GET", "/traffic/daily")) {
    const d = q.get("account_id") === "all" ? mockDailyTotal(db) : db.daily[q.get("account_id") ?? ""];
    return d ? ok(d) : fail(404, "not_found");
  }
  if (is("GET", "/traffic/billing-subjects")) return ok({ items: byAccount(db.subjects) });
  if (is("GET", "/bandwidth-packages")) return ok({ items: db.packages });
  if (seg[0] === "bandwidth-packages" && seg[2] === "hourly") {
    const h = db.hourly[seg[1]];
    return h ? ok(h) : fail(404, "not_found");
  }

  /* 自动化与额度保护 */
  if (is("GET", "/protection/status")) return ok(db.protectionStatus);
  if (is("GET", "/protection/rules")) return ok({ items: db.protectionRules });
  if (is("POST", "/protection/rules")) {
    const inRule = b as unknown as T.QuotaProtectionRuleInput;
    const rule: T.QuotaProtectionRule = {
      ...inRule,
      id: "rule_" + Math.random().toString(36).slice(2, 8),
      revision: 1,
      reserve_seconds: inRule.reserve_seconds ?? 60,
      dry_run: inRule.dry_run ?? true,
      confirmed_self_instance_id: inRule.confirmed_self_instance_id ?? "",
      created_at: iso(),
      updated_at: iso(),
    };
    db.protectionRules.unshift(rule);
    return ok(rule, 201);
  }
  if (seg[0] === "protection" && seg[1] === "rules" && seg[2]) {
    const idx = db.protectionRules.findIndex((r) => r.id === seg[2]);
    if (idx < 0) return fail(404, "not_found");
    if (method === "PUT") {
      const inRule = b as unknown as T.QuotaProtectionRuleInput;
      db.protectionRules[idx] = {
        ...db.protectionRules[idx],
        ...inRule,
        revision: db.protectionRules[idx].revision + 1,
        updated_at: iso(),
      };
      return ok(db.protectionRules[idx]);
    }
    if (method === "DELETE") {
      db.protectionRules.splice(idx, 1);
      return ok(undefined, 204);
    }
  }
  if (is("GET", "/protection/simulations")) {
    db.simulations = db.simulations.filter((s) => Date.parse(s.expires_at) > Date.now());
    return ok({ items: db.simulations });
  }
  if (is("PUT", "/protection/simulations")) {
    const sim = b as unknown as T.QuotaSimulation;
    const idx = db.simulations.findIndex((s) => s.account_id === sim.account_id && s.scope === sim.scope && s.source_instance_id === sim.source_instance_id);
    const saved: T.QuotaSimulation = { ...sim, revision: (idx >= 0 ? db.simulations[idx].revision : sim.revision) + 1 };
    if (idx >= 0) db.simulations[idx] = saved;
    else db.simulations.push(saved);
    return ok(saved);
  }
  if (is("DELETE", "/protection/simulations")) {
    db.simulations = db.simulations.filter((s) => !(s.account_id === q.get("account_id") && s.scope === q.get("scope") && s.source_instance_id === (q.get("source_instance_id") ?? "")));
    return ok(undefined, 204);
  }
  if (is("GET", "/protection/decisions")) {
    const items = db.decisions.filter((d) => !q.get("rule_id") || d.rule_id === q.get("rule_id"));
    return ok({ items, next_before: null });
  }
  if (is("GET", "/automation/schedules")) return ok({ items: db.schedules });
  if (is("POST", "/automation/schedules")) {
    const inSch = b as unknown as T.PowerScheduleInput;
    const sch: T.PowerSchedule = {
      ...inSch,
      id: "sch_" + Math.random().toString(36).slice(2, 8),
      revision: 1,
      catch_up_minutes: inSch.catch_up_minutes ?? 15,
      activated_at: iso(),
      last_triggered_at: null,
      created_at: iso(),
      updated_at: iso(),
    };
    db.schedules.unshift(sch);
    return ok(sch, 201);
  }
  if (seg[0] === "automation" && seg[1] === "schedules" && seg[2]) {
    const idx = db.schedules.findIndex((s) => s.id === seg[2]);
    if (idx < 0) return fail(404, "not_found");
    if (method === "PUT") {
      const inSch = b as unknown as T.PowerScheduleInput;
      db.schedules[idx] = {
        ...db.schedules[idx],
        ...inSch,
        revision: db.schedules[idx].revision + 1,
        catch_up_minutes: inSch.catch_up_minutes ?? db.schedules[idx].catch_up_minutes,
        activated_at: inSch.enabled && !db.schedules[idx].enabled ? iso() : db.schedules[idx].activated_at,
        updated_at: iso(),
      };
      return ok(db.schedules[idx]);
    }
    if (method === "DELETE") {
      db.schedules.splice(idx, 1);
      return ok(undefined, 204);
    }
  }
  if (is("GET", "/automation/keepalive")) return ok(db.keepalive);
  if (is("PUT", "/automation/keepalive")) {
    const u = b as unknown as T.KeepaliveSettings;
    db.keepalive = { ...u, revision: db.keepalive.revision + 1, updated_at: iso() };
    return ok(db.keepalive);
  }
  if (is("GET", "/automation/monthly-reset")) return ok(db.monthlyReset);
  if (is("PUT", "/automation/monthly-reset")) {
    const u = b as unknown as T.MonthlyResetSettings;
    db.monthlyReset = { ...db.monthlyReset, ...u, revision: db.monthlyReset.revision + 1, updated_at: iso() };
    return ok(db.monthlyReset);
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
    const sameProxy = u.proxy_mode === db.telegram.proxy_mode && u.proxy_url === db.telegram.proxy_url;
    const authSet = u.proxy_mode !== "none" && (u.proxy_username !== undefined ? u.proxy_username !== "" : sameProxy && !!db.telegram.proxy_auth_set);
    db.telegram = { ...db.telegram, bot_token_set: true, chat_id: u.chat_id, proxy_mode: u.proxy_mode, proxy_url: u.proxy_mode === "none" ? "" : u.proxy_url, proxy_auth_set: authSet, status: "not_tested", last_error: null, last_success_at: null };
    return ok(db.telegram);
  }
  if (is("POST", "/notify/telegram/test")) {
    if (!db.telegram.bot_token_set) return fail(409, "telegram_not_configured");
    return ok(startJob("telegram_test", 1500, () => {
      if (scene === "stale") return { error: { code: "telegram_unreachable", message: "mock" } };
      const n: T.Notification = { id: "ntf_" + Date.now(), created_at: iso(), kind: "test", severity: "info", rule_key: null, title: "⚪ 测试消息", body: "如果你在 Telegram 里看到这条消息，说明通知已经配置好了。", account_id: null, instance_id: null, probe_plan_id: null, delivery_status: "sent" };
      db.notifications.unshift(n);
      db.telegram.status = "ok";
      db.telegram.last_error = null;
      db.telegram.last_success_at = iso();
      emit("notification.created", n);
      return {};
    }), 202);
  }
  if (is("GET", "/notify/telegram/control")) return ok(db.telegramControl);
  if (is("PUT", "/notify/telegram/control")) {
    const u = b as unknown as T.TelegramControlSettings;
    db.telegramControl = { revision: db.telegramControl.revision + 1, enabled: u.enabled, allowed_user_ids: u.allowed_user_ids, updated_at: iso() };
    return ok(db.telegramControl);
  }
  if (is("GET", "/notify/quiet-hours")) return ok(db.quietHours);
  if (is("PUT", "/notify/quiet-hours")) {
    const maint = (b.maintenance ?? null) as T.MaintenanceWindow | null;
    const maintActive = !!maint && Date.parse(maint.end_at) > Date.now();
    db.quietHours = {
      revision: db.quietHours.revision + 1,
      enabled: Boolean(b.enabled),
      start: String(b.start ?? "23:30"),
      end: String(b.end ?? "07:30"),
      timezone: db.settings.business_timezone,
      active: maintActive,
      active_reason: maintActive ? "maintenance" : null,
      maintenance: maint,
      updated_at: iso(),
    };
    return ok(db.quietHours);
  }
  if (is("GET", "/alert-rules")) {
    const items = q.get("include_reconciliation") === "true" ? db.rules : db.rules.filter((r) => r.key !== "cdt_estimation_deviation");
    return ok({ items });
  }
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
      { id: "instance_power", reviewed_on: "2026-11-12", verification: "draft", cloud_write_permissions: true, limitations: ["cdt_action_unverified", "resource_scope_not_account_bound"], document: doc(power) },
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

  /* ---------- M3 & M4 模拟接口 ---------- */
  if (is("GET", "/notify/email")) {
    return ok({ enabled: false, status: "not_configured", host: "", port: 587, tls_mode: "starttls", username: "", password_set: false, from_address: "", to_addresses: [], subject_prefix: "[Harbormaster]", last_success_at: null, last_error: null, revision: 1 } satisfies T.EmailChannel);
  }
  if (is("PUT", "/notify/email")) {
    return ok({ enabled: Boolean(b.enabled), status: "not_tested", host: String(b.host ?? ""), port: Number(b.port) || 587, tls_mode: (b.tls_mode as "starttls" | "implicit_tls" | "none") ?? "starttls", username: String(b.username ?? ""), password_set: true, from_address: String(b.from_address ?? ""), to_addresses: (b.to_addresses as string[]) ?? [], subject_prefix: String(b.subject_prefix ?? "[Harbormaster]"), last_success_at: null, last_error: null, revision: 2 } satisfies T.EmailChannel);
  }
  if (is("POST", "/notify/email/test")) return ok(startJob("email_test", 500, () => ({})), 202);
  if (is("GET", "/notify/webhook")) {
    return ok({ enabled: false, status: "not_configured", url: "", secret_set: false, header_name: "", header_value_set: false, last_success_at: null, last_error: null, revision: 1 } satisfies T.WebhookChannel);
  }
  if (is("PUT", "/notify/webhook")) {
    return ok({ enabled: Boolean(b.enabled), status: "not_tested", url: String(b.url ?? ""), secret_set: Boolean(b.secret), header_name: String(b.header_name ?? ""), header_value_set: Boolean(b.header_value), last_success_at: null, last_error: null, revision: 2 } satisfies T.WebhookChannel);
  }
  if (is("POST", "/notify/webhook/test")) return ok(startJob("webhook_test", 500, () => ({})), 202);
  if (is("GET", "/notify/daily-report")) {
    return ok({ enabled: false, time_of_day: "09:00", revision: 1, last_sent_date: null, updated_at: null } satisfies T.DailyReportSettings);
  }
  if (is("PUT", "/notify/daily-report")) {
    return ok({ enabled: Boolean(b.enabled), time_of_day: String(b.time_of_day ?? "09:00"), revision: Number(b.revision ?? 1) + 1, last_sent_date: null, updated_at: iso() } satisfies T.DailyReportSettings);
  }
  if (is("POST", "/notify/daily-report/send")) return ok({ notification_id: "ntf_daily_mock" }, 202);

  if (is("GET", "/automation/failovers")) return ok({ items: [] as T.FailoverRule[] });
  if (is("POST", "/automation/failovers") || (seg[0] === "automation" && seg[1] === "failovers" && seg[2] && method === "PUT")) {
    const out: T.FailoverRule = {
      id: seg[2] ?? "fo_mock_1",
      revision: Number(b.revision ?? 0) + 1,
      name: String(b.name ?? "主备容灾"),
      enabled: Boolean(b.enabled),
      dry_run: Boolean(b.dry_run),
      primary_instance_id: String(b.primary_instance_id ?? ""),
      standby_instance_id: String(b.standby_instance_id ?? ""),
      last_triggered_at: null,
      created_at: iso(),
      updated_at: iso(),
    };
    return ok(out, method === "POST" ? 201 : 200);
  }
  if (seg[0] === "automation" && seg[1] === "failovers" && seg[2] && method === "DELETE") return ok(undefined, 204);

  if (is("GET", "/idle-resources")) return ok({ items: [], sources: [], next_cursor: null } satisfies T.IdleResourcesPage);
  if (is("GET", "/system-events")) return ok({ items: [] as T.SystemEvent[], next_cursor: null });
  if (seg[0] === "instances" && seg[2] === "finance" && method === "GET") {
    const inst = db.instances.find((x) => x.id === seg[1]);
    if (!inst) return fail(404, "not_found");
    const cycle = new Date().toISOString().slice(0, 7);
    return ok({
      instance_id: inst.id,
      account_id: inst.account_id,
      enabled: true,
      billing_cycle: cycle,
      updated_at: iso(),
      bill: { status: "ok", fetched_at: iso(), last_attempt_at: iso(), last_error: null, data: { billing_cycle: cycle, items: [] } },
    } as unknown as T.InstanceFinance);
  }
  if (seg[0] === "instances" && seg[2] === "release-preview" && method === "GET") {
    const inst = db.instances.find((x) => x.id === seg[1]);
    if (!inst) return fail(404, "not_found");
    return ok({
      instance_id: inst.id,
      account_id: inst.account_id,
      status: inst.status === "stopped" ? "eligible" : "blocked",
      block_reasons: inst.status !== "stopped" ? ["instance_not_stopped"] : [],
      fetched_at: iso(),
      last_attempt_at: iso(),
      last_error: null,
      expires_at: iso(Date.now() + 300_000),
      preview_token: null,
      execution_enabled: false,
      impact: {
        cloud_instance_id: inst.cloud_instance_id,
        instance_name: inst.name,
        cloud_status: inst.status === "stopped" ? "Stopped" : "Running",
        instance_charge_type: "PostPaid",
        spot_strategy: "NoSpot",
        deletion_protection: false,
        locked: false,
        instance_data_action: "delete",
        fixed_public_ip_action: "release_if_present",
        manual_snapshots_action: "retain",
        eips_action: "retain",
        disks: [],
      },
    } satisfies T.ReleasePreview);
  }
  if (seg[0] === "instances" && seg[2] === "release" && method === "POST") return fail(503, "instance_release_disabled");
  if (seg[0] === "instances" && seg[2] === "change-eip" && method === "POST") return fail(503, "eip_manage_disabled");
  if (is("POST", "/instances/create")) return fail(503, "instance_create_disabled");
  if (is("GET", "/cloud/ecs-catalog")) {
    return ok({
      account_id: q.get("account_id") ?? "",
      region_id: q.get("region_id") ?? "ap-southeast-1",
      zones: [{ id: "ap-southeast-1a", name: "新加坡 可用区 A" }],
      images: [{ id: "debian_12_x64", name: "Debian 12.8 64位" }],
      instance_types: [{ id: "ecs.e-c1m1.large", cpu: 2, memory_gib: 2 }],
      vpcs: [{ id: "vpc-mock", name: "default-vpc", cidr_block: "172.16.0.0/12" }],
      vswitches: [{ id: "vsw-mock", vpc_id: "vpc-mock", zone_id: "ap-southeast-1a", name: "default-vsw", cidr_block: "172.16.0.0/24" }],
    } satisfies T.ECSCatalog);
  }

  /* M3 网络延迟与线路模拟 */
  if (is("GET", "/network/points")) {
    const pts: T.ProbePoint[] = [
      { id: "server", name: "面板服务端", point_kind: "server", instance_id: null, available: true, unavailable_reason: null, updated_at: iso(), trace: { available: true, unavailable_reason: null, methods: ["icmp", "tcp"], last_poll_at: iso() } },
      ...db.instances
        .filter((i) => i.agent.status === "online")
        .map((i) => ({
          id: "agent:" + i.id,
          name: i.name,
          point_kind: "agent" as const,
          instance_id: i.id,
          available: true,
          unavailable_reason: null,
          updated_at: iso(),
          trace: { available: true, unavailable_reason: null, methods: ["icmp", "tcp"] as ("icmp" | "tcp")[], last_poll_at: iso() },
        })),
    ];
    return ok({ items: pts, next_cursor: null });
  }
  if (is("GET", "/network/targets")) {
    return ok({
      items: [
        { id: "tgt_cf", revision: 1, name: "Cloudflare DNS", kind: "custom", host: "1.1.1.1", instance_id: null, tcp_port: null, enabled: true, created_at: iso(), updated_at: iso() },
      ] satisfies T.NetworkTarget[],
      next_cursor: null,
    });
  }
  if (is("POST", "/network/targets") || (seg[0] === "network" && seg[1] === "targets" && seg[2] && method === "PUT")) {
    return ok({
      id: seg[2] ?? "tgt_" + Math.random().toString(36).slice(2, 7),
      revision: Number(b.revision ?? 0) + 1,
      name: String(b.name ?? "目标"),
      kind: (b.kind as "custom" | "instance") ?? "custom",
      host: (b.host as string | null) ?? null,
      instance_id: (b.instance_id as string | null) ?? null,
      tcp_port: (b.tcp_port as number | null) ?? null,
      enabled: Boolean(b.enabled ?? true),
      created_at: iso(),
      updated_at: iso(),
    } satisfies T.NetworkTarget, method === "POST" ? 201 : 200);
  }
  if (seg[0] === "network" && seg[1] === "targets" && seg[2] && method === "DELETE") return ok(undefined, 204);
  if (is("GET", "/network/plans")) {
    const run: T.ProbeRun = {
      id: "run_1",
      plan_id: "plan_1",
      plan_revision: 1,
      target_id: "tgt_cf",
      target_revision: 1,
      point_kind: "server",
      instance_id: null,
      created_at: iso(),
      updated_at: iso(),
      status: "succeeded",
      skip_reason: null,
      job_id: "job_prb_1",
      request: { host: "1.1.1.1", port: 0, family: "ipv4", method: "icmp", count: 5, timeout_ms: 2000, interval_ms: 1000 },
      result: {
        started_at: iso(Date.now() - 5000),
        finished_at: iso(),
        address: "1.1.1.1",
        resolved_addresses: ["1.1.1.1"],
        samples: [{ sequence: 1, started_at: iso(), rtt_ms: 14.2, error_code: null }],
        summary: { attempts: 5, succeeded: 5, loss_percent: 0, minimum_ms: 13.8, maximum_ms: 15.1, average_ms: 14.3, jitter_ms: 0.4 },
      },
      error: null,
    };
    return ok({
      items: [
        { id: "plan_1", revision: 1, name: "面板 → Cloudflare", point_kind: "server", instance_id: null, target_id: "tgt_cf", family: "ipv4", method: "icmp", frequency_seconds: 300, count: 5, timeout_ms: 2000, interval_ms: 1000, enabled: true, created_at: iso(), updated_at: iso(), next_run_at: iso(Date.now() + 300_000), last_run: run },
      ] satisfies T.ProbePlan[],
      next_cursor: null,
    });
  }
  if (is("POST", "/network/plans") || (seg[0] === "network" && seg[1] === "plans" && seg[2] && method === "PUT")) {
    return ok({
      id: seg[2] ?? "plan_" + Math.random().toString(36).slice(2, 7),
      revision: Number(b.revision ?? 0) + 1,
      name: String(b.name ?? "测速组合"),
      point_kind: (b.point_kind as "server" | "agent") ?? "server",
      instance_id: (b.instance_id as string | null) ?? null,
      target_id: String(b.target_id ?? "tgt_cf"),
      family: (b.family as "ipv4" | "ipv6") ?? "ipv4",
      method: (b.method as "icmp" | "tcp") ?? "icmp",
      frequency_seconds: Number(b.frequency_seconds ?? 300),
      count: 5,
      timeout_ms: 2000,
      interval_ms: 1000,
      enabled: Boolean(b.enabled ?? true),
      created_at: iso(),
      updated_at: iso(),
      next_run_at: iso(Date.now() + 300_000),
      last_run: null,
    } satisfies T.ProbePlan, method === "POST" ? 201 : 200);
  }
  if (seg[0] === "network" && seg[1] === "plans" && seg[2] && method === "DELETE") return ok(undefined, 204);
  if (seg[0] === "network" && seg[1] === "plans" && seg[3] === "runs" && method === "GET") {
    const items: T.ProbeRun[] = Array.from({ length: 12 }, (_, idx) => ({
      id: `run_${idx}`,
      plan_id: seg[2],
      plan_revision: 1,
      target_id: "tgt_cf",
      target_revision: 1,
      point_kind: "server",
      instance_id: null,
      created_at: iso(Date.now() - idx * 3600_000),
      updated_at: iso(Date.now() - idx * 3600_000),
      status: "succeeded",
      skip_reason: null,
      job_id: `job_prb_${idx}`,
      request: { host: "1.1.1.1", port: 0, family: "ipv4", method: "icmp", count: 5, timeout_ms: 2000, interval_ms: 1000 },
      result: {
        started_at: iso(Date.now() - idx * 3600_000),
        finished_at: iso(Date.now() - idx * 3600_000),
        address: "1.1.1.1",
        resolved_addresses: ["1.1.1.1"],
        samples: [],
        summary: { attempts: 5, succeeded: 5, loss_percent: 0, minimum_ms: 13.5, maximum_ms: 16.0 + (idx % 3), average_ms: 14.2 + (idx % 3) * 0.5, jitter_ms: 0.4 },
      },
      error: null,
    }));
    return ok({ items, next_cursor: null });
  }
  if (seg[0] === "network" && seg[1] === "plans" && seg[3] === "trace-schedule") {
    return ok({ plan_id: seg[2], revision: 1, enabled: Boolean(b.enabled ?? false), notify_changes: Boolean(b.notify_changes ?? true), time_of_day: String(b.time_of_day ?? "03:00"), max_hops: 30, count: 3, timeout_ms: 1000, active: false, timezone: db.settings.business_timezone, updated_at: iso(), next_run_at: null } satisfies T.TraceSchedule);
  }
  if (seg[0] === "network" && seg[1] === "plans" && seg[3] === "trace-snapshots") {
    return ok({ items: [] as T.TraceSnapshot[], next_cursor: null });
  }
  if (is("GET", "/network/traces")) return ok({ items: [] as T.TraceRun[], next_cursor: null });
  if (is("POST", "/network/traces")) return ok({ job_id: "job_trc_1", trace_id: "trc_mock_1" } satisfies T.TraceAccepted, 202);
  if (seg[0] === "network" && seg[1] === "traces" && seg[2] && seg[3] === "export" && method === "GET") return ok("trace: trc_mock_1\nstatus: succeeded\n1 1 1.1.1.1 14.200ms echo_reply 0 0 -\n");
  if (seg[0] === "network" && seg[1] === "traces" && seg[2] && method === "GET") {
    return ok({
      id: seg[2],
      job_id: "job_trc_1",
      point_id: "server",
      point_name: "面板服务端",
      point_kind: "server",
      instance_id: null,
      target: null,
      request: { host: "1.1.1.1", family: "ipv4", method: "icmp", port: 0, max_hops: 30, count: 3, timeout_ms: 1000 },
      created_at: iso(),
      updated_at: iso(),
      status: "succeeded",
      enrichment_status: "complete",
      duplex_id: null,
      duplex_direction: null,
      outcome: { started_at: iso(), finished_at: iso(), address: "1.1.1.1", reached: true, stop_reason: "reached", hop_count: 1, sample_count: 3 },
      error: null,
      result: {
        started_at: iso(),
        finished_at: iso(),
        address: "1.1.1.1",
        resolved_addresses: ["1.1.1.1"],
        reached: true,
        stop_reason: "reached",
        hops: [{ ttl: 1, samples: [{ sequence: 1, started_at: iso(), address: "1.1.1.1", rtt_ms: 14.2, reply: "echo_reply", icmp_type: 0, icmp_code: 0, error_code: null }] }],
      },
      enrichment: null,
      enrichment_error: null,
    } satisfies T.TraceDetail);
  }
  if (is("GET", "/network/ipdb")) {
    const ds = { name: "dbip-asn-lite", edition: "2026-11", built_at: iso(), size_bytes: 4_200_000, origin: "bundled" as const, storage: "memory" as const, source_url: "https://db-ip.com" };
    return ok({ mode: "country", city_enabled: false, auto_update: false, asn: ds, country: { ...ds, name: "dbip-country-lite" }, city: null, last_check_at: null, last_update_at: null, last_error: null, attribution: [{ name: "IP Geolocation by DB-IP", url: "https://db-ip.com", license: "CC BY 4.0", license_url: "https://creativecommons.org/licenses/by/4.0/" }], go_heap_alloc_bytes: 16_777_216, process_peak_rss_bytes: null, mapped_file_bytes: 0 } satisfies T.IPDBStatus);
  }

  return fail(404, "not_found");
}

// 接口调用。真实模式走 fetch；模拟模式（VITE_API_MODE=mock）走 src/mocks，不发任何网络请求。
import type * as T from "./types";

export const MOCK = import.meta.env.VITE_API_MODE === "mock";
const BASE = "/api/v1";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public fields?: Record<string, string>,
    public retryAfterSeconds?: number,
  ) {
    super(message);
  }
}

let csrfToken = "";
let readOnly = false;
export function setReadOnly(value: boolean) { readOnly = value; }
let onUnauthenticated: () => void = () => {};
export function setUnauthenticatedHandler(fn: () => void) {
  onUnauthenticated = fn;
}

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

const PUBLIC_WRITES = new Set(["/session", "/setup", "/session/passkey/options", "/session/passkey/finish"]);

async function transport(method: Method, path: string, body?: unknown, extraHeaders?: Record<string, string>): Promise<{ status: number; body: unknown }> {
  if (readOnly && method !== "GET" && !PUBLIC_WRITES.has(path)) throw new ApiError(501, "not_implemented", "read only");
  if (import.meta.env.VITE_API_MODE === "mock") {
    const { handle } = await import("../mocks/server");
    return handle(method, path, body, extraHeaders);
  }
  const headers: Record<string, string> = { Accept: "application/json", ...extraHeaders };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrfToken) headers["X-CSRF-Token"] = csrfToken;
  let res: Response;
  try {
    res = await fetch(BASE + path, { method, headers, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError(0, "network_error", "network error");
  }
  let parsed: unknown = null;
  if (res.status !== 204) {
    try {
      parsed = await res.json();
    } catch {
      parsed = null;
    }
  }
  return { status: res.status, body: parsed };
}

async function request<R>(method: Method, path: string, body?: unknown, opts?: { quiet401?: boolean; headers?: Record<string, string> }): Promise<R> {
  const res = await transport(method, path, body, opts?.headers);
  if (res.status >= 200 && res.status < 300) return res.body as R;
  const err = (res.body as T.ErrorBody | null)?.error;
  if (res.status === 401 && !opts?.quiet401) onUnauthenticated();
  throw new ApiError(res.status, err?.code ?? (res.status >= 500 ? "internal_error" : "unknown_error"), err?.message ?? "", err?.fields, err?.retry_after_seconds);
}

const qs = (params: Record<string, string | number | boolean | undefined | null>) => {
  const p = Object.entries(params).filter(([, v]) => v !== undefined && v !== null && v !== "");
  return p.length ? "?" + p.map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join("&") : "";
};

const keepSession = (s: T.Session) => {
  csrfToken = s.csrf_token;
  return s;
};

export function randomIdempotencyKey(prefix = "pwr"): string {
  const rand = typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID().replace(/-/g, "") : Math.random().toString(36).slice(2) + Date.now().toString(36);
  return `${prefix}_${rand}`;
}

/** 下载加密备份：响应是二进制文件，不走 JSON 解析。 */
export async function downloadBackup(backup_password: string): Promise<{ blob: Blob; filename: string }> {
  if (readOnly) throw new ApiError(501, "not_implemented", "read only");
  if (import.meta.env.VITE_API_MODE === "mock") {
    const { handle } = await import("../mocks/server");
    const res = await handle("POST", "/backups", { backup_password });
    const err = (res.body as T.ErrorBody | null)?.error;
    throw new ApiError(res.status, err?.code ?? "internal_error", err?.message ?? "", err?.fields);
  }
  let res: Response;
  try {
    res = await fetch(BASE + "/backups", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken }, body: JSON.stringify({ backup_password }) });
  } catch {
    throw new ApiError(0, "network_error", "network error");
  }
  if (!res.ok) {
    const err = ((await res.json().catch(() => null)) as T.ErrorBody | null)?.error;
    if (res.status === 401) onUnauthenticated();
    throw new ApiError(res.status, err?.code ?? "internal_error", err?.message ?? "", err?.fields, err?.retry_after_seconds);
  }
  const name = /filename="?([^";]+)"?/.exec(res.headers.get("Content-Disposition") ?? "")?.[1] ?? "harbormaster.hmbak";
  return { blob: await res.blob(), filename: name };
}

/** 上传自定义面板图标（PNG / JPEG 二进制） */
export async function uploadBrandingLogo(revision: number, file: File): Promise<T.Branding> {
  if (readOnly) throw new ApiError(501, "not_implemented", "read only");
  const path = `/settings/branding/logo?revision=${encodeURIComponent(String(revision))}`;
  if (import.meta.env.VITE_API_MODE === "mock") {
    const { handle } = await import("../mocks/server");
    const res = await handle("PUT", path, { size: file.size, type: file.type });
    if (res.status >= 200 && res.status < 300) return res.body as T.Branding;
    const err = (res.body as T.ErrorBody | null)?.error;
    throw new ApiError(res.status, err?.code ?? "internal_error", err?.message ?? "", err?.fields);
  }
  let res: Response;
  try {
    res = await fetch(BASE + path, {
      method: "PUT",
      credentials: "same-origin",
      headers: { Accept: "application/json", "Content-Type": file.type || "application/octet-stream", ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}) },
      body: file,
    });
  } catch {
    throw new ApiError(0, "network_error", "network error");
  }
  const parsed = await res.json().catch(() => null);
  if (res.ok) return parsed as T.Branding;
  const err = (parsed as T.ErrorBody | null)?.error;
  if (res.status === 401) onUnauthenticated();
  throw new ApiError(res.status, err?.code ?? "internal_error", err?.message ?? "", err?.fields, err?.retry_after_seconds);
}

/** 下载纯文本导出（如单向/双向线路追踪报告） */
export async function downloadText(path: string, fallbackName: string): Promise<{ text: string; filename: string }> {
  if (import.meta.env.VITE_API_MODE === "mock") {
    const { handle } = await import("../mocks/server");
    const res = await handle("GET", path, undefined);
    if (res.status >= 200 && res.status < 300) {
      return { text: typeof res.body === "string" ? res.body : JSON.stringify(res.body, null, 2), filename: fallbackName };
    }
    const err = (res.body as T.ErrorBody | null)?.error;
    throw new ApiError(res.status, err?.code ?? "internal_error", err?.message ?? "");
  }
  let res: Response;
  try {
    res = await fetch(BASE + path, { method: "GET", credentials: "same-origin", headers: { Accept: "text/plain" } });
  } catch {
    throw new ApiError(0, "network_error", "network error");
  }
  if (!res.ok) {
    if (res.status === 401) onUnauthenticated();
    const err = ((await res.json().catch(() => null)) as T.ErrorBody | null)?.error;
    throw new ApiError(res.status, err?.code ?? "internal_error", err?.message ?? "");
  }
  const name = /filename="?([^";]+)"?/.exec(res.headers.get("Content-Disposition") ?? "")?.[1] ?? fallbackName;
  return { text: await res.text(), filename: name };
}

export const api = {
  finance: (account_id: string) => request<T.AccountFinance>("GET", `/accounts/${account_id}/finance`),
  instanceFinance: (instance_id: string) => request<T.InstanceFinance>("GET", `/instances/${instance_id}/finance`),
  idleResources: (p: { account_id?: string; region_id?: string; kind?: string; cursor?: string; limit?: number } = {}) =>
    request<T.IdleResourcesPage>("GET", "/idle-resources" + qs(p)),
  apiKeys: () => request<{ items: T.APIKey[]; next_cursor: string | null }>("GET", "/api-keys" + qs({ limit: 100 })).then((r) => r.items),
  createApiKey: (name: string, scopes: T.APIKeyScope[]) => request<{ api_key: T.APIKey; token: string }>("POST", "/api-keys", { name, scopes }),
  revokeApiKey: (id: string) => request<void>("DELETE", `/api-keys/${id}`),
  ramPolicies: () => request<{ items: T.RAMPolicyDraft[] }>("GET", "/ram-policies").then((r) => r.items),
  instanceEvents: (p: { instance_id?: string; cursor?: string; limit?: number } = {}) => request<{ items: T.InstanceEvent[]; next_cursor: string | null }>("GET", "/instance-events" + qs(p)),
  systemEvents: (p: { account_id?: string; instance_id?: string; category?: string; cursor?: string; limit?: number } = {}) =>
    request<{ items: T.SystemEvent[]; next_cursor: string | null }>("GET", "/system-events" + qs(p)),

  setupStatus: () => request<T.SetupStatus>("GET", "/setup/status"),
  completeSetup: (b: T.SetupRequest) => request<T.Session>("POST", "/setup", b, { quiet401: true }).then(keepSession),
  session: () => request<T.Session>("GET", "/session", undefined, { quiet401: true }).then(keepSession),
  loginMethods: () => request<T.LoginMethods>("GET", "/session/methods", undefined, { quiet401: true }),
  login: (password: string) => request<T.Session>("POST", "/session", { password }, { quiet401: true }).then(keepSession),
  beginPasskeyLogin: () => request<T.PasskeyOptions>("POST", "/session/passkey/options", {}, { quiet401: true }),
  finishPasskeyLogin: (ceremony_id: string, credential: Record<string, unknown>) =>
    request<T.Session>("POST", "/session/passkey/finish", { ceremony_id, credential }, { quiet401: true }).then(keepSession),
  logout: () => request<void>("DELETE", "/session"),
  setPasswordLogin: (enabled: boolean) => request<{ password_enabled: boolean }>("PATCH", "/session/password", { enabled }),
  passkeys: () => request<{ items: T.Passkey[] }>("GET", "/passkeys").then((r) => r.items),
  beginPasskeyRegistration: (name: string) => request<T.PasskeyOptions>("POST", "/passkeys/registration/options", { name }),
  finishPasskeyRegistration: (ceremony_id: string, credential: Record<string, unknown>) =>
    request<T.Passkey>("POST", "/passkeys/registration/finish", { ceremony_id, credential }),
  deletePasskey: (id: string) => request<void>("DELETE", `/passkeys/${id}`),

  systemInfo: () => request<T.SystemInfo>("GET", "/system/info"),
  versionCenter: () => request<T.VersionCenter>("GET", "/system/version-center"),
  checkLatestVersion: () => request<T.VersionCenter>("POST", "/system/version-center/check"),
  publicBranding: () => request<T.PublicBranding>("GET", "/public/branding", undefined, { quiet401: true }),
  branding: () => request<T.Branding>("GET", "/settings/branding"),
  updateBrandingName: (revision: number, name: string) => request<T.Branding>("PUT", "/settings/branding", { revision, name }),
  deleteBrandingLogo: (revision: number) => request<T.Branding>("DELETE", `/settings/branding/logo?revision=${encodeURIComponent(String(revision))}`),
  settings: () => request<T.Settings>("GET", "/settings"),
  updateSettings: (b: T.SettingsUpdate) => request<T.Settings>("PATCH", "/settings", b),
  heartbeat: () => request<T.HeartbeatSettings>("GET", "/settings/heartbeat"),
  updateHeartbeat: (b: { revision: number; enabled: boolean; interval_seconds: number; url?: string }) => request<T.HeartbeatSettings>("PUT", "/settings/heartbeat", b),
  testHeartbeat: () => request<T.JobAccepted>("POST", "/settings/heartbeat/test", {}),
  auditLog: (p: { actor?: string; action?: string; target_type?: string; account_id?: string; cursor?: string; limit?: number } = {}) =>
    request<{ items: T.AuditEntry[]; next_cursor: string | null }>("GET", "/audit-log" + qs(p)),

  regions: (site: T.AccountSite) => request<{ items: T.Region[] }>("GET", "/regions" + qs({ site })).then((r) => r.items),
  jobs: (p: { account_id?: string; instance_id?: string; type?: T.JobType; status?: T.JobStatus; trigger?: T.JobTrigger; cursor?: string; limit?: number } = {}) =>
    request<{ items: T.Job[]; next_cursor: string | null }>("GET", "/jobs" + qs(p)),
  job: (id: string) => request<T.Job>("GET", `/jobs/${id}`),

  accounts: () => request<{ items: T.Account[] }>("GET", "/accounts").then((r) => r.items),
  createAccount: (b: T.AccountCreate) => request<{ account: T.Account; permission_check_job_id: string }>("POST", "/accounts", b),
  updateAccount: (id: string, b: T.AccountUpdate) => request<T.Account>("PATCH", `/accounts/${id}`, b),
  deleteAccount: (id: string) => request<void>("DELETE", `/accounts/${id}`),
  checkPermissions: (id: string) => request<T.JobAccepted>("POST", `/accounts/${id}/permission-check`),

  instances: (p: { account_id?: string; kind?: T.InstanceKind; limit?: number; cursor?: string } = {}) =>
    request<{ items: T.Instance[]; next_cursor: string | null; updated_at: string | null }>("GET", "/instances" + qs(p)),
  refresh: (account_id?: string) => request<T.JobAccepted>("POST", "/instances/refresh", account_id ? { account_id } : {}),
  updateInstanceMetadata: (id: string, b: T.InstanceMetadataUpdate) =>
    request<{ instance_id: string; note: string; group: string; tags: string[]; updated_at: string }>("PATCH", `/instances/${id}/metadata`, b),
  controlInstancePower: (id: string, action: "start" | "stop" | "reboot", stop_mode?: "keep_charging" | "stop_charging", idempotencyKey = randomIdempotencyKey()) =>
    request<T.JobAccepted>("POST", `/instances/${id}/power`, stop_mode ? { action, stop_mode } : { action }, { headers: { "Idempotency-Key": idempotencyKey } }),
  setProtectionExemption: (id: string, duration_seconds: number) =>
    request<{ instance_id: string; expires_at: string }>("PUT", `/instances/${id}/protection-exemption`, { duration_seconds }),
  clearProtectionExemption: (id: string) => request<void>("DELETE", `/instances/${id}/protection-exemption`),
  confirmInstanceIntent: (id: string, decision: "keep_stopped" | "resume") =>
    request<{ instance_id: string; decision: string; job_id?: string | null }>("POST", `/instances/${id}/intent-confirmation`, { decision }),
  rotateInstanceEIP: (id: string, b: T.EIPRotateInput, idempotencyKey = randomIdempotencyKey("eip")) =>
    request<T.JobAccepted>("POST", `/instances/${id}/change-eip`, b, { headers: { "Idempotency-Key": idempotencyKey } }),
  releasePreview: (id: string) => request<T.ReleasePreview>("GET", `/instances/${id}/release-preview`),
  releaseInstance: (id: string, confirmation_name: string, preview_token: string, idempotencyKey = randomIdempotencyKey("rel")) =>
    request<T.JobAccepted>("POST", `/instances/${id}/release`, { confirmation_name, preview_token }, { headers: { "Idempotency-Key": idempotencyKey } }),
  ecsCatalog: (account_id: string, region_id: string) =>
    request<T.ECSCatalog>("GET", "/cloud/ecs-catalog" + qs({ account_id, region_id })),
  createECSInstance: (b: T.ECSCreateInput, idempotencyKey = randomIdempotencyKey("ecs")) =>
    request<T.JobAccepted>("POST", "/instances/create", b, { headers: { "Idempotency-Key": idempotencyKey } }),
  agentInstallCommand: (id: string) => request<T.AgentInstallCommand>("POST", `/instances/${id}/agent/install-command`),
  revokeAgentToken: (id: string) => request<void>("DELETE", `/instances/${id}/agent/token`),
  agentHourlyHistory: (id: string, p: { from: string; to: string; limit?: number }) =>
    request<T.AgentHourlyHistoryPage>("GET", `/instances/${id}/agent-hourly-history` + qs(p)),
  agentNetworkHistory: (id: string, p: { from: string; to: string; limit?: number; cursor?: string }) =>
    request<{ items: T.AgentNetworkSample[]; next_cursor: string | null; from: string; to: string }>("GET", `/instances/${id}/agent-network-history` + qs(p)),
  cloudNetworkHistory: (id: string, p: { from: string; to: string; direction?: "ingress" | "egress"; limit?: number; cursor?: string }) =>
    request<{ items: T.CloudNetworkSample[]; next_cursor: string | null; from: string; to: string }>("GET", `/instances/${id}/cloud-network-history` + qs(p)),

  quotas: (account_id?: string) => request<{ items: T.QuotaStatus[] }>("GET", "/traffic/quotas" + qs({ account_id })).then((r) => r.items),
  dailyTraffic: (account_id: string) => request<T.DailyTraffic>("GET", "/traffic/daily" + qs({ account_id })),
  trafficObservations: (account_id: string, cursor?: string) => request<{ items: T.TrafficObservation[]; next_cursor: string | null }>("GET", "/traffic/observations" + qs({ account_id, cursor, limit: 100 })),
  trafficReconciliations: (p: { account_id?: string; scope?: T.QuotaScope; before?: number; limit?: number } = {}) =>
    request<{ items: T.CDTReconciliation[]; next_before: number | null }>("GET", "/traffic/reconciliations" + qs(p)),
  trafficPricing: () => request<T.TrafficPricing>("GET", "/traffic/pricing"),
  updateTrafficPricing: (b: T.TrafficPricing) => request<T.TrafficPricing>("PUT", "/traffic/pricing", b),
  billingSubjects: (account_id?: string) => request<{ items: T.BillingSubject[] }>("GET", "/traffic/billing-subjects" + qs({ account_id })).then((r) => r.items),
  bandwidthPackages: () => request<{ items: T.BandwidthPackage[] }>("GET", "/bandwidth-packages").then((r) => r.items),
  bandwidthPackageHourly: (id: string) => request<T.BandwidthPackageHourly>("GET", `/bandwidth-packages/${id}/hourly`),

  protectionStatus: () => request<T.ProtectionStatus>("GET", "/protection/status"),
  protectionRules: () => request<{ items: T.QuotaProtectionRule[] }>("GET", "/protection/rules").then((r) => r.items),
  createProtectionRule: (b: T.QuotaProtectionRuleInput) => request<T.QuotaProtectionRule>("POST", "/protection/rules", b),
  updateProtectionRule: (id: string, b: T.QuotaProtectionRuleInput) => request<T.QuotaProtectionRule>("PUT", `/protection/rules/${id}`, b),
  deleteProtectionRule: (id: string, revision: number) => request<void>("DELETE", `/protection/rules/${id}` + qs({ revision })),
  quotaSimulations: () => request<{ items: T.QuotaSimulation[] }>("GET", "/protection/simulations").then((r) => r.items),
  setQuotaSimulation: (b: T.QuotaSimulation) => request<T.QuotaSimulation>("PUT", "/protection/simulations", b),
  clearQuotaSimulation: (p: { account_id: string; scope: "mainland" | "overseas" | "swas"; source_instance_id?: string; revision: number }) =>
    request<void>("DELETE", "/protection/simulations" + qs(p)),
  quotaDecisions: (p: { rule_id?: string; before?: number; limit?: number } = {}) =>
    request<{ items: T.QuotaDecision[]; next_before: number | null }>("GET", "/protection/decisions" + qs(p)),

  powerSchedules: () => request<{ items: T.PowerSchedule[] }>("GET", "/automation/schedules").then((r) => r.items),
  createPowerSchedule: (b: T.PowerScheduleInput) => request<T.PowerSchedule>("POST", "/automation/schedules", b),
  updatePowerSchedule: (id: string, b: T.PowerScheduleInput) => request<T.PowerSchedule>("PUT", `/automation/schedules/${id}`, b),
  deletePowerSchedule: (id: string, revision: number) => request<void>("DELETE", `/automation/schedules/${id}` + qs({ revision })),
  keepaliveSettings: () => request<T.KeepaliveSettings>("GET", "/automation/keepalive"),
  updateKeepaliveSettings: (b: T.KeepaliveSettings) => request<T.KeepaliveSettings>("PUT", "/automation/keepalive", b),
  monthlyResetSettings: () => request<T.MonthlyResetSettings>("GET", "/automation/monthly-reset"),
  updateMonthlyResetSettings: (b: T.MonthlyResetSettings) => request<T.MonthlyResetSettings>("PUT", "/automation/monthly-reset", b),
  failoverRules: () => request<{ items: T.FailoverRule[] }>("GET", "/automation/failovers").then((r) => r.items),
  createFailoverRule: (b: T.FailoverRuleInput) => request<T.FailoverRule>("POST", "/automation/failovers", b),
  updateFailoverRule: (id: string, b: T.FailoverRuleInput) => request<T.FailoverRule>("PUT", `/automation/failovers/${id}`, b),
  deleteFailoverRule: (id: string, revision: number) => request<void>("DELETE", `/automation/failovers/${id}` + qs({ revision })),

  // M3 网络延迟与线路追踪
  networkPoints: () => request<{ items: T.ProbePoint[]; next_cursor: string | null }>("GET", "/network/points" + qs({ limit: 200 })).then((r) => r.items),
  networkTargets: () => request<{ items: T.NetworkTarget[]; next_cursor: string | null }>("GET", "/network/targets" + qs({ limit: 200 })).then((r) => r.items),
  createNetworkTarget: (b: T.NetworkTargetInput) => request<T.NetworkTarget>("POST", "/network/targets", b),
  updateNetworkTarget: (id: string, b: T.NetworkTargetInput) => request<T.NetworkTarget>("PUT", `/network/targets/${id}`, b),
  deleteNetworkTarget: (id: string, revision: number) => request<void>("DELETE", `/network/targets/${id}`, { revision }),
  networkPlans: () => request<{ items: T.ProbePlan[]; next_cursor: string | null }>("GET", "/network/plans" + qs({ limit: 200 })).then((r) => r.items),
  createNetworkPlan: (b: T.ProbePlanInput) => request<T.ProbePlan>("POST", "/network/plans", b),
  updateNetworkPlan: (id: string, b: T.ProbePlanInput) => request<T.ProbePlan>("PUT", `/network/plans/${id}`, b),
  deleteNetworkPlan: (id: string, revision: number) => request<void>("DELETE", `/network/plans/${id}`, { revision }),
  probeRuns: (planId: string, p: { from: string; to: string; limit?: number; cursor?: string }) =>
    request<{ items: T.ProbeRun[]; next_cursor: string | null }>("GET", `/network/plans/${planId}/runs` + qs(p)),
  traceSchedule: (planId: string) => request<T.TraceSchedule>("GET", `/network/plans/${planId}/trace-schedule`),
  saveTraceSchedule: (planId: string, b: T.TraceScheduleInput) => request<T.TraceSchedule>("PUT", `/network/plans/${planId}/trace-schedule`, b),
  traceSnapshots: (planId: string, p: { from: string; to: string; limit?: number; cursor?: string }) =>
    request<{ items: T.TraceSnapshot[]; next_cursor: string | null }>("GET", `/network/plans/${planId}/trace-snapshots` + qs(p)),
  networkTraces: (p: { from: string; to: string; point_id?: string; target_id?: string; limit?: number; cursor?: string }) =>
    request<{ items: T.TraceRun[]; next_cursor: string | null }>("GET", "/network/traces" + qs(p)),
  submitTrace: (b: T.TraceSubmission) => request<T.TraceAccepted>("POST", "/network/traces", b),
  networkTrace: (id: string) => request<T.TraceDetail>("GET", `/network/traces/${id}`),
  submitDuplexTrace: (b: T.DuplexTraceSubmission) => request<T.DuplexTraceAccepted>("POST", "/network/duplex-traces", b),
  duplexTrace: (id: string) => request<T.DuplexTrace>("GET", `/network/duplex-traces/${id}`),
  networkIPDB: () => request<T.IPDBStatus>("GET", "/network/ipdb"),

  telegram: () => request<T.TelegramChannel>("GET", "/notify/telegram"),
  updateTelegram: (b: T.TelegramChannelUpdate) => request<T.TelegramChannel>("PUT", "/notify/telegram", b),
  testTelegram: () => request<T.JobAccepted>("POST", "/notify/telegram/test"),
  emailChannel: () => request<T.EmailChannel>("GET", "/notify/email"),
  updateEmailChannel: (b: T.EmailChannelUpdate) => request<T.EmailChannel>("PUT", "/notify/email", b),
  testEmailChannel: () => request<T.JobAccepted>("POST", "/notify/email/test"),
  webhookChannel: () => request<T.WebhookChannel>("GET", "/notify/webhook"),
  updateWebhookChannel: (b: T.WebhookChannelUpdate) => request<T.WebhookChannel>("PUT", "/notify/webhook", b),
  testWebhookChannel: () => request<T.JobAccepted>("POST", "/notify/webhook/test"),
  dailyReport: () => request<T.DailyReportSettings>("GET", "/notify/daily-report"),
  updateDailyReport: (b: { revision: number; enabled: boolean; time_of_day: string }) => request<T.DailyReportSettings>("PUT", "/notify/daily-report", b),
  sendDailyReportNow: () => request<{ notification_id: string }>("POST", "/notify/daily-report/send"),
  telegramControl: () => request<T.TelegramControlSettings>("GET", "/notify/telegram/control"),
  updateTelegramControl: (b: T.TelegramControlSettings) => request<T.TelegramControlSettings>("PUT", "/notify/telegram/control", b),
  quietHours: () => request<T.QuietHours>("GET", "/notify/quiet-hours"),
  updateQuietHours: (b: { revision: number; enabled: boolean; start: string; end: string; maintenance: T.MaintenanceWindow | null }) =>
    request<T.QuietHours>("PUT", "/notify/quiet-hours", b),
  alertRules: (includeReconciliation = true) =>
    request<{ items: T.AlertRule[] }>("GET", "/alert-rules" + qs({ include_reconciliation: includeReconciliation ? "true" : undefined })).then((r) => r.items),
  updateAlertRule: (key: T.AlertRuleKey, b: T.AlertRuleUpdate) => request<T.AlertRule>("PATCH", `/alert-rules/${key}`, b),
  notifications: (p: { limit?: number; cursor?: string } = {}) =>
    request<{ items: T.Notification[]; next_cursor: string | null }>("GET", "/notifications" + qs(p)),
};

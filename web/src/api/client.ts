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

async function transport(method: Method, path: string, body?: unknown): Promise<{ status: number; body: unknown }> {
  if (readOnly && method !== "GET" && path !== "/session" && path !== "/setup") throw new ApiError(501, "not_implemented", "read only");
  if (import.meta.env.VITE_API_MODE === "mock") {
    const { handle } = await import("../mocks/server");
    return handle(method, path, body);
  }
  const headers: Record<string, string> = { Accept: "application/json" };
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

async function request<R>(method: Method, path: string, body?: unknown, opts?: { quiet401?: boolean }): Promise<R> {
  const res = await transport(method, path, body);
  if (res.status >= 200 && res.status < 300) return res.body as R;
  const err = (res.body as T.ErrorBody | null)?.error;
  if (res.status === 401 && !opts?.quiet401) onUnauthenticated();
  throw new ApiError(res.status, err?.code ?? (res.status >= 500 ? "internal_error" : "unknown_error"), err?.message ?? "", err?.fields, err?.retry_after_seconds);
}

const qs = (params: Record<string, string | number | undefined | null>) => {
  const p = Object.entries(params).filter(([, v]) => v !== undefined && v !== null && v !== "");
  return p.length ? "?" + p.map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`).join("&") : "";
};

const keepSession = (s: T.Session) => {
  csrfToken = s.csrf_token;
  return s;
};

export const api = {
  setupStatus: () => request<T.SetupStatus>("GET", "/setup/status"),
  completeSetup: (b: T.SetupRequest) => request<T.Session>("POST", "/setup", b, { quiet401: true }).then(keepSession),
  session: () => request<T.Session>("GET", "/session", undefined, { quiet401: true }).then(keepSession),
  login: (password: string) => request<T.Session>("POST", "/session", { password }, { quiet401: true }).then(keepSession),
  logout: () => request<void>("DELETE", "/session"),

  systemInfo: () => request<T.SystemInfo>("GET", "/system/info"),
  settings: () => request<T.Settings>("GET", "/settings"),
  updateSettings: (b: { business_timezone?: string }) => request<T.Settings>("PATCH", "/settings", b),
  regions: (site: T.AccountSite) => request<{ items: T.Region[] }>("GET", "/regions" + qs({ site })).then((r) => r.items),
  job: (id: string) => request<T.Job>("GET", `/jobs/${id}`),

  accounts: () => request<{ items: T.Account[] }>("GET", "/accounts").then((r) => r.items),
  createAccount: (b: T.AccountCreate) => request<{ account: T.Account; permission_check_job_id: string }>("POST", "/accounts", b),
  updateAccount: (id: string, b: T.AccountUpdate) => request<T.Account>("PATCH", `/accounts/${id}`, b),
  deleteAccount: (id: string) => request<void>("DELETE", `/accounts/${id}`),
  checkPermissions: (id: string) => request<T.JobAccepted>("POST", `/accounts/${id}/permission-check`),

  instances: (p: { account_id?: string; kind?: T.InstanceKind; limit?: number; cursor?: string } = {}) =>
    request<{ items: T.Instance[]; next_cursor: string | null; updated_at: string | null }>("GET", "/instances" + qs(p)),
  refresh: (account_id?: string) => request<T.JobAccepted>("POST", "/instances/refresh", account_id ? { account_id } : {}),
  agentInstallCommand: (id: string) => request<T.AgentInstallCommand>("POST", `/instances/${id}/agent/install-command`),
  revokeAgentToken: (id: string) => request<void>("DELETE", `/instances/${id}/agent/token`),

  quotas: (account_id?: string) => request<{ items: T.QuotaStatus[] }>("GET", "/traffic/quotas" + qs({ account_id })).then((r) => r.items),
  dailyTraffic: (account_id: string) => request<T.DailyTraffic>("GET", "/traffic/daily" + qs({ account_id })),
  trafficObservations: (account_id: string, cursor?: string) => request<{ items: T.TrafficObservation[]; next_cursor: string | null }>("GET", "/traffic/observations" + qs({ account_id, cursor, limit: 100 })),
  billingSubjects: (account_id?: string) => request<{ items: T.BillingSubject[] }>("GET", "/traffic/billing-subjects" + qs({ account_id })).then((r) => r.items),
  bandwidthPackages: () => request<{ items: T.BandwidthPackage[] }>("GET", "/bandwidth-packages").then((r) => r.items),
  bandwidthPackageHourly: (id: string) => request<T.BandwidthPackageHourly>("GET", `/bandwidth-packages/${id}/hourly`),

  telegram: () => request<T.TelegramChannel>("GET", "/notify/telegram"),
  updateTelegram: (b: T.TelegramChannelUpdate) => request<T.TelegramChannel>("PUT", "/notify/telegram", b),
  testTelegram: () => request<T.JobAccepted>("POST", "/notify/telegram/test"),
  alertRules: () => request<{ items: T.AlertRule[] }>("GET", "/alert-rules").then((r) => r.items),
  updateAlertRule: (key: T.AlertRuleKey, b: T.AlertRuleUpdate) => request<T.AlertRule>("PATCH", `/alert-rules/${key}`, b),
  notifications: (p: { limit?: number; cursor?: string } = {}) =>
    request<{ items: T.Notification[]; next_cursor: string | null }>("GET", "/notifications" + qs(p)),
};

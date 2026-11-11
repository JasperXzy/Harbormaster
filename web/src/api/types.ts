// 契约类型的别名。schema.gen.ts 由 `npm run gen:api` 从 docs/api/openapi.yaml 生成，不要手改。
import type { components } from "./schema.gen";

type S = components["schemas"];

export type ErrorBody = S["ErrorResponse"];
export type Job = S["Job"];
export type JobAccepted = S["JobAccepted"];
export type SetupStatus = S["SetupStatus"];
export type SetupRequest = S["SetupRequest"];
export type Session = S["Session"];
export type SystemInfo = S["SystemInfo"];
export type Settings = S["Settings"];
export type Region = S["Region"];
export type Account = S["Account"];
export type AccountCreate = S["AccountCreate"];
export type AccountUpdate = S["AccountUpdate"];
export type AccountSite = S["AccountSite"];
export type PermissionCheck = S["PermissionCheck"];
export type PermissionKey = S["PermissionKey"];
export type PermissionResult = S["PermissionResult"];
export type Instance = S["Instance"];
export type InstanceKind = S["InstanceKind"];
export type InstanceStatus = S["InstanceStatus"];
export type Agent = S["Agent"];
export type AgentStatus = S["AgentStatus"];
export type InstanceRealtime = S["InstanceRealtime"];
export type AgentInstallCommand = S["AgentInstallCommand"];
export type RealtimeProtection = S["RealtimeProtection"];
export type BillingSubjectKind = S["BillingSubjectKind"];
export type MeteringRule = S["MeteringRule"];
export type QuotaScope = S["QuotaScope"];
export type QuotaStatus = S["QuotaStatus"];
export type DailyTraffic = S["DailyTraffic"];
export type BillingSubject = S["BillingSubject"];
export type BandwidthPackage = S["BandwidthPackage"];
export type BandwidthPackageHourly = S["BandwidthPackageHourly"];
export type TelegramChannel = S["TelegramChannel"];
export type TelegramChannelUpdate = S["TelegramChannelUpdate"];
export type AlertRule = S["AlertRule"];
export type AlertRuleKey = S["AlertRuleKey"];
export type AlertRuleUpdate = S["AlertRuleUpdate"];
export type Notification = S["Notification"];
export type Severity = S["Severity"];
export type InstanceRealtimeEvent = S["InstanceRealtimeEvent"];
export type AgentStatusEvent = S["AgentStatusEvent"];

export interface EventMap {
  "quota.updated": QuotaStatus;
  "instance.realtime": InstanceRealtimeEvent;
  "agent.status": AgentStatusEvent;
  "job.updated": Job;
  "notification.created": Notification;
  ping: Record<string, never>;
}
export type EventName = keyof EventMap;

// 英文枚举 -> 中文显示。接口只返回枚举，文案都在这里。
import type * as T from "./types";
import type { Level } from "../components/ui";

export const SITE: Record<T.AccountSite, string> = { china: "国内站", international: "国际站" };
export const KIND: Record<T.InstanceKind, string> = { ecs: "云服务器", swas: "轻量服务器" };
export const KIND_SHORT: Record<T.InstanceKind, string> = { ecs: "ECS", swas: "轻量" };
export const SCOPE: Record<T.QuotaScope, string> = { overseas: "非中国内地", mainland: "中国内地" };
export const SCOPE_SHORT: Record<T.QuotaScope, string> = { overseas: "海外", mainland: "国内" };

export const INSTANCE_STATUS: Record<T.InstanceStatus, [Level, string]> = {
  running: ["good", "运行中"],
  stopped: ["neutral", "已停止"],
  starting: ["neutral", "正在开机"],
  stopping: ["neutral", "正在关机"],
  pending: ["neutral", "准备中"],
  unknown: ["warning", "状态未知"],
};

export const CHARGE: Record<T.Instance["charge_type"], string> = { subscription: "包年包月", pay_as_you_go: "按量付费", spot: "抢占式", unknown: "计费方式待确认" };

export const SUBJECT_KIND: Record<T.BillingSubjectKind, string> = {
  unknown: "公网计费类型待确认",
  ecs_pay_by_traffic: "实例公网 IP · 按流量",
  eip_pay_by_traffic: "弹性公网 IP · 按流量",
  eip_fixed_bandwidth: "弹性公网 IP · 固定带宽",
  cbwp_dominant_traffic: "共享带宽 · 按主流量",
  cbwp_bandwidth: "共享带宽 · 按带宽",
  cbwp_95: "共享带宽 · 按 95 峰值",
  ecs_fixed_bandwidth: "实例公网 IP · 固定带宽",
  swas_traffic_package: "轻量服务器 · 套餐流量包",
  swas_no_traffic_package: "轻量服务器 · 无流量包",
};

export const RULE: Record<T.MeteringRule, string> = {
  unknown: "计量规则待确认",
  outbound_only: "只算发出的流量",
  max_in_out_per_period: "每小时取收、发中较大的一方",
  plan_defined: "按套餐规则",
  not_metered: "不按流量计费",
};

export const AGENT: Record<T.AgentStatus, [Level, string]> = {
  not_installed: ["neutral", "未安装"],
  online: ["good", "在线"],
  offline: ["critical", "已掉线"],
};

export const PROTECTION: Record<T.RealtimeProtection, [Level, string, string]> = {
  protected: ["good", "实时保护中", "采集程序每 10 秒上报一次，用量越线时能立刻发现。"],
  worst_case: ["critical", "按最坏情况推算", "采集程序掉线了。掉线期间按它之前的最高速度估算用量，宁可多算。"],
  unprotected: ["serious", "未受实时保护", "采集数据或官方计量依据尚未完整。请核对采集程序状态和官方数据截止时间。"],
  not_applicable: ["neutral", "不需要", "这台机器的流量不计入 CDT 免费额度。"],
};

/** 实例的实时保护状态。采集程序在线但额度暂时算不出时，不说“未受保护”，以免和没装的机器混在一起。 */
export function protectionOf(i: Pick<T.Instance, "realtime_protection" | "agent">): [Level, string, string] {
  if (i.realtime_protection === "unprotected" && i.agent.status === "online")
    return ["warning", "暂无法估算", "采集程序在正常上报，但阿里云官方数据的统计时间还不明确，暂时没法把两者加在一起算出实时用量。"];
  return PROTECTION[i.realtime_protection];
}

export const PERMISSION: Record<T.PermissionKey, string> = {
  ecs_read: "查看云服务器",
  swas_read: "查看轻量服务器",
  cdt_read: "查看 CDT 流量",
  cms_read: "查看云监控",
  instance_power: "开关机",
  vpc_read: "查看网络",
  eip_manage: "管理弹性公网 IP",
  bss_read: "查看账单",
};
export const PERMISSION_RESULT: Record<T.PermissionResult, [Level, string]> = {
  passed: ["good", "通过"],
  missing: ["serious", "缺少权限"],
  not_verified: ["neutral", "未验证"],
  check_failed: ["critical", "检查失败"],
};

export const SEVERITY: Record<T.Severity, [Level, string]> = {
  info: ["good", "信息"],
  warning: ["warning", "警告"],
  serious: ["serious", "较重"],
  critical: ["critical", "严重"],
};

export const DELIVERY: Record<T.Notification["delivery_status"], [Level, string]> = {
  sent: ["good", "已发送"],
  pending: ["neutral", "等待发送"],
  failed: ["critical", "发送失败"],
  not_sent: ["neutral", "仅记录"],
};

export const ALERT_RULE: Record<T.AlertRuleKey, { title: string; desc: string }> = {
  cdt_usage: { title: "免费额度用量", desc: "国内或海外用量每越过一个档位通知一次；同一档位每个计费周期只通知一次。" },
  cdt_forecast_exceed: { title: "预计月底超额", desc: "按最近 7 天的日均用量推算，月底前会超出免费额度时通知。" },
  account_api_failure: { title: "账号连接失败", desc: "连续多次调用阿里云接口失败时通知，通常是 AccessKey 失效或权限被改。" },
  agent_offline: { title: "采集程序掉线", desc: "实例上的采集程序停止上报时立刻通知，并开始按最坏情况推算用量。" },
  cdt_official_unavailable: { title: "官方数据取不到", desc: "连续多次取不到 CDT 官方数据时通知；期间沿用最近一次官方值继续估算。" },
};

// 常见地域的中文名；不认识的直接显示地域 ID。
const REGION: Record<string, string> = {
  "cn-hangzhou": "杭州", "cn-shanghai": "上海", "cn-beijing": "北京", "cn-shenzhen": "深圳", "cn-qingdao": "青岛",
  "cn-zhangjiakou": "张家口", "cn-huhehaote": "呼和浩特", "cn-wulanchabu": "乌兰察布", "cn-chengdu": "成都", "cn-guangzhou": "广州",
  "cn-heyuan": "河源", "cn-nanjing": "南京", "cn-fuzhou": "福州", "cn-wuhan-lr": "武汉", "cn-hongkong": "香港",
  "ap-southeast-1": "新加坡", "ap-southeast-3": "吉隆坡", "ap-southeast-5": "雅加达", "ap-southeast-6": "马尼拉", "ap-southeast-7": "曼谷",
  "ap-northeast-1": "东京", "ap-northeast-2": "首尔", "us-west-1": "硅谷", "us-east-1": "弗吉尼亚", "eu-central-1": "法兰克福",
  "eu-west-1": "伦敦", "me-east-1": "迪拜", "me-central-1": "利雅得",
};
export const regionName = (id: string) => REGION[id] ?? id;

// 错误码 -> 给用户看的话
const ERROR: Record<string, string> = {
  not_implemented: "当前版本尚未开放这个功能。",
  network_error: "连不上服务器。请检查网络，或确认 hm-server 正在运行。",
  internal_error: "服务器出错了，请稍后再试。",
  unauthenticated: "登录已过期，请重新登录。",
  forbidden: "没有权限执行这个操作。",
  csrf_failed: "请求来源或会话校验未通过。请从面板的正式地址重新打开页面。",
  not_found: "要找的内容不存在，可能已被删除。",
  validation_failed: "填写的内容有问题，请检查标红的项目。",
  rate_limited: "尝试次数太多，请稍后再试。",
  invalid_setup_token: "初始化凭证不正确或已过期。请检查部署时提供的凭证；过期后需更换凭证并重启服务。",
  already_initialized: "已经完成过首次设置，请直接登录。",
  invalid_credentials: "密码不正确。",
  account_name_taken: "已经有同名的账号了，请换一个名称。",
  account_disabled: "这个账号已停用。重新启用后才能刷新或测试权限。",
  account_changed: "账号配置已修改，旧任务已停止；启用的账号会按新配置重新读取。",
  collection_failed: "这次读取没有完成，已保留上一次的数据。",
  permission_denied: "阿里云拒绝了读取请求，请检查账号的只读权限。",
  aliyun_throttled: "阿里云接口暂时限流，任务会稍后重试。",
  retry_exhausted: "任务多次中断，已停止重试。请检查服务状态后重新刷新。",
  cdt_line_unverified: "CDT 返回了尚未核实的线路类型，暂不将它计入额度合计。",
  cdt_response_unrecognized: "CDT 返回格式未能确认，已保留旧记录，请检查数据来源。",
  cdt_value_out_of_range: "CDT 返回值超出可准确处理的范围，暂不用于估算。",
  cdt_international_endpoint_unverified: "这个站点的 CDT 查询端点尚未验证，暂不能提供官方用量。",
  access_key_invalid: "阿里云不认这组 AccessKey，请检查是否复制完整、是否已被禁用。",
  agent_not_supported: "这台实例暂时不能安装采集程序。",
  telegram_not_configured: "还没有保存 Telegram 设置。",
  telegram_unauthorized: "Bot Token 不正确，Telegram 拒绝了请求。",
  telegram_chat_not_found: "找不到这个 Chat ID。请确认机器人已被加入该对话。",
  telegram_unreachable: "连不上 Telegram。如果服务器在中国内地，需要设置代理。",
  aliyun_unreachable: "连不上阿里云接口，请稍后再试。",
  job_timeout: "操作等待超时，结果未知。请稍后刷新查看。",
};
export const errorText = (code: string) => ERROR[code] ?? `操作没有成功（${code}）。`;

const FIELD_ERROR: Record<string, string> = {
  required: "这一项必须填写",
  too_short: "太短了",
  invalid_format: "格式不对",
  out_of_range: "数值超出允许的范围",
};
export const fieldErrorText = (code: string) => FIELD_ERROR[code] ?? "填写有误";

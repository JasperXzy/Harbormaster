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
  unprotected: ["serious", "未受实时保护", "这台机器的流量计入免费额度，但没有装采集程序。它的用量要等约 1 小时后的官方数据才能看到。"],
  not_applicable: ["neutral", "不需要", "这台机器的流量不计入免费额度。"],
};

/** 实例的实时保护状态。采集程序在线但额度暂时算不出时，不说“未受保护”，以免和没装的机器混在一起。 */
export function protectionOf(i: Pick<T.Instance, "realtime_protection" | "agent" | "traffic_estimation">): [Level, string, string] {
  if (i.traffic_estimation?.status === "ready" && i.traffic_estimation.estimated_bytes !== null)
    return ["warning", i.traffic_estimation.mode === "unverified" ? "未核实估算" : "估算可用", "采集数据已纳入所在账号与区域的共享估算；这不代表已开启真实停机保护。"];
  if (i.realtime_protection === "unprotected" && i.agent.status === "online")
    return ["warning", "暂无法估算", "采集程序在正常上报，所在范围还缺少完整的计量覆盖。请在流量页查看具体原因。"];
  return PROTECTION[i.realtime_protection];
}

export const PERMISSION: Record<T.PermissionKey, string> = {
  ecs_read: "查看云服务器",
  swas_read: "查看轻量服务器",
  cdt_read: "查看流量用量",
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
  balance_low: { title: "余额不足", desc: "可用余额低于所设金额时提醒（在费用页按账号设置金额）。" },
  network_latency: { title: "网络延迟升高", desc: "连续探测超过已配置的延迟阈值时提醒。" },
  network_loss: { title: "网络连接失败", desc: "探测失败比例超过阈值时提醒。" },
  network_unreachable: { title: "目标不可达", desc: "连续探测全部失败时提醒。" },
  network_route_change: { title: "线路变化", desc: "可比较的线路快照发生变化时提醒。" },
  idle_resource_detected: { title: "闲置资源", desc: "发现已确认的闲置资源候选时提醒。" },
  quota_protection: { title: "额度保护", desc: "记录额度规则的判定和执行结果。" },
  cdt_estimation_deviation: { title: "估算偏差", desc: "同一截止时刻的估算与官方值持续相差超过 10% 时提醒。" },
  cdt_usage: { title: "免费额度用量", desc: "每越过一个档位通知一次。" },
  cdt_forecast_exceed: { title: "预计月底超额", desc: "按最近 7 天的日均推算，月底前会超出时通知。" },
  account_api_failure: { title: "账号连接失败", desc: "通常是 AccessKey 失效或权限被改。" },
  agent_offline: { title: "采集程序掉线", desc: "立刻通知，并开始按最坏情况估算用量。" },
  cdt_official_unavailable: { title: "官方数据取不到", desc: "期间沿用上一次的官方数值继续估算。" },
  instance_expiring: { title: "实例快到期", desc: "包年包月的机器到期前提醒，避免忘记续费。" },
  spec_bandwidth_insufficient: { title: "规格带宽不足", desc: "买的公网带宽超过了机器规格能跑的上限时通知。" },
  instance_public_ip_changed: { title: "公网 IP 变了", desc: "每次发现实例的公网 IP 变化都通知。" },
};

export const JOB_TYPE: Record<T.JobType, string> = {
  refresh: "读取资源与用量",
  permission_check: "检查权限",
  telegram_test: "发送测试通知",
  heartbeat_test: "测试心跳上报",
  instance_power: "实例开关机",
  instance_release: "释放云服务器",
  network_probe: "网络延迟探测",
};

export const JOB_STATUS: Record<T.JobStatus, [Level, string]> = {
  queued: ["neutral", "等待执行"],
  running: ["warning", "执行中"],
  succeeded: ["good", "已完成"],
  failed: ["critical", "失败"],
};

export const JOB_TRIGGER: Record<T.JobTrigger, string> = {
  panel: "网页手动",
  api: "API Key",
  cli: "服务器命令行",
  telegram: "Telegram",
  schedule: "自动策略 / 定时",
  system: "系统自动",
  unknown: "未知来源",
};

export const AUDIT_ACTOR: Record<T.AuditEntry["actor"]["kind"], string> = {
  admin: "管理员",
  api_key: "API Key",
  cli: "服务器命令行",
  telegram: "Telegram",
  system: "系统自动",
};

export const STOP_MODE: Record<"keep_charging" | "stop_charging", [string, string]> = {
  keep_charging: ["普通停机", "保留实例公网 IP 与计算资源，停机期间继续按配置收费，开机快。"],
  stop_charging: ["节省停机", "按量付费的计算与云盘停止计费，但非弹性公网 IP 会释放且下次开机可能变慢或因库存不足失败。"],
};

export const DECISION_STATE: Record<T.QuotaDecision["state"], [Level, string]> = {
  below: ["good", "未越线"],
  triggered: ["critical", "触发规则"],
  unknown: ["warning", "数据不足未触发"],
  inactive: ["neutral", "未启用"],
};

export const DECISION_OUTCOME: Record<string, string> = {
  would_stop: "试运行/模拟：本应停机",
  queued: "已下发停机任务",
  power_disabled: "服务器未开启开关机权限",
  self_identity_unknown: "未识别面板所在实例，已拦截",
  self_excluded: "跳过面板自身所在实例",
  exempt: "处于临时豁免期，已跳过",
  already_stopped: "实例已是停止状态",
  operation_limited: "触发 10 分钟最多 3 台的熔断保护",
  source_unqualified: "数据来源尚未满足真实停机条件",
  busy: "已有进行中的电源任务",
  target_unavailable: "目标实例不存在或不可操作",
  restore_quarantine: "备份恢复隔离中，暂不自动停机",
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
  not_implemented: "这个功能还没有开放。",
  network_error: "连不上服务器。请检查网络，或确认 hm-server 正在运行。",
  internal_error: "服务器出错了，请稍后再试。",
  unauthenticated: "登录已过期，请重新登录。",
  forbidden: "没有权限执行这个操作。",
  csrf_failed: "安全校验没有通过。请用面板的正式地址重新打开页面再试。",
  not_found: "要找的内容不存在，可能已被删除。",
  validation_failed: "填写的内容有问题，请检查标红的项目。",
  rate_limited: "尝试次数太多，请稍后再试。",
  revision_conflict: "配置刚刚被改动过，请刷新页面后再保存。",
  quiet_hours_changed: "静默时段配置刚刚被改动过，请刷新页面后再保存。",
  heartbeat_configuration_changed: "心跳配置刚刚被改动过，请刷新页面后再保存。",
  heartbeat_not_configured: "还没有保存外部心跳地址，请先填写并保存后再测试。",
  heartbeat_unavailable: "当前环境暂无法加密保存心跳地址，请检查主密钥。",
  branding_changed: "面板外观配置刚刚被改动过，请刷新后再试。",
  branding_logo_too_large: "图标文件太大了，请上传不超过 256 KB 的 PNG 或 JPEG 图片。",
  unsupported_media_type: "文件格式不支持，请使用 PNG 或 JPEG 图片。",
  instance_power_disabled: "服务器还没有开启实例开关机能力（HM_INSTANCE_POWER_ENABLED=false）。",
  instance_power_busy: "这台实例正在执行另一个开关机任务，请等它完成后再试。",
  idempotency_key_conflict: "重复的开关机请求已被拦截，请刷新状态后重试。",
  passkey_unavailable: "当前访问地址未配置正式 HTTPS 域名，暂无法使用 Passkey。",
  invalid_passkey: "Passkey 验证没有通过，请重试或改用密码登录。",
  passkey_required: "必须至少在当前域名下注册一把 Passkey，才能关闭密码登录。",
  last_passkey: "已关闭密码登录时不能删除最后一把 Passkey，否则将无法登录。",
  passkey_limit: "最多只能注册 20 把 Passkey。",
  passkey_exists: "这把安全密钥已经注册过了。",
  password_login_disabled: "已关闭密码登录，请使用 Passkey 登录。",
  invalid_setup_token: "初始化凭证不正确或已过期。请检查部署时提供的凭证；过期后需更换凭证并重启服务。",
  already_initialized: "已经完成过首次设置，请直接登录。",
  invalid_credentials: "密码不正确。",
  account_name_taken: "已经有同名的账号了，请换一个名称。",
  account_disabled: "这个账号已停用。重新启用后才能刷新或测试权限。",
  account_changed: "账号配置已修改，旧任务已停止；启用的账号会按新配置重新读取。",
  collection_failed: "这次读取没有完成，已保留上一次的数据。",
  permission_denied: "阿里云拒绝了请求，请检查子账号的 RAM 权限。",
  aliyun_throttled: "阿里云接口暂时限流，任务会稍后重试。",
  retry_exhausted: "任务多次中断，已停止重试。请检查服务状态后重新刷新。",
  cdt_line_unverified: "阿里云返回了一种还没核实的线路类型，先不把它算进额度。",
  cdt_response_unrecognized: "看不懂阿里云这次返回的流量数据，先保留上一次的记录。",
  cdt_value_out_of_range: "阿里云返回的数值大得不正常，先不用它估算。",
  cdt_international_endpoint_unverified: "这个站点的流量查询还没有验证过，暂时拿不到官方用量。",
  access_key_invalid: "阿里云不认这组 AccessKey，请检查是否复制完整、是否已被禁用。",
  agent_not_supported: "这台实例暂时不能安装采集程序。",
  telegram_not_configured: "还没有保存 Telegram 设置。",
  telegram_unauthorized: "Bot Token 不正确，Telegram 拒绝了请求。",
  telegram_chat_not_found: "找不到这个 Chat ID。请确认机器人已被加入该对话。",
  telegram_unreachable: "连不上 Telegram。如果服务器在中国内地，需要设置代理。",
  telegram_rate_limited: "Telegram 限制了发送频率，稍后会自动重试。",
  telegram_proxy_unauthorized: "代理拒绝了认证，请检查代理的用户名和密码。",
  telegram_forbidden: "机器人没有权限在这个对话里发消息，可能已被移出或屏蔽。",
  telegram_request_rejected: "Telegram 拒绝了这条消息。",
  telegram_chat_migrated: "这个群已经升级成超级群，Chat ID 变了，请更新。",
  telegram_redirect_rejected: "代理返回了跳转，已拒绝。请检查代理地址。",
  telegram_response_invalid: "收到的回复看不懂，不能确定消息是否已发出。",
  telegram_delivery_uncertain: "不能确定这条消息是否已发出，为避免重复，没有自动重发。",
  telegram_configuration_changed: "Telegram 设置改过了，这条消息没有发到新的对话。",
  telegram_configuration_unavailable: "读不到 Telegram 设置，请检查服务器的主密钥。",
  telegram_configuration_invalid: "Telegram 设置有误，请重新保存。",
  aliyun_unreachable: "连不上阿里云接口，请稍后再试。",
  backup_unavailable: "备份没有生成成功，请稍后再试。",
  job_timeout: "操作等待超时，结果未知。请稍后刷新查看。",
};
export const errorText = (code: string) => ERROR[code] ?? `操作没有成功（${code}）。`;

const FIELD_ERROR: Record<string, string> = {
  required: "这一项必须填写",
  too_short: "太短了",
  invalid_format: "格式不对",
  invalid_value: "选项不对",
  not_allowed: "不使用代理时不能填这一项",
  out_of_range: "数值超出允许的范围",
  invalid_for_instance: "这台机器不支持所选的关机方式（包年包月或轻量服务器不能节省停机）",
  not_current_server: "所填实例 ID 与当前检测到的面板所在机器不一致",
};
export const fieldErrorText = (code: string) => FIELD_ERROR[code] ?? "填写有误";

export const RENEWAL: Record<T.InstanceRenewal["status"], [Level, string]> = {
  auto_renewal: ["good", "自动续费"],
  manual_renewal: ["warning", "需要手动续费"],
  no_renewal: ["serious", "到期不续"],
  unknown: ["neutral", "续费方式未知"],
  not_applicable: ["neutral", "不需要续费"],
};

export const INSTANCE_EVENT: Record<T.InstanceEvent["kind"], string> = {
  discovered: "发现了这台实例",
  disappeared: "实例不见了（可能已释放）",
  reappeared: "实例重新出现",
  status_changed: "状态变了",
  public_ipv4_changed: "公网 IP 变了",
  ipv6_changed: "IPv6 地址变了",
};

export const API_SCOPE: Record<T.APIKeyScope, [string, string]> = {
  "summary:read": ["读取概况", "只能看用量和实例概况，适合手机小组件。"],
  "instance:control": ["控制实例", "可以开机、关机、重启。请只给信得过的程序。"],
  "cron:run": ["触发定时任务", "可以从外部触发一次检查。"],
};

export const RAM_POLICY: Record<T.RAMPolicyDraft["id"], [string, string]> = {
  readonly: ["只读", "只能查看实例、流量和账单。看用量和收告警用这个就够。"],
  instance_power: ["只读 + 开关机", "在只读的基础上，允许开机、关机、重启。"],
  resource_management: ["只读 + 开关机 + 资源管理", "再加上换公网 IP、创建和释放实例。权限最大。"],
};
export const RAM_LIMITATION: Record<T.RAMPolicyDraft["limitations"][number], string> = {
  cdt_action_unverified: "读取 CDT 流量所需的权限名还没有在真实账号上核对过。",
  public_metadata_actions_unverified: "部分公共信息查询的权限名还没有核对过。",
  resource_scope_not_account_bound: "策略没有限定到你的账号或某几台机器，范围比实际需要的大。",
  runtime_writes_unimplemented: "释放实例等高危资源写操作默认保持关闭。",
  creation_dependencies_pending: "创建实例还需要的其他权限没有列全。",
};

const BILL_TYPE: Record<string, string> = { SubscriptionOrder: "包年包月", PayAsYouGoBill: "按量付费", Refund: "退款", Adjustment: "调账" };
export const billTypeName = (t: string) => BILL_TYPE[t] ?? t;

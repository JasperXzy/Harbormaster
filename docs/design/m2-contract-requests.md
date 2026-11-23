# M2 页面还需要的接口（前端提给后端）

日期：2026-11-12。契约现在由后端维护，这里只列前端做 M2 页面还缺什么，字段是建议，由后端定稿后写进 `docs/api/openapi.yaml`。

## 已经按契约做完页面的

| 页面 | 用到的接口 | PRD |
|---|---|---|
| 费用页：余额、本月账单 | `GET /accounts/{id}/finance` | F9.1 |
| 费用页与实例详情：到期、续费方式 | `Instance.expires_at`、`Instance.renewal` | F9.3 |
| 实例页：规格带宽不足提示 | `Instance.network_capacity` | F11.1 |
| 实例详情：IPv6 | `Instance.ipv6` | F2.3 |
| 实例页：变化记录 | `GET /instance-events` | F2.5 |
| 设置页：API Key | `GET/POST /api-keys`、`DELETE /api-keys/{id}` | F13.1 |
| 设置页：备份下载 | `POST /backups` | F16.6 |
| 设置页：权限策略模板 | `GET /ram-policies` | F1.4 |
| 通知页：新增的三条规则 | `instance_expiring`、`spec_bandwidth_insufficient`、`instance_public_ip_changed` | F8.3 |

## 还缺的接口

按前端能不能独立开工排序。前四项不依赖额度保护的设计，可以先定。

| # | 页面需要 | 建议的接口 | PRD | 说明 |
|---|---|---|---|---|
| 1 | 实例开机 / 关机 / 重启 | `POST /instances/{id}/power`，请求体 `{action: start \| stop \| reboot, stop_mode?: keep_charging \| stop_charging}`，返回 `202 + job_id` | F7.1、F6.2 | 关机时界面要让用户选“普通停机 / 节省停机”并说明副作用。需要知道某台实例是否支持节省停机（包年包月不支持）——建议在 `Instance` 上加 `supports_stop_charging: boolean \| null` |
| 2 | 操作记录（任务列表） | `GET /jobs?limit&cursor`，每条带 `type`、`status`、`created_at`、`finished_at`、`error`、`instance_id`、`trigger: panel \| telegram \| api \| schedule` | F7.5 | 现在只有 `GET /jobs/{id}`，页面没法列出“最近做过什么” |
| 3 | 余额不足提醒的金额 | 在 `AlertRuleParams` 加 `balance_below: string`（金额字符串）+ 规则 `balance_low`；或按账号设置 | F9.5 | 费用页目前只能显示余额，不能设阈值 |
| 4 | 心跳地址 | `GET/PUT /settings/heartbeat`：`{url, interval_seconds, last_success_at, last_error}`；`POST /settings/heartbeat/test` 返回 `202 + job_id` | F16.10 | |
| 5 | 静默时段 | `GET/PUT /notify/quiet-hours`：`{enabled, start: "23:00", end: "08:00"}`（业务时区）；临时维护期可后补 | F8.10 | 规则上已有 `urgent` 字段，界面已经标了“紧急” |
| 6 | 审计日志 | `GET /audit-log?limit&cursor`：`{id, at, actor: admin \| api_key \| telegram \| system, action, target, summary}` | F15.7 | `summary` 建议和通知一样由后端渲染成中文 |
| 7 | Passkey | 注册与登录各两步（取 challenge、提交结果），以及列表、删除、“关闭密码登录”开关 | F15.3 | 字段格式跟所用的 WebAuthn 库走，后端定了我照着接 |
| 8 | 期望状态 | `Instance.desired_state: running \| stopped \| quota_stopped`、`exemption_until` | F6.9 | 依赖第 9 项的设计 |
| 9 | 自动化规则（额度保护、定时开关机、保活、月初开机、试运行）与模拟用量 | 待定 | F6.1–F6.13 | **卡在 V3**：官方数据不带统计时间，实时估算的算法没定，额度保护的触发依据也就没定。等替代方案确定后再起草 |
| 10 | Telegram 远程控制白名单 | `GET/PUT /notify/telegram/control`：`{enabled, allowed_user_ids: string[]}` | F8.4 | |

## 需要后端确认的两点

- **停用账号**：设置页现在只用 `PATCH /accounts/{id} {enabled: false}`，不再调用 `DELETE`。如果 `DELETE` 还承担别的清理工作，请告知。
- **本月账单的合计**：契约说明不要对账单行求和（退款、调账的正负号不确定），所以费用页只逐行列出、不显示合计。如果后端能给出一个可靠的“本月应付合计”，页面会更直观。

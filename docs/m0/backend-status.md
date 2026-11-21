# 后端实现与验收状态

核对日期：2026-11-21。M0–M4 前后端功能已按用户确认方案全部实现并部署到 `main-server`（`https://cdt.jasperxzy.com`，镜像 `harbormaster-server:m4-f995843`，版本 `0.4.0`），同时完成了独立网络命名空间下的 Linux 内核计数验证；详见 [M3–M4 完成与上线记录](m4-rollout.md) 与 [M0–M2 完成与上线记录](m2-rollout.md)。此前记录见[额度保护运行记录](quota-protection-rollout.md)、[前端交接](frontend-protection-handoff.md)与[首次部署](main-server-rollout.md)。

当前线上已开启实例电源控制（`HM_INSTANCE_POWER_ENABLED=true`），并按用户确认严格保持 3 项高危/计费云写开关默认关闭（`HM_INSTANCE_RELEASE_ENABLED=false`、`HM_EIP_MANAGE_ENABLED=false`、`HM_INSTANCE_CREATE_ENABLED=false`）。真实云上停开机时延、24 小时业务流量对账（V7）、多渠道真实投递/远控交互以及高危写操作仍按实际运行证据区分“代码与前端已上线”与“真实业务场景已实测”。

范围：全栈在分支 `be/m0-foundation` 分阶段提交并通过 `go test -race ./...` 与前端构建检查；未 push 或合并 `main`，`docs/PRD.md` 原文未改。

## M0 与初始工程要求

| 项目 | 状态和证据 |
|---|---|
| V1 | 部分接口真实只读通过；写权限和部分完整权限集合未验证。[V1](V1-api.md)、[发现](production-discovery.md)、[费用](account-finance.md)、[续费](instance-renewal.md) |
| V2 | 与 PRD 假设不完全一致：额度 20/200GB、自然月重置已确认；两个账号实际服务查询返回 Enabled=true，仍不能据空产品列表确定资源资格，站点差异待核实。[V2](V2-quota.md)、[服务读取](cdt-service-reads.md) |
| V3 | 实际响应没有 cutoff，不能按 PRD 公式精确对齐；实际延迟、补账仍缺长期观察。[V3](V3-cdt-watermark.md) |
| V4 | 每小时按整个共享带宽包合并方向后取较大值已确认；CDT 资格不能只看实例创建日期。[V4](V4-dominant-traffic.md) |
| V5 | 文档确认增强型 95 的方向/峰值口径；没有将文档结论冒充实际账单对账。[V5](V5-enhanced95.md) |
| V6 | sha-pro 元数据返回底层 ECS ID，与轻量 API ID 不同；用户已同意注册码预绑定。轻量套餐计量方向仍有验证边界。[V6](V6-swas.md) |
| V7 | `main-server` 独立网络命名空间 Linux 内核计数测试已通过（`TestKernelCounters` 0.32s，验证 IPv4/IPv6 公网入出、私网排除、重启继承与异构表保护）；线上 `hm-agent` 持续正常上报。受控 24 小时业务对账（≤5% 偏差）仍待长期运行观察。[V7](V7-agent-metering.md)、[M2 上线与内核验证](m2-rollout.md) |
| V8 | hkg-pro 的 EIP 与 hk-share 关联已真实读取确认。[V8](V8-hkg-network.md) |
| V9 | 仅文档核对；真实节省停机/开机试验没有授权，未执行。[V9](V9-economical-stop.md) |
| Go 骨架、配置、日志、WAL/迁移、CLI、健康检查 | 已实现并通过本地构建/测试；后续实现均建立在同一工程上。[初始验证](validation.md) |
| 无账号开发模式 | 已有隔离的合成数据、会话和只读接口；不是生产计量或自动化模拟注入功能。[前后端分离](frontend-backend-separation.md) |
| 可选嵌入与缺 dist 回退 | 已实现。默认 API 独立运行；统一构建脚本可暂存 web/dist 后嵌入，缺失时使用占位页。[构建验证](validation.md) |
| 私有 agent 协议与数据库说明 | 已交付并随实现更新。[协议](agent-protocol.md)、[数据库初始设计](database.md) |
| CI | 构建、race、vet、跨平台及容器检查配置已存在；没有 push，因此没有本轮远程 Actions 执行结果。配置存在不等于 CI 已运行成功。 |

## PRD 功能覆盖

“已实现”在本表指后端代码与所列验证，不自动代表前端已适配或真实环境端到端验收通过。“通知记录”与“实际发送”明确区分。

| 需求 | 当前状态 |
|---|---|
| F1.1 | 账号创建/修改/停用已实现；删除按用户确认采用停用并保留历史。凭据加密、版本隔离及停用停止采集已测试。[账号发现](production-discovery.md) |
| F1.2 | 只读权限测试已接通，写权限保持 not_verified；不能由读成功推定能开关机。 |
| F1.3 | 本地额度、阈值配置已实现；按用户确认采用 1024³ 字节单位，重置日固定自然月 1 日。保存阈值不等于用量告警已启用。 |
| F1.4 | 三档 RAM 策略草稿、只读复制接口（`GET /ram-policies`）、部署 JSON 与设置页一键复制界面均已实现并上线；完整最小权限云端真实身份验收仍区分于本地草稿。[RAM 草稿](ram-policy.md) |
| F2.1、F2.2 | ECS/SWAS 多账号多地域发现和主要字段已实现；未知 CDT 资格仍为 null。真实读取到两个账号的四台实例。 |
| F2.3 | ECS 扩展字段优先、按实例主副网卡分页与 3 秒回退、失败保留旧值已实现。地址分配与公网/计费状态分开；SWAS 来源仍未确认。[IPv6](ipv6-discovery.md) |
| F2.4 | 实例本地多行备注 `note`、分组 `group`、多标签 `tags` 存储与 `PATCH /instances/{id}/metadata` 及前端筛选/编辑已实现（`050` 迁移，不回写阿里云）。 |
| F2.5 | 已有持久的首次发现、消失/重现、状态及 IPv4/IPv6 变化记录和分页接口。消失表示完整扫描不再出现，不能证明释放原因；未接入云端操作事件来源。[实例事件](instance-events.md) |
| F3.1、F3.11 | 官方累计与日级/预测曲线已接通；`fetched_at` 与未知官方 cutoff 分开。真实每日计费账单长期延迟仍按实际观测保留说明。[CDT](production-cdt.md) |
| F3.2 | 当前公网出口与计费对象识别已实现；多出口/失败拓扑保留未知，不把创建时间当作 CDT 资格证明。 |
| F3.3–F3.7 | 双路径估算、max＋速率提前量、同截止对账、近 7 个完整日月底预测及阶梯单价超额费用估算（`GET/PUT /traffic/pricing`、`QuotaStatus.overage_cost`，`051` 迁移）前后端均已实现。[计量与保护](realtime-metering.md)、[M2 上线](m2-rollout.md) |
| F3.8 | 各账号本地额度及全账号汇总视图（保留未知缺口不补零）已实现。 |
| F3.9 | 计费对象当天与本计费周期累计归因流量及同范围占比（`cycle_*_bytes`、`cycle_share_percent`）前后端已实现。 |
| F3.10 | 共享带宽配置、成员列表、当天收发/主方向汇总与每小时收发接口（`/bandwidth-packages/{id}/hourly`）前后端已实现。 |
| F4.1 | 实例及无流量包状态已实现，sha-pro 空包结果已真实确认。 |
| F4.2 | 轻量与 ECS 电源调用、持久任务、状态确认及前端实例详情开关机操作已在 `main-server` 开启上线。 |
| F4.3 | 四项官方字节值的采集、身份隔离缓存、前端展示、轻量套餐额度保护规则支持及 `swas_traffic_usage`（80/90/95% 与耗尽预测，`055` 迁移）告警均已实现并上线。[流量包缓存](swas-traffic-package.md) |
| F4.4 | 轻量预绑定注册与流量上报代码已实现，并排除 CDT 保护；真实安装和延迟测速未完成。 |
| F5.1 | agent 原始样本历史、`agent_hourly_traffic` 时间分摊小时历史（最长 13 个月）与云监控分钟历史回退及实例详情 24h/7d 收发折线图已实现。[Agent 历史](agent-network-history.md)、[云历史](cloud-network-history.md) |
| F5.2 | ECS CPU/绝对积分与 SWAS CPU 的后台采集、缓存、过期状态和实例详情展示已实现。[指标来源](cpu-metrics.md) |
| F6.1–F6.13 | 额度保护规则（含 `allow_unverified_execution` 可选实时估算执行）、模拟用量演练、试运行、每日定时开关机（`activated_at` 下个计划点生效 + 15 分钟停服补偿）、意外停止保活、每月 1 号自动开机、跨账号主备切换（`F6.8`，`057` 迁移）、面板外关机意图确认及 `/automation` 前端页面均已实现并上线。[M2 上线](m2-rollout.md)、[M4 上线](m4-rollout.md) |
| F7.1、F7.5 | 刷新、权限查询与实例开关机持久队列、任务分页筛选及前端开关机弹窗（区分普通停机与节省停机副作用提示）已上线。[任务记录](job-history.md)、[电源调用](instance-power.md) |
| F7.2、F7.3 | 更换受管 EIP（`POST /instances/{id}/change-eip` + `managed_eip` 原有 EIP 严格防误删保护）与创建按量/抢占 ECS（`GET /cloud/ecs-catalog`、`POST /instances/create` + 可选自动创建 VPC/交换机/安全组，`057` 迁移）前后端已实现，按安全边界在 `main-server` 默认保持 `HM_EIP_MANAGE_ENABLED=false`、`HM_INSTANCE_CREATE_ENABLED=false`。[M4 上线](m4-rollout.md) |
| F7.4 | ECS 释放后端队列、预览（`GET /instances/{id}/release-preview`）、前端输入完整实例名二次确认弹窗（`POST /instances/{id}/release`）与安全边界均已实现，按安全边界保持 `HM_INSTANCE_RELEASE_ENABLED=false` 关闭。[API/接入](ecs-release-api.md) |
| F8.1 | Telegram 渠道配置、代理认证与异步测试发送已在生产上线。[实际发送](telegram-dispatch.md) |
| F8.2 | 按用户确认采用 `1024^3` 字节口径默认启用 `cdt_usage`（档位周期去重）与 `cdt_forecast_exceed`（超额预测与恢复，`052` 迁移）。 |
| F8.3 | 全部 18 项告警规则（含 `swas_traffic_usage`、`cpu_credit_low`、`system_event`、网络延迟/丢包/不可达/路径变化等）前后端管理已接通。 |
| F8.4 | Telegram 远程控制 Bot（数字用户 ID 白名单、`/status`、`/quota`、`/start`、`/stop`、`/reboot` 及 `/changeip` 二次按钮确认、外部关机内联确认按钮）前后端已实现并上线。 |
| F8.5、F8.6 | 每日定时与手动流量简报（`GET/PUT /notify/daily-report`、`POST /notify/daily-report/send`）、AES-256-GCM 加密 SMTP 邮件与 Webhook（HMAC-SHA256 签名 + 自定义头）多渠道并发投递（`056` 迁移）前后端均已实现并上线。[M4 上线](m4-rollout.md) |
| F8.7–F8.10 | outbox 租约重试、告警恢复去重、每日静默时段与临时维护期配置前后端均已实现并上线。[静默](quiet-hours.md) |
| F9.1–F9.5 | 账户余额、月账单、单实例月度账单明细（`F9.2`）、续费状态、闲置计费资源巡检（`F9.4`，无可靠报价保留 `null`）与费用页低余额告警阈值前后端均已实现并上线。[费用](account-finance.md)、[余额告警](balance-alerts.md) |
| F10.1–F10.11 | ICMP/TCP 延迟探测、单向与去回程双向 Traceroute、文本导出、离线 ASN/国家/城市数据库、每日线路快照与 AS 路径变化提醒，以及 `/network` 前端完整页面（延迟矩阵、24h/7d RTT/抖动/丢包曲线、逐跳路由表、目标/组合/快照管理）均已实现并上线。[M4 上线](m4-rollout.md) |
| F11.1 | 已实现出方向配置能力比较及告警记录；不宣称入方向所有产品限额或实测吞吐已验证。[检查](spec-bandwidth-check.md)、[告警](bandwidth-alerts.md) |
| F11.2 | 突发性能 CPU 积分告警（`TotalCredit / 24h 基准上限 < threshold_percent`，默认 20%，`055` 迁移）前后端已实现并上线。[上限核对](cpu-credit-specs.md) |
| F11.3 | ECS/CMS 系统事件采集、跨来源去重告警评估（维护仅从 `ecs_history` 且遵守静默，抢占仅从 `cms_spot` 5 分钟内发布且紧急绕过静默）及实例页系统事件折叠面板均已实现并上线。[事件采集](system-events.md) |
| F11.4 | 账号连续失败的告警记录与多渠道投递已接通。 |
| F12.1 | Linux 双架构发布包、校验下载、一行命令、systemd 单元及模拟安装回滚已实现；2026-11-18 已补齐发布文件并上线 root 无 sudo 的命令兼容修复。用户自行安装的 sg-server-nano 已正常上报；受控安装/计量验收仍待完成。[安装](agent-installation.md)、[发布修复](agent-release-publication.md)、[root 兼容](root-agent-installer.md) |
| F12.2 | ECS 元数据匹配及用户批准的轻量预绑定流程已实现；元数据文本不等于远程可信证明。 |
| F12.3 | Linux nftables 原生代码、规则检查及 `main-server` 独立网络命名空间内核测试（`TestKernelCounters`）已通过；24 小时业务口径验证待长期运行观察。[M2 上线](m2-rollout.md) |
| F12.4 | 10 秒默认/本地可配上报、加密离线队列、批量 ACK、重启计数代际已实现并在线上运行。[客户端](production-agent-client.md)、[周期](agent-cadence.md) |
| F12.5 | 掉线状态、事件及掉线期间按近期峰值/带宽最坏情况推算已接通。 |
| F12.6 | 内置测速与线路执行器、持久队列、独立私有领取/回报、结果校验、agent 加密待办、串行执行恢复及 `/network` 前端视图均已实现并上线。 |
| F12.7 | 当前固定动作、独立 token、哈希与撤销已实现；没有任意命令接口或云助手。 |
| F12.8 | 后端版本、最后上报时间、校验过的本地发布版本（已更新至 `v0.4.0`）与前端 `update_available` 升级提示/升级命令生成已实现。[版本](agent-version-hints.md) |
| F13.1、F13.2 | API Key 管理接口、精确 scope 鉴权及实例控制/刷新/任务查询入口前后端已实现。[API Key](api-key-management.md) |
| F13.3、F13.4 | 小组件只读摘要接口 `GET /api/v1/integrations/summary`（`summary:read`）与外部定时触发接口 `POST /api/v1/integrations/cron/run`（`cron:run`）及设置页接入说明均已实现并上线。 |
| F14.1–F14.3 | SQLite 持久队列、租约恢复、幂等键与快照非阻塞 GET 已实现。 |
| F15.1–F15.9 | 首次设置、密码与 Passkey WebAuthn 登录/绑定/关闭密码登录开关、AES-GCM 主密钥隔离（覆盖阿里云 AccessKey、Telegram、SMTP、Webhook 与外部心跳）、包含非敏感前后值与中文摘要的审计日志及 HTTPS/CIDR 访问控制均已实现并上线。[M2 上线](m2-rollout.md)、[M4 上线](m4-rollout.md) |
| F16.1–F16.10 | Docker 非 root 只读根容器构建、在线备份与 CLI 恢复、数据保留期配置与后台清理、登录前/登录后自定义名称与图标（`F16.9`）、外部心跳自监控及只读容器安全的在线版本中心与升级命令复制（`F16.8`）均已实现并上线。[M4 上线](m4-rollout.md) |
| F17 | M0–M4 全部页面（总览、实例、流量、网络、自动化、费用、通知、设置、登录与首次向导）及 `VITE_API_MODE=mock` 契约对齐均已提交并原子发布至 `main-server`。[M4 上线](m4-rollout.md) |

## 已确认并落地的 M3/M4 细节决定

此前 M0–M2 与 M3–M4 的全部设计细节均已由用户于 2026-11-21 确认采纳推荐方案并落地：

| 问题 | 已提出的推荐方向 | 影响 |
|---|---|---|
| CPU 积分告警分母 | TotalCredit / 该规格 24 小时累积上限；不另加初始积分，缺失/过期不判断，比例可超过 100% | F11.2（M3） |
| 系统事件通知范围 | 维护限 Scheduled/Executing/Inquiring，抢占信号限发布后 5 分钟；其余历史只记录，不补发 | F11.3/F8（M3） |
| 系统事件紧急分类 | 抢占中断不受静默影响，普通维护遵守静默 | F11.3/F8.10（M3） |
| 系统事件跨来源去重 | 维护只从 ECS 通知、抢占只从 CMS 通知，两套原始记录都保留 | F11.3/F8.9（M3） |
| 闲置资源月估算 | 按当前配置保持整月，可靠官方小时报价 × 业务时区当月小时数；无可靠价格/资格则未知 | F9.4（M3） |
| 登录前品牌展示 | 允许登录页/首次设置页读取名称和 Logo，同时保留 HTTPS/主机名/CIDR 限制 | F16.9 可选扩展 |

main-server 只读试运行和旧 CDT-Monitor 退役已获用户授权并完成。安装 agent、V7 24 小时观察及 V9 写实验仍未授权，后续需按具体可复核计划分别确认。已有凭据和 SSH 访问条件不需要重新索取。开关机实验未获授权不妨碍在本地编写和模拟测试控制代码，不能把所有未实现项都归为等待授权。

## 既有决定与工程选择

用户已确认：单管理员无需用户名；账号删除即停用保留历史；本地 GB=1024³ 字节；CDT 原值暂按字节显示并标待核实；无 cutoff 时估算待确认、依赖精确对齐的保护禁用；轻量注册码预绑定；前后端独立；每个通过检查的阶段直接提交、不 push；恢复时取消未完成任务与待发通知并清空租约；手动开关机入队时保存期望状态，云调用失败后保留意图并显示任务错误；API Key 撤销时取消尚未进入发送阶段的任务并保留意图；关闭密码登录后，CLI reset-password 重新开启密码、清除所有会话与未完成挑战，并保留 Passkey；外部心跳仅在面板/SQLite 就绪和采集调度最近有进展时发送，单账号云失败不阻断；静默/维护期间的非紧急告警只记录，结束后不补发；网络测速只允许公网目标，解析后也校验；抖动为相邻成功 RTT 差绝对值的平均，丢包打断配对；实例目标跟随所选地址族的唯一公网 IP，发现不完整/无地址/多地址时跳过；停服错过的测速不补历史，恢复后安排一次当前测速；网络告警保留 PRD 的 10 分钟/5%/3 次触发默认，恢复需要连续 3 次有效健康结果，缺测打断累计但不恢复。

用户 2026-11-18 确认：Telegram 结果不确定时不自动重发；代理模式和地址不变才保留省略的代理认证；CDT 单位核实前不启用用量阈值告警。实现见[实际发送](telegram-dispatch.md)。

用户另已确认：离线归属默认 ASN＋国家级、城市级可选；线路仅按证据标注，CN2 的 GIA/GT、阿里云跨境状态保持待确认，估计国家/城市序列不直接判定绕行。

用户已确认 ECS 释放范围：首期仅已停止的按量/抢占式实例，关联云盘/自动快照沿用当前删除属性并展示影响，不自动修改属性，不另外释放 EIP 或手动快照。这不构成真实云写操作授权。

用户已确认定时线路快照：每个已启用测速组合单独开启，业务时区每日时间默认 03:00；不补停服期间历史，恢复后仅安排一次当前快照。变化只比较目标 IP 相同、均到达、每 TTL 的 ASN 唯一完整的路径，合并连续 ASN；缺测、未知或多 ASN 不可比较。ASN 库版本不同先不告警，以当前快照作为后续比较起点。实现及验证见每日快照文档。

用户已确认双向追踪：一个请求原子创建两端任务，提交时任一端不可用则都不创建；成功入队后独立领取/执行，保存各端实际时间、结果和错误，不要求毫秒同步。实现与验证见双向追踪文档。

当前工程边界已分别记录：agent 周期 1–60 秒、队列 8640 样本、版本提示缓存最长 30 秒、金额最多 12 位有效小数且超界拒绝。API Key 按当前可撤销模型没有新增自动到期策略，权限改变通过替代密钥处理。这些不是对阿里云计费、性能或真实部署结果的推定。

## 尚缺的阶段验收证据

- M1：真实 agent 安装、连续 24 小时计量/对账 ≤5%、可靠实时估算、阈值 Telegram 送达、掉线 30 秒送达，均未证明。Telegram 发送 worker 已接通，但尚无真实送达证据。不能因当前告警有本地记录就标为通过。
- M2：额度保护调度到假云停止确认已验证（1.093 秒、真实云调用 0），包含最坏情况和持久守卫；其余自动化、真实云操作延迟、Telegram 远控与外部心跳的完整场景仍未完成。用户此次明确要求提前退役 CDT-Monitor，现已执行；不据此标记 M2 验收通过。
- M3/M4：核心功能及验收均未完成。
- 非功能：macOS 空库设置/密码登录后测到 RSS 约 210.2MB，显式 GOMEMLIMIT=80MiB 时约 146.8MB，该本地 RSS 场景未达 100MB；未据此设置默认内存限制或修改 PRD。[内存实测与修订建议](server-memory-baseline.md)。生产 Linux/10 实例常驻内存、agent 资源占用、长期数据库 ≤1GB、保留期、保护响应 ≤30 秒、真实发送可靠性与跨平台运行仍缺对应实测。

最近计量、保护、同截止对账、全量测试／race 和调度假云结果见[计量与保护](realtime-metering.md)。此前构建与契约检查见[闲置告警](idle-resource-alerts.md)；隔离副本真实只读缓存验证见[闲置候选](idle-resource-candidates.md)。此前见 [ECS 释放 API/接入](ecs-release-api.md)、[执行/确认](ecs-release-worker.md)、[任务](ecs-release-queue.md)、[预览](ecs-release-preview.md)和[固定调用层](ecs-release.md)。另见[轻量流量包](swas-traffic-package.md)、[名称与 Logo](branding.md)、[CDT 服务](cdt-service-reads.md)、[闲置来源](idle-resource-sources.md)、[延迟清理](latency-retention.md)、[API Key 刷新](api-key-refresh.md)、[审计](audit-log.md)、[CPU 规格](cpu-credit-specs.md)及各需求行文档。初始跨平台命令见 [M0 原始输出](validation.txt)。默认关闭真实电源与释放执行；当前未实现及未验收项仍需推进，不能据此将持续目标标记完成。

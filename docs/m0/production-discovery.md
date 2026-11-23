# 生产账号管理、任务与只读资源发现

2026-11-12，接续生产认证阶段。前端仍独立位于 `web/`，后端通过 OpenAPI + SSE 对接。

## 已实现

- 添加、编辑、停用和重新启用账号；AccessKey ID 与 Secret 一起使用 AES-256-GCM 加密，AAD 绑定账号，响应只返回打码 ID。创建账号、保存密文与首次权限/发现任务在同一事务提交。
- 用户确认的账号移除语义：停用并保留实例快照与历史，不删除云资源。停用后不调度新查询；已发出的查询可能结束，但旧配置版本或失效租约的结果不能发布。
- 本地保护额度默认内地 20 × 1024³ 字节、海外 200 × 1024³ 字节，可修改。这是用户确认的本地换算规则，不声明官方流量口径已对齐；CDT 资格及截止时间未知仍保留 null。
- SQLite 持久化任务：队列、首次运行/完成时间、最多三次尝试、60 秒租约、15 秒续租、随机租约所有者。重复请求可复用同类型/范围的活动任务。过期租约可被其他 worker 回收，旧 worker 无法完成新租约。
- 账号另有扫描租约与配置版本，防止全量刷新、单账号刷新、权限测试并发覆盖。配置修改会终止旧的单账号任务，按新配置入队；全量任务继续处理其他账号。
- 默认两个 worker，每账号默认每 300 秒扫描；worker 数和周期可配置。阿里云请求在单进程内间隔至少 200ms，单请求超时 20 秒，任务超时 10 分钟。只读网络/限流失败最多尝试三次；不同账号互不阻塞其结果保存。
- GET 账号、实例、地域、计费对象、共享带宽、任务状态均读取数据库快照，不访问阿里云。实例支持筛选与游标分页。任务状态变化持久化，并通过 SSE 合并发送；断线后仍以重新读快照为准。
- 支持 ECS、轻量、EIP、网卡绑定与共享带宽关联；按主流量的包只作为一个计费对象。多公网出口返回 `billing_subject_ids`，单出口摘要留空，避免任选一个出口代表整台实例。
- 轻量流量包只有读取成功且为空时显示“无流量包”；查询失败显示待确认。扫描分页、身份字段或某个地域/产品读取失败时，不将旧资源误删为已释放。完整扫描确认消失的资源停止出现在当前列表，但保留原行及实例历史关系。
- 固定的 SDK 只读 action/端点/参数白名单；拒绝重定向和 SDK DEBUG 输出。RAM 写权限标为未验证，不通过实际开关机或 EIP 写操作验证。CMS 没有合适的 EIP 测量目标时标为未验证，不拿读取指标定义冒充指标读取成功。

## 接口与工程约定

新增/启用现有契约：账号写 API、`/instances/refresh`、`/jobs/{id}`、生产实例/计费对象/共享带宽/地域读取。契约补充未知计费类型、固定带宽 EIP、多出口列表和可空带宽值；前端生成类型、未知状态文案及任务结束后刷新已同步。

新增迁移 `003_accounts_jobs.sql`，含账号配置版本、job、job_change、resource_snapshot。凭据只在既有 credential 表中保存密文，任务只存内部账号 ID，不存 AccessKey。

`worker_count` / `HM_WORKER_COUNT` 为 1–8，默认 2；`resource_scan_interval_seconds` / `HM_RESOURCE_SCAN_INTERVAL_SECONDS` 为 60–86400，默认 300。当前版本仍无云资源写操作实现。

生产 `run-once` 会处理已到期的只读任务；开发 `run-once --dev` 仍只做本地检查。使用 `hm-server import-accounts` 可将现有 M0 加密凭据库导入生产数据库，默认读取原 M0 外部路径，也可指定 `--source-vault`、`--source-key-file`。源密钥与目标的 `HM_MASTER_KEY(_FILE)` 独立；只在内存解密再加密，不产生明文中间文件。已有同名账号跳过，不覆盖其配置或凭据。导入仅入队，不立即查云。

地域初始目录来自 2026-11-11 成功的 DescribeRegions 查询；账号扫描后优先使用保存的目录。目录不保证每个产品、账号在每个地域均可用，实际由各产品查询结果确定。

## 实际验证

隔离的仓库外生产测试数据库成功导入两个既有账号，再运行只读 `run-once`。结果：

| 账号 | 实例 | 云侧名称与识别 |
|---|---:|---|
| jasperxzy | 3 ECS | 新加坡 `sg-server-nano`、香港 `hk-server-nano` 均为共享带宽按主流量；上海 `Ali_ShangHai` 为固定带宽 |
| winnemjw | 1 轻量 | `sha-pro`，无流量包 |

两个账号读取状态为 ok。jasperxzy 的 ECS/SWAS/CDT/CMS/VPC/BSS 读取通过；winnemjw 除 CMS 无目标而未验证外，其余读取通过。两个账号的开关机与 EIP 管理权限均未验证。没有执行任何真实云写入。

首次导入测试发现旧 M0 密钥加载器会被目标数据库的环境密钥覆盖；已分离源与目标读取路径，并加入回归测试。该次失败没有发出云查询。

自动化验证包含加密/AAD、账号与任务事务、重复提交、配置变更竞争、停用保留历史、两个数据库连接抢租约、任务恢复、失败保留旧快照、SDK 签名与写请求拦截、分页、共享带宽关联、多出口、轻量缺包/查询失败区分、GET 无云查询、TLS 下 job.updated 与会话撤销。命令输出见 [验证记录](production-discovery-validation.txt)。

字段来源复核：[ECS DescribeInstances](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstances)、[ECS 官方 API 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstances/api.json)、[轻量 ListInstances 元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/ListInstances/api.json)、[VPC EIP 元数据](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/DescribeEipAddresses/api.json)，并与上述真实只读响应比对。创建时间不被当作 CDT 资格的充分证据。

## 尚未覆盖

本阶段不宣称完成整个 M1。CDT 数据持久化/历史图表、真实 agent 注册与计数、告警、Telegram 发件箱以及生产部署继续在后续阶段实现；真实机器安装和云写验收仍按既有授权边界执行。资源快照不是实时流量，未部署 agent 的实例不会被标为实时受保护。公开部署、长时内存/数据库容量验收尚未进行。

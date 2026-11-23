# 审计日志

日期：2026-11-13。对应 PRD F15.7；后端与契约已实现，界面由 Claude 按契约对接。配置前后值是否保存仍待用户答复，本批只保存固定元数据与实际变更的字段名。

## 当前覆盖

| 修改入口 | 动作代码 | 记录来源 |
|---|---|---|
| 云账号创建、配置修改、凭据替换、停用 | `account_created` / `account_updated` / `account_disabled` | 管理员会话、CLI 导入 |
| 已实现的告警规则 | `alert_rule_updated` | 管理员会话 |
| 静默时段、维护窗口 | `quiet_hours_updated` | 管理员会话 |
| 测速目标、测速组合的创建、更新、软删除 | `probe_target_created/updated/deleted`、`probe_plan_created/updated/deleted` | 管理员会话 |
| 每日线路快照设置 | `trace_schedule_updated` | 管理员会话 |
| API Key 创建与撤销 | `api_key_created` / `api_key_revoked` | 管理员会话 |
| Agent 注册码签发、绑定或换 token、撤销 | `agent_registration_issued` / `agent_registered` / `agent_revoked` | 管理员签发/撤销；私有注册完成记为 system |
| 实例电源请求及期望状态 | `instance_power_requested` | 管理员或已鉴权的 API Key |
| 电源任务开始、下发标记、响应、终止 | `instance_power_started` / `instance_power_dispatch_started` / `instance_power_dispatch_recorded` / `instance_power_finished` | system |

CLI / Telegram / system 来源由已有可信任务上下文映射；Telegram 远程控制尚未接通，支持该来源枚举及本地测试不代表能通过 Telegram 操作实例。未实现的自动化、备注/标签和 M4 操作没有虚构审计入口，接入这些修改时须在同一事务内追加记录。

云采集缓存、流量上报、租约续期和调度游标更新不作为管理员配置修改记录；这些数据保留在各自的观测、任务或事件表中。登录会话与 Passkey 管理目前也不在本批审计覆盖内。

## 存储与提交边界

迁移 `037_audit_log.sql` 新建 `audit_log`，以自增 `seq` 进行稳定分页。字段为 UTC 创建时间、可信操作者种类及可选的内部 API Key ID、固定动作代码、对象种类/内部 ID、可选账号/任务 ID、类型明确的详情 JSON。账号、对象、动作、来源和时间有查询索引。历史引用不设外键，避免将来的业务历史清理连带删除审计证据。

审计插入与业务修改使用同一个 SQLite 事务。插入失败会回滚配置、凭据、期望状态、任务或发送标记。发送 worker 必须成功提交下发标记及其审计后才可调用云接口。`job` 的状态触发器覆盖电源任务开始和终止，因此撤销 Key、重启租约恢复和备份恢复取消旧任务也会记录，不只覆盖正常 worker 分支。响应记录先于终止记录；API 接受请求之后仍为 running，只有后续观测确认才记录 succeeded。

任务的事件键重试、合并到同一在途动作的别名，不生成第二条逻辑电源请求。注册响应重传与重复撤销也不重复记录凭据变更。已接受的配置保存可以产生字段名列表为空的记录，例如重复保存同一配置；这不宣称配置值发生了变化。

迁移不补写历史用户动作，不推测旧任务的发起人。迁移前已存在的电源任务，之后若发生恢复终止，仅记录实际发生的新状态变化。备份包含审计表；恢复保留已有记录，并追加此次旧电源任务取消的 system 记录。

这是应用内追加记录机制，没有提供修改/删除审计的 HTTP 路由，也没有实现防数据库管理员篡改或自动保留期。

## 内容边界

- 详情只允许字段名、凭据变更布尔标记、配置 revision、电源动作/停机模式、任务状态/下发结果、固定错误代码及确认依据。
- 不保存账号名、目标地址、规则参数等配置值；不保存 AccessKey ID/Secret、token、注册码、nonce、密码或其哈希/密文，也不复制任意请求、云响应、SDK 错误文本。
- 操作者取自已鉴权入口的上下文，客户端不能通过 JSON 填写 actor。API Key 只记录经 scope 校验的内部 ID；单管理员、CLI 与 system 的 actor.id 为 null。
- `summary` 使用 `audit: <action>` 占位，最终文案由前端维护。

## 查询与前端对接

`GET /api/v1/audit-log` 仅管理员 Cookie 会话可读；API Key 与 agent token 不提供管理员访问能力。按序号倒序，默认 50 条、最多 200 条；`next_cursor` 与 actor/action/target_type/target_id/account_id 和起止时间绑定。并发新增记录不会使后续页重复或跳过原先更旧的记录。所有筛选为精确匹配，SQL 参数化。

`from` / `to` 可同时省略，以游标读取已有历史；显式提供时使用 `[from,to)`、最多 31 天、截止时间不能在未来。读取不调用阿里云、执行任务或追加“查询”审计。开发模式提供 4 条带 demo 标记的合成记录、相同筛选与分页，保持只读。

## 验证

专项测试实际执行：

```text
go test ./internal/store ./internal/api ./internal/devmode -run 'TestAudit|TestDemoAudit|TestDevelopmentContractAndSessionBoundary' -count=1
ok  github.com/JasperXzy/harbormaster/internal/store    1.311s
ok  github.com/JasperXzy/harbormaster/internal/api      0.808s
ok  github.com/JasperXzy/harbormaster/internal/devmode  2.004s
```

覆盖事务回滚、凭据排除、注册/撤销与请求重传去重、管理员/CLI/Key/system 来源、电源响应与观测成功的区分、恢复/撤销终止、036→037 无伪造历史迁移、外键检查、分页间新增数据、精确筛选和游标绑定、会话隔离、查询无云调用/任务变更及开发模式。

全量检查实际执行并通过，以下为输出节选：

```text
go build ./...                         # exit 0，无输出
go vet ./...                           # exit 0，无输出
gofmt -l cmd internal                  # exit 0，无输出
git diff --check -- internal docs/m0 docs/api/openapi.yaml  # exit 0，无输出
go test -race ./...                    # exit 0，全部包通过
ok  github.com/JasperXzy/harbormaster/internal/api       307.624s
ok  github.com/JasperXzy/harbormaster/internal/backup    109.132s
ok  github.com/JasperXzy/harbormaster/internal/cli        43.882s
ok  github.com/JasperXzy/harbormaster/internal/collect    28.465s
ok  github.com/JasperXzy/harbormaster/internal/power      45.238s
ok  github.com/JasperXzy/harbormaster/internal/store     378.962s
ok  github.com/JasperXzy/harbormaster/internal/traceinfo  16.572s
npx --yes @redocly/cli lint docs/api/openapi.yaml
Your API description is valid.  # exit 0，6 条原有警告
```

最后补齐开发示例任务链接、字段差异记录后，对受影响范围再执行：

```text
go test -race ./internal/devmode
ok  github.com/JasperXzy/harbormaster/internal/devmode  36.530s
go test -race ./internal/store -run 'TestAudit|TestAgentRegistrationPrebindingRetriesRotationAndRevocation|TestProbeConfigurationSchedulingMissedPeriodsAndHistory|TestTraceSchedule' -count=1
ok  github.com/JasperXzy/harbormaster/internal/store    14.594s
```

本阶段还执行了 `CGO_ENABLED=0 GOOS=<linux|darwin|windows> GOARCH=<amd64|arm64> go build -trimpath -o <临时文件> ./cmd/hm-server` 的 6 组交叉构建，均 exit 0；交叉构建不代表目标系统运行验收。没有执行真实云写操作、agent 安装、Telegram 外发或前端修改。

## 待决定与 PRD 建议

已提出的审计详情粒度问题仍待答复：是否保存非敏感配置前后值，凭据只记更换标记。本批字段名日志不是对该选择的替代，也无法事后还原未保存的值。建议 PRD 明确该粒度与审计保留期限。未改 PRD 原文。

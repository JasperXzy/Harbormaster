# 闲置资源告警与恢复

2026-11-17，接续[闲置候选与缓存](idle-resource-candidates.md)，实现 PRD F8.3、F8.8、F8.9 对闲置问题的本地记录、去重和恢复。未接通 Telegram dispatcher，未发送消息，也未操作真实云资源。价格与月参考费用仍未知，不把状态候选说成已经产生的实际浪费金额。

## 规则与证据

新增固定规则 `idle_resource_detected`，默认启用、非紧急、参数为空。现有管理员告警规则 GET/PATCH 可查看和开关；不接受无关阈值。生产规则共十二项，开发模式也提供该规则。

只在后台发现事务接受了新的、成功的完整来源列表且查询起点距提交不超过 10 分钟时判断；GET 和周期告警评估均不会凭旧缓存新建闲置事件。逐条分类区分候选、明确非候选和无法判断 ID，不增加阿里云请求。

| 观测/设置 | 行为 |
|---|---|
| 首次明确候选 | 创建告警 episode 及通知记录 |
| 相同资源仍为候选 | 同一问题只记录一次；理由或显示名变化不重复提示 |
| 原始字段不足、请求失败、结果过期 | 保留当前问题，不假称恢复，也不重复创建 |
| 新结果明确不再符合候选条件 | 记录一次恢复；之后再次符合可进入新 episode |
| 完整成功列表不再返回该资源 | 静默结束问题，取消该资源待发记录；不据此声称已删除或计费结束 |
| 停用规则/账号，移出监控地域 | 静默结束问题并取消待发；重新启用需新观测，不回放缓存 |
| 仅更换账号凭据 | 候选 API 继续隐藏旧凭据证据，逻辑问题保持去重；新凭据成功读取后才确认恢复或继续 |
| 每日静默或维护期 | 非紧急记录按既有策略抑制，结束后不补发 |

10 分钟沿用候选来源的新鲜度界限，是本地判断上限，不是阿里云数据截止时间或计费水位。资源不再返回及监控取消沿用现有告警的“不适用”语义，与明确健康恢复分开。

## 数据与接口

迁移 `045_idle_alerts.sql` 增加 `idle_alert_target`，以账号/地域/类型/云 ID 标识逻辑资源，保存最近接受的观测起点和可选实例关联。逻辑目标不按凭据版本拆分，防止换密钥把同一问题重新通知；候选事实仍按原来的凭据版本隔离。

`notification.resource_id` 为可空字段，关联这个逻辑目标，适用于 EIP、磁盘及实例。现有 `instance_id` 只在候选关联唯一且已纳管的实例时填写；多实例挂载不任意选择一个。来源缓存、实例首次发现、目标、告警状态与 outbox 同事务提交，先写入实例再建立关联。周期评估仅保持监控范围内的目标为未知，不从缓存生成新的异常或恢复。

消息继续使用固定占位文字，仅包含逻辑目标、资源种类、原因枚举和 `monthly_cost=unestimated`；不插入用户名称、IP、访问密钥或未经核实的金额。前端可通过 `resource_id` 与闲置候选关联；候选已消失时历史关联仍保留。契约变更已先写入 OpenAPI 顶部记录，未修改 web/ 或 docs/design/。

## 本地验证

专门测试覆盖同一问题去重、明确恢复与复发、独立资源取消、未知/失败/过期不恢复、换凭据、账号/地域/规则开关、每日静默和维护不补发、首次发现实例关联，以及通知失败时整笔事务回滚。分类器测试同时检查明确非候选 ID 被保留，避免把“已不再闲置”误当作“资源消失”。管理员 API 检查默认开关、非紧急属性与参数拒绝。

实际命令与结果：

```text
go test ./internal/store ./internal/model ./internal/cloud/aliyun ./internal/api ./internal/devmode -run 'TestIdle|TestAlert|TestNotifications|TestDemoIdle|TestDevelopmentContractAndSessionBoundary' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 0.887s
ok github.com/JasperXzy/harbormaster/internal/model 0.166s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 0.190s
ok github.com/JasperXzy/harbormaster/internal/api 0.565s
ok github.com/JasperXzy/harbormaster/internal/devmode 0.867s

go build ./...
（exit 0，无输出）
go vet ./...
（exit 0，无输出）

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 62ms
Woohoo! Your API description is valid.
（exit 0）
```

全量检查实际输出（exit 0；下列为本轮非缓存测试，其余有测试的包均为 ok cached，命令包等为 no test files）：

```text
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 10.444s
ok github.com/JasperXzy/harbormaster/internal/agentrelease 6.417s
ok github.com/JasperXzy/harbormaster/internal/api 136.087s
ok github.com/JasperXzy/harbormaster/internal/backup 47.300s
ok github.com/JasperXzy/harbormaster/internal/cli 23.627s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.491s
ok github.com/JasperXzy/harbormaster/internal/collect 14.898s
ok github.com/JasperXzy/harbormaster/internal/devmode 14.691s
ok github.com/JasperXzy/harbormaster/internal/heartbeat 3.404s
ok github.com/JasperXzy/harbormaster/internal/m0 1.469s
ok github.com/JasperXzy/harbormaster/internal/model 1.379s
ok github.com/JasperXzy/harbormaster/internal/notify 2.376s
ok github.com/JasperXzy/harbormaster/internal/power 23.664s
ok github.com/JasperXzy/harbormaster/internal/probes 14.122s
ok github.com/JasperXzy/harbormaster/internal/release 17.704s
ok github.com/JasperXzy/harbormaster/internal/retention 2.468s
ok github.com/JasperXzy/harbormaster/internal/store 212.269s
ok github.com/JasperXzy/harbormaster/internal/traceinfo 8.013s

git diff --check
（exit 0，无输出）
```

首轮专测因新测试误用了不存在的 DTO 字段 LastErrorCode 而编译失败；改为已有 LastError.Code 后，以上专测全部通过。此前遗留的 lint 会话在本轮已不存在，未将其当作成功结果；上面是本轮重新运行的检查。

真实账号的候选来源读取已在前一阶段验证，但没有闲置正例。本阶段只有本地合成告警验证，没有真实 Telegram 送达验收；不会为造正例而停止实例、解绑 EIP 或磁盘。

PRD 修订建议：F8.9 增补“明确非候选才恢复；查询失败/字段未知保留状态；资源不可见和监控关闭静默结束”的证据语义，F9.4 继续区分状态候选、可靠报价月参考值和实际账单费用。PRD 原文未改。

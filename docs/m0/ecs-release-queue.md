# ECS 释放持久任务

2026-11-13：在已确认的停止/PostPaid 范围和[影响预览](ecs-release-preview.md)之上，完成持久任务的入队、二次确认、去重、租约、发送边界和结果存储协议。尚无面板/Telegram 提交入口、执行 worker 或云端缺失确认步骤；execution_enabled 仍为 false。没有真实释放或 DryRun。

## 入队与确认

先在 OpenAPI 增加 instance_release 和 ECSReleaseJobResult，再实现存储。EnqueueReleaseJob 在同一事务读取当前实例、账号和凭据版本隔离的预览，要求预览仍 eligible、token 完全匹配、输入名称与真实云名称逐字符相同。不修剪或折叠名称、不以实例 ID 替代空名称。API Key 不拥有释放权限，即使已有 instance:control 也拒绝。

持久化目标的账号、配置修订、凭据版本、地域、云实例 ID、确认名称、预览 token/时间及完整影响，不从后续调用参数重建目标。不会在释放入队时改写电源期望状态；释放属于独立操作，不伪造开/关机意图。

事件键沿用既有动作键格式：16–64 位字母、数字、下划线或连字符。相同键、相同确认在预览过期、完成或恢复后返回原任务；相同键不同参数报 idempotency_key_conflict。新事件键遇到相同活动确认时关联原任务；不同活动操作报冲突。保存任务和事件键的事务失败会整体回滚。

同一实例的活动电源任务与释放任务互斥；两种入队路径都检查另一种，不依赖进程内锁。释放排队或执行时不能新建电源动作；已有电源动作时不能入队释放。未来自动化和 Telegram 入口也必须复用这些入队方法。

## 领取与发送边界

ClaimReleaseJob 独立于采集、电源、心跳及网络 worker，只能领取释放任务。发送前租约一分钟、最多领取三次；过期 owner 不能提交结果。通用 FinishJob / RetryJob 拒绝释放，不能借用只读请求的重试语义。

BeginReleaseDispatch 要求租约有效，并在事务中复核账号启用、修订、凭据版本、地域、目标仍存在。调用者必须交付最近 30 秒内的完整只读结果；未来时间、失败、不完整、错误地域均拒绝。按已保存云 ID 重新计算影响，只有名称、状态、计费、保护/锁和完整磁盘影响与入队证据一致时才写发送标记。任何变化以明确失败结束，保持 not_sent。

五分钟预览有效期用于入队；排队后不能凭旧缓存直接发送，必须再次取得上述新证据。30 秒、租约和发送前最多三次领取是本地工程边界，不代表 PRD 的保护响应时间已验收。

发送标记与 dispatching 必须成功提交，才允许下一阶段的 worker 调用独立固定 DeleteInstance 客户端。标记仅能写一次。云响应按稳定分类保存：

| 结果 | 任务状态 |
|---|---|
| not_sent / rejected / unknown | failed，保留错误和递送证据，不自动重发 |
| accepted | 继续 running，尚未证明实例消失 |
| dispatching 后租约失效 | 改为 unknown 并失败，禁止重新发送 |
| accepted 后租约失效 | 保留 accepted，失败为 release_confirmation_interrupted，禁止重新发送 |

RecordReleaseDispatch 只允许固定结果/错误码组合，拒绝原始 SDK 消息。后续完整、定向只读查询确认步骤尚未实现；当前代码不会把受理直接写成 succeeded。

恢复沿用用户已确认规则：排队和执行中释放任务全部取消为 backup_restored、清空租约；处于 dispatching 的记录先改为 unknown，accepted 证据保留。事件键仍关联取消后的原任务，旧客户端重试不能重放。

请求、开始、发送边界、结果及结束的固定字段审计与任务状态同事务，不写确认名称、影响 JSON、token、云错误原文或凭据进审计。

## 迁移 043

SQLite 的 job.type CHECK 必须扩展。迁移在原事务中复制/重建 job，只增加 instance_release；所有现有入站外键都是 NO ACTION。保留 child 表原行，保留 job_change/job_order 序号、通知关联、CDT 观测、代理任务、心跳、线路历史、刷新键及意图关系；不把旧任务重新插入成新事件。

foreign_keys 始终 ON，替换父表期间仅延迟外键检查。在重建数据和原有 job/SSE/审计/延迟清理触发器后，使用 pragma_foreign_key_check 的完整结果和 CHECK(n=0) 断言校验，再关闭延迟模式并返回立即检查。显式校验不可省略：SQLite DROP 父表留下的延迟计数即使对应键已恢复也可能阻止提交；本批先在临时 Python SQLite 中观察到这一行为，再用项目实际驱动验证完整迁移。没有使用 writable_schema 或在生产关闭 foreign_keys。

新表 instance_release_job 保存已确认影响与执行阶段；instance_release_request 保存长期事件键。恢复/租约失败仍保留历史和外键关系。新增释放审计触发器不改变旧电源和延迟清理行为。

迁移测试验证既有任务、关联、审计和缓存逐值未变、后续任务序号递增、旧电源审计继续生效、foreign_keys=1 / defer_foreign_keys=0 / 无外键错误。另在隔离测试库制造旧悬空引用，验证整个迁移失败回滚，新表及迁移标记均不残留；该测试不修复或丢弃原损坏行。此前从旧版升级的代理/心跳/通知迁移测试也通过。

依据：[SQLite 外键与 DROP TABLE](https://www.sqlite.org/foreignkeys.html#fk_schemacommands)。延迟计数的具体行为是本批实验结论，不是云能力或业务假设。

## 验证与范围

本地测试覆盖精确名称、过期确认重试、并发重复入队、双向操作互斥、API Key 拒绝、数据/名称/状态/属性变化、账号修改/停用、过期 owner、三次发送前领取上限、单次发送标记、稳定错误码、受理后不冒充完成、恢复取消和幂等历史。

实际定向命令（均 exit 0）：

```text
go test ./internal/store -run 'TestRelease|TestRestoredRelease|Test.*Migration' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 1.870s

go test ./internal/store ./internal/model -run 'TestRelease|TestRestoredRelease|Test.*Migration' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 2.307s
ok github.com/JasperXzy/harbormaster/internal/model 0.551s
```

最终检查（均 exit 0）：

```text
go build ./...
（无输出）
go vet ./...
（无输出）
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 10.385s
ok github.com/JasperXzy/harbormaster/internal/api 126.018s
ok github.com/JasperXzy/harbormaster/internal/backup 46.978s
ok github.com/JasperXzy/harbormaster/internal/cli 21.260s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.464s
ok github.com/JasperXzy/harbormaster/internal/collect 14.930s
ok github.com/JasperXzy/harbormaster/internal/devmode 15.013s
ok github.com/JasperXzy/harbormaster/internal/heartbeat 3.543s
ok github.com/JasperXzy/harbormaster/internal/m0 1.478s
ok github.com/JasperXzy/harbormaster/internal/model 1.341s
ok github.com/JasperXzy/harbormaster/internal/notify 2.331s
ok github.com/JasperXzy/harbormaster/internal/power 23.206s
ok github.com/JasperXzy/harbormaster/internal/probes 13.604s
ok github.com/JasperXzy/harbormaster/internal/retention 2.377s
ok github.com/JasperXzy/harbormaster/internal/store 192.569s
ok github.com/JasperXzy/harbormaster/internal/traceinfo 7.988s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。
npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 81ms
Woohoo! Your API description is valid.
git diff --check
（无输出）
```

前端只需认识新增任务类型；尚不能启用释放提交按钮。PRD 修订建议沿用预览文档，并补充 F7.5 的未知结果不重放、恢复取消与受理/确认分离；PRD 原文未改。

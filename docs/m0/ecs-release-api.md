# 管理员 ECS 释放 API 与生产接入

2026-11-14：管理员异步提交、独立生产开关及 serve worker 已接通，并完成本地模拟端到端测试。默认及示例配置均为关闭。没有启动开启真实释放的服务，没有执行真实释放、DryRun 或新增真实云查询。

## 接口与范围

先更新契约和顶部记录，再实现 `POST /api/v1/instances/{instance_id}/release`：

- 仅管理员会话，经过现有 HTTPS、主机/CIDR、同源和 CSRF 校验；不提供 bearer API Key 释放入口。
- 必须有且仅有一个 Idempotency-Key，格式沿用持久动作事件键。
- JSON 只允许 confirmation_name / preview_token。名称 1–128 个字符，必须与预览中的真实名称完全一致；token 为当前的 64 位小写十六进制指纹。
- 新确认必须有未过期且 eligible 的预览。无预览、过期、事实/账号变化、实例不在当前发现范围、运行中、包年包月、SWAS、锁/删除保护或资料不完整均不能新建任务。
- POST 只做 SQLite 入队，返回 202 / job_id；不会同步查询云或执行释放。共享实例互斥、重复确认及原任务返回沿用[持久队列](ecs-release-queue.md)。

名称不匹配/字段非法返回 422，当前证据改变返回 409 release_preview_changed，活动电源/释放冲突为 instance_release_busy，同事件键参数冲突为 idempotency_key_conflict。目标不存在返回 404。未启用时返回 503 instance_release_disabled，不会入队。

任务列表/详情与既有 SSE 可识别 instance_release。前端确认页应显示 GET release-preview 的影响与阻止原因，并同时检查 execution_enabled=true、status=eligible；不能只凭其中一项显示可执行。一次用户确认生成一个事件键，在网络失败重试时保留；完成/恢复后的同键仍返回原任务，不能因为 HTTP 响应丢失重新产生释放。

释放范围沿用用户确认：只支持 Stopped / PostPaid（包含抢占式）的 ECS，Force=false、TerminateSubscription=false；不自动停机、关闭删除保护、修改磁盘属性或单独释放 EIP/手动快照。精确影响与证据限制见[预览](ecs-release-preview.md)及[执行确认](ecs-release-worker.md)。

## 开关与进程边界

配置字段 instance_release_enabled，或环境变量 HM_INSTANCE_RELEASE_ENABLED。默认 false，两个已跟踪的部署示例显式 false；环境覆盖 JSON，非法布尔值拒绝启动。开发模式与 true 组合配置直接报错，运行时工厂也独立拒绝。

这是独立于 instance_power_enabled 的能力：开关机开启不会开启释放；释放可以单独启用但仍只能作用于已停止的合格目标。只有 serve、生产模式、显式开关、采集器及可用主密钥都具备时才构造并启动释放 worker。服务将实际工厂是否可用反馈给面板配置；SystemInfo.instance_release_enabled 和预览 execution_enabled 反映该能力，不代表云权限已验证。

run-once 和 M0 不调用释放工厂；即使加载的配置为 true，run-once 仍只读。开发模式返回合成预览和 capability=false，提交固定拒绝，不执行或模拟真实云副作用。服务结束会取消并等待释放 worker；已受理证据保留为中断，未知结果不重放。

本批没有替用户设置 true、修改外部真实配置、安装或部署服务。代码和示例可供审阅，真正开启与真实资源验证仍需具体授权。

## 已验证

本地 API 测试覆盖会话/CSRF、关闭开关、无事件键/重复头、非法 token、精确名称、禁止 Force 附加字段、目标不存在、去重与过期重试；提交期间 scanner 调用数为 0。使用模拟 writer/observer 把 HTTP 入队推进到受理和完整零行确认，任务详情出现 not_found_after_accept，writer 调用数为 1。

构造器测试验证开关、电源能力、采集器和开发模式的独立关系，只构造真实适配器而不运行它们。CLI 隔离测试使用无凭据的合成账号、关闭该账号的采集，在 instance_release_enabled=true 下实际运行 run-once，原释放任务保持 queued、attempts=0，日志 cloud_writes=0。没有任何真实账号参与这些测试。

首次手写 bearer 请求测试因缺少同源 Origin 先被既有前置校验以 403 拒绝；补齐 Origin 后验证没有 Cookie 返回 401，另验证重复动作头返回 422。没有放宽同源或认证校验。

实际定向命令（exit 0）：

```text
go test ./internal/api ./internal/cli ./internal/config ./internal/devmode -run 'Test.*Release|TestDevelopmentContractAndSessionBoundary' -count=1
ok github.com/JasperXzy/harbormaster/internal/api 1.311s
ok github.com/JasperXzy/harbormaster/internal/cli 0.569s
ok github.com/JasperXzy/harbormaster/internal/config 0.366s
ok github.com/JasperXzy/harbormaster/internal/devmode 1.175s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 131ms
Woohoo! Your API description is valid.
```

最终检查（均 exit 0）：

```text
go build ./...
（无输出）
go vet ./...
（无输出）
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 17.084s
ok github.com/JasperXzy/harbormaster/internal/api 187.174s
ok github.com/JasperXzy/harbormaster/internal/cli 36.587s
ok github.com/JasperXzy/harbormaster/internal/config 1.345s
ok github.com/JasperXzy/harbormaster/internal/devmode 24.287s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。
git diff --check
（无输出）
gofmt -l <本阶段修改的 Go 文件>
（无输出）
```

前端仍由 Claude 依据最新契约实现预览、名称确认及任务结果呈现；本批未改 web/ 或 docs/design/。Telegram 释放入口、真实云写权限/资源释放验收未做，不能把本地模拟通过计为这些验收完成。PRD 修订建议沿用此前释放设计记录，PRD 原文未改。

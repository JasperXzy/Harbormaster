# 外部心跳自监控

2026-11-12；PRD F16.10。用户已确认成功报活条件：面板就绪、SQLite 可用、采集调度循环最近仍有进展；单个账号的云接口失败不阻断整个服务报活。

## 行为与健康条件

生产 serve 启动独立 heartbeat runner；开发模式、run-once、迁移和备份命令不启动发送。初始 disabled、无 URL，默认周期 60 秒，允许 15–86400 秒。定时心跳不积压历史任务：每个到期周期重新检查当前健康状态，发送一次新心跳，不补发错过的旧周期。

每次成功发送前都要求：

1. 本机实际 HTTP listener 的 `/readyz` 返回 200 与 `status=ready`。这个现有接口会检查 SQLite 和当前迁移状态；心跳领取还需要成功提交数据库写事务。检查超时 2 秒。
2. collect.Engine 的调度循环在最近 30 秒内成功完成一次 Schedule。调度周期仍为 5 秒；启动前没有进度证据，不能报活。此进度不由独立心跳定时器更新，也不由账号查询成功率推断。
3. 配置修订、发送租约仍有效。解密失败或租约/配置变化不能发送；完成状态同样按修订和租约隔离。

这里的“面板就绪”是 hm-server 本机 HTTP/API readiness，不代表外部反向代理、DNS、浏览器页面或所有账号云 API 均可用。单账号失败由现有账号告警处理。上述健康门槛和 30 秒进度宽限不等于外部通知延迟承诺，外部检查周期/宽限期需与这里的周期和网络条件匹配。

## 协议、凭据与接口评审

参考 [Healthchecks Pinging API](https://healthchecks.io/docs/http_api/) 与 [URL 格式说明](https://healthchecks.io/docs/slug_urls/)，心跳通过 HTTP 请求信号工作，路径/查询可能包含秘密。实现固定 HTTPS GET，无请求正文、Cookie、附加认证 Header 或日志内容；不跟随重定向、不使用环境代理，不关闭 TLS 验证。单次 HTTP 超时 10 秒，整个一次处理最多 15 秒，无自动重试、无连接复用；后续周期可重新检查健康后尝试。2xx 是这次请求成功，429、重定向和其他错误分别使用固定错误码。响应正文与原始 URL 错误从不写日志/状态。

URL 只由已认证管理员配置，完整保存在通用 credential 表，AES-256-GCM，AAD=`system/heartbeat-url/v1`。主密钥仍从环境或数据目录外文件取得。沿用通用备份凭据重加密与主密钥校验机制，不增加明文配置文件、数据库 URL 列或特殊恢复密钥。

前端草案希望 GET/PUT 返回 url；本轮已在 OpenAPI 改为 **写入 url，读取 url_configured**，原因是心跳 URL 本身可能就是 bearer 凭据，不能当普通显示文本回读。未修改 web/ 或 docs/design/。

- `GET /settings/heartbeat`：返回 enabled、url_configured、interval_seconds、revision、updated_at、last_attempt_at、last_success_at、last_error，不发网络请求。
- `PUT /settings/heartbeat`：会话+CSRF，required revision/enabled/interval_seconds；URL 省略保留、空字符串清除且只能 disabled。乐观版本冲突 409；配置变更清除旧结果、取消旧测试并隔离旧 worker。
- `POST /settings/heartbeat/test`：会话+CSRF、空 JSON 对象，202+job_id。可以测试已配置但 disabled 的目标，也必须通过同一健康门槛。测试不能绕过故障检测去向外部服务发送假成功。
- 通用 JobType 新增 heartbeat_test；任务列表、详情、SSE 沿用既有机制。配置入口和测试入口只保存/入队，不同步发网络请求。

last_attempt_at 包含健康检查尝试，不意味着网络已发送；last_success_at 表示当前配置下健康门槛通过且 HTTPS 返回 2xx，不证明外部服务已发出或收件人已收到告警。代码只用占位错误码，前端负责文案。周期性心跳不写入大量通用 job；显式测试才产生 job。

## 持久化、并发与恢复

迁移 025 为通用 job 增加 heartbeat_test 类型，逐项保留 job_change、job_order、CDT、通知及电源请求/意图/API Key 归属关系，重建时不关闭外键约束；保留序号高水位和触发器行为。新增 heartbeat_settings（默认行、修订、周期、状态、下一次到期和租约）及 heartbeat_test（任务与配置修订关联）。

生产实例间通过数据库 30 秒租约共享发送资格，周期性发送和手动测试也使用同一租约。活动测试去重；配置变更使旧任务失败。已开始的 HTTP 请求无法撤回，但旧结果不能覆盖新配置。运行中测试在进程中断、租约失效后标 heartbeat_test_interrupted，不自动重放；尚未发送的排队测试可在当前健康检查通过后执行。

备份恢复沿用用户授权：取消旧未完成任务，同时清除心跳租约并重置下一次到期。保留加密 URL 和 enabled 策略；只有启动生产 serve 并通过当前健康门槛后才可能发送。测试证明通用备份重加密后 URL 可由新主密钥解开。

## 验证与未完成验收

本地测试使用临时 SQLite、模拟 sender、本机 HTTP readiness 服务和本机 TLS 接收端。未连接真实 Healthchecks / 其他外部监控，未触发第三方通知，未进行真实云写操作。

已验证：

- 配置加密、禁止明文回读、保留/清除 URL、CSRF、修订冲突、测试仅入队。
- 单次 HTTPS GET、无额外凭据、重定向拒绝、429/其他响应固定错误码、取消和不泄漏 URL。
- HTTP readiness 失败、调度进度过期/尚未开始时不发心跳；账号云查询失败不阻断调度进度。
- CLI serve helper 使用实际本机 HTTP readiness；HTTP 服务停止但调度器仍运行时不发送。
- run-once 不发心跳、不消费已排队测试；开发模式不启动 sender。
- 并发只领取一次、测试去重、配置修订隔离、运行中测试过期不重放、恢复取消旧任务/清除租约、跨主密钥重加密。
- 旧 schema 升级时有真实关联的电源记录、请求、意图和 API Key 归属保留；游标/SSE 序号高水位不重置。

另外在既有离线验证副本执行 `migrate`，由 schema 23 升级到 25：53 个 job、163 个 job_change、53 个 job_order、20 个 CDT 记录、4 个通知和 112 个云流量历史样本逐行完全保留，外键错误为 0。该副本的电源表为空，非空电源关联另由上述迁移测试验证。见[迁移证据](heartbeat-migration-evidence.json)。命令退出 0；没有启动发送 worker。

```text
HM_MASTER_KEY= \
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/production-discovery-q8fh4ddr.key \
go run ./cmd/hm-server migrate \
  --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/network-rates-offline-aqnc6ndj
exit 0
```

真实阶段验收仍需：在已有外部监控创建/选定检查，通过受保护的配置入口录入 URL（不要贴入聊天或仓库），配置外部宽限期与收件人；正式部署后确认连续收包，再经用户批准停掉 hm-server 并核对外部告警送达。前端设置界面和这项真实停服验收未完成；不能把本地测试列作已通过 M2 的外部告警验收。

## 实际工程检查

```text
go test ./internal/store -run TestHeartbeat -count=1
ok internal/store 0.527s

go test ./internal/api ./internal/collect ./internal/cli ./internal/heartbeat ./internal/store
ok internal/api 16.125s
ok internal/collect 0.894s
ok internal/cli 1.541s
ok internal/heartbeat 0.680s
ok internal/store 2.978s

go test ./internal/cli -run Heartbeat -count=1
ok internal/cli 0.732s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok internal/api 79.035s
ok internal/backup 43.471s
ok internal/cli 13.607s
ok internal/cloud/aliyun 4.750s
ok internal/collect 8.555s
ok internal/devmode 7.842s
ok internal/heartbeat 2.641s
ok internal/m0 1.757s
ok internal/model 1.617s
ok internal/notify 2.165s
ok internal/power 12.831s
ok internal/store 52.528s
其余包 cached 或 [no test files]；exit 0。

# 强化重定向错误码断言后复验（接收端和重定向目标均为本机测试服务器）
go test -race ./internal/heartbeat -count=1
ok internal/heartbeat 2.147s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 26ms
Your API description is valid.
```

调度进度测试的时间断言与实际 30 秒健康宽限保持一致，避免以不必要的一秒调度时限造成测试抖动；最后针对性复验：

```text
go test -race ./internal/collect -run TestSchedulerProgressContinuesDespiteCloudAccountFailure -count=1
ok github.com/JasperXzy/harbormaster/internal/collect 1.996s
```

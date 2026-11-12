# 静默时段与临时维护期

2026-11-12；PRD F8.10。用户已确认：静默期间的非紧急告警只保留记录，结束后不补发；额度类、保护执行和 agent 掉线不受影响。

## 配置与契约

新增 `GET/PUT /notify/quiet-hours`。每日时段使用安装向导保存的业务 IANA 时区，不接受本接口覆盖时区。初始 daily disabled，保留默认输入 23:00–08:00；这不会自动启用静默。

PUT 要求 revision、enabled、start、end、maintenance，使用管理员会话和 CSRF。revision 在事务内比较；旧版本更新返回 409 quiet_hours_changed。start/end 严格 HH:mm，允许跨午夜，含起点、不含终点；相同起止拒绝，避免把零时长或全天作猜测。每日按当前业务时区的墙钟小时/分钟判断，使用 Go 的 IANA 时区实现。

maintenance 为明确的 RFC3339 起止时刻或 null，存为 UTC；含起点、不含终点，end 必须晚于 start。它独立于每日 enabled 开关生效；清除维护期需提交 null。两个条件重叠时 active_reason 优先显示 maintenance。GET 返回当前 active、active_reason、timezone 和 updated_at，不写数据库、不创建任务。

前端原草案只包含每日 enabled/start/end，本轮补充了修订与维护期，符合 PRD 的两种静默方式；未修改 web/ 或 docs/design/。

## 通知行为

迁移 026 新增 quiet_hours，并为 notification 添加持久 urgency、suppression_reason、suppressed_at、内部 suppression_revision。旧记录按规则 urgent、已知豁免和 critical/test 分类回填紧急性，不改变原 delivery_status，不生成伪造的送达事件。

服务端紧急判定不能被调用方的 false 降级：

- `agent_offline`、已有契约中的 `cdt_usage`、`cdt_forecast_exceed`、`cdt_official_unavailable` 始终豁免。
- alert_rule.urgent=true、critical 严重级别或内部明确设置 Urgent 的通知豁免。
- 显式发送测试不是自动告警，始终豁免。
- 后续额度保护执行的生产者必须明确标为 Urgent；本轮仅验证该通路，不启用尚未实现的额度保护动作。

恢复通知沿用原规则的紧急性。前端可读取每条通知的 urgent，但不能编辑单条通知的紧急判定。

三处门槛：

1. **入队**：在生成通知及事件去重的同一事务中检查静默，非紧急记录直接存为 not_sent，并记录原因/时间。即使没有发送 worker，也不会在静默结束后恢复 pending。
2. **领取**：发送租约领取前，处理当前到期的非紧急待发记录。保存一个当前已生效的静默策略时，也处理到期且未领取的通知。
3. **发送授权**：新增 `AuthorizeNotification`，供 dispatcher 在真正网络发送前调用；重查渠道版本、通知/测试任务租约与静默。返回 false 表示已终止为 not_sent，不是延迟重试。

当前生产 Telegram dispatcher 仍未接通，受此前未答复的代理凭据更新和不确定送达重试策略影响。本轮实现并测试上述存储与发送前授权边界，不能将它称为真实消息投递验收。后续 dispatcher 必须调用发送前授权方法。

已领取的通知不会被设置保存操作直接改成“已阻止”，因为它可能已经被授权/开始发送。若授权发生在静默生效之后，当前 owner 会将其抑制；已经授权或开始的网络请求不能撤回，完成结果仍如实保存。已取消的发送测试不能通过发送前授权。

静默不取消云资源操作任务。只改变通知是否发送，不改变期望状态、采集、告警状态或额度保护判断。

## 记录、去重与不确定性

复用终止状态 not_sent，不新增待补发状态。其含义扩展为“只记录不发送”，可能来自未配置渠道、静默或其他既有取消条件；前端需结合 suppression_reason、suppressed_at、last_error 展示，不能把所有 not_sent 都显示为“没有配置渠道”。

关闭静默、维护期结束、重启、备份恢复或再次入队同一 event_key 都不复活已抑制记录。新的告警/恢复事件仍按其生成/发送时的策略正常处理。

若之前有结果不确定的发送尝试，静默只阻止后续尝试，不抹掉 has_uncertain_attempt。未曾发送而在当前 owner 的发送前检查中被抑制，不会仅因为领取过租约而虚构不确定送达。

## 验证与边界

本轮没有向 Telegram 或其他真实服务发信，没有任何云写操作。

已测试：跨午夜和起止边界、业务时区、维护期独立生效/结束/优先级、非法时间与冲突范围、修订冲突、CSRF、完整替换校验、静默入队不补发、重复事件不复活、领取时生效、领取后跨静默边界的发送前检查、已授权请求结果保留、紧急与测试豁免、未知送达证据保留、旧数据库 urgency 回填不改送达状态或事件序列，以及 GET/PUT 不创建工作任务。

静默时段的数据与策略随现有数据库备份保存。实际生产发送与前端文案/表单尚需后续联调；本轮没有代替尚未完成的 Telegram worker，也没有把模拟测试当作真实告警送达。

实际工程检查：

```text
go test ./internal/store ./internal/model -run Quiet -count=1
ok internal/store 0.694s
ok internal/model 0.572s

go test ./internal/model ./internal/store ./internal/api ./internal/notify ./internal/devmode
ok internal/model 0.124s
ok internal/store 2.785s
ok internal/api 15.844s
ok internal/notify (cached)
ok internal/devmode (cached)

go build ./... && go vet ./... && git diff --check
exit 0，无输出（发送测试任务授权复查补充后也已通过）。

go test -race ./...
ok internal/api 78.222s
ok internal/backup 40.435s
ok internal/cli 12.697s
ok internal/collect 8.189s
ok internal/devmode 6.996s
ok internal/heartbeat 2.780s
ok internal/model 1.563s
ok internal/notify 2.185s
ok internal/power 13.008s
ok internal/store 57.261s
其余包 cached 或 [no test files]；exit 0。

# 发送前授权补充测试任务状态/租约检查后的针对性复验
go test -race ./internal/store -run 'TestQuiet|TestNotificationAuthorization' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 4.404s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 77ms
Your API description is valid.
```

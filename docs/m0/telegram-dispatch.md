# Telegram 实际发送

2026-11-18。接通 Telegram 渠道的生产写入口、测试发送和发送 worker；告警正文改为按前端模板渲染。用户本次确认了三个此前待答的问题：

| 问题 | 用户选择 |
|---|---|
| 发送后响应丢失、结果不确定 | 不自动重试，标记失败并保留不确定标记，由管理员决定 |
| 代理认证更新 | 代理模式和地址不变时，省略即保留；任一改变即清除旧认证，除非同一请求提供新认证 |
| CDT 单位未核实期间的用量阈值告警 | 暂不启用，只启用有独立证据的规则 |

## 接口

- `PUT /api/v1/notify/telegram`：会话＋CSRF。Bot Token、代理用户名/密码只写，响应只返回 `bot_token_set`、`proxy_auth_set`。首次保存必须提供 Token；之后省略表示保留。用户名和密码必须成对提交，两个空串表示显式清除。校验失败只返回字段名与固定错误码，不回显提交值。配置变化时状态回到 `not_tested`，未发出的旧通知按 `telegram_configuration_changed` 失败，不会改投到新对话；内容不变的保存不递增版本。
- 同一事务写入审计 `telegram_channel_updated`，只记录 `chat_id`/`proxy_mode`/`proxy_url` 是否变化及 `credentials_changed` 布尔值。
- `POST /api/v1/notify/telegram/test`：返回 `202 + job_id`。未完成的测试复用同一任务。测试文案取自 `docs/design/telegram-templates.md`，不受静默时段影响。没有配置时返回 409 `telegram_not_configured`。

## 发送 worker

`internal/notify/Dispatcher` 在生产 `serve` 中随主密钥可用时启动，每秒领取一条到期的通知：

1. `ClaimNotification(retryUncertain=false, maxAttempts=5)`：上一进程留下的过期发送租约直接记为失败、`has_uncertain_attempt=true`，不重发。
2. 解密当前渠道；版本不一致按配置已变化失败。
3. `AuthorizeNotification` 在发送前重新检查租约和静默时段；静默期间的非紧急通知终止为 not_sent，不补发。
4. 调用发送层。成功需要明确的 message_id。
5. 结果处理：
   - 请求未写出的连接失败：按 10 秒、30 秒、2 分钟、10 分钟退避重试。
   - 429：按 Telegram 给出的 `retry_after` 重试。
   - 最多 5 次。
   - 结果不确定（请求已写出后断开、无效响应）：记为 `telegram_delivery_uncertain`，不再自动发送。
   - 其他明确拒绝（401/403/chat not found/群迁移等）：直接失败。
6. 结果、测试任务状态和渠道健康状态在同一事务提交。

本地事件键去重不等于 Telegram 端 exactly-once；按用户选择，宁可漏发一条也不自动产生重复消息。

## 文案

告警标题和正文按 `docs/design/telegram-templates.md` 的规则渲染：级别符号、账号/实例/测速组合的名称（缺失时回退到 ID）、业务时区时间、GB 一位小数。面板通知记录与 Telegram 是同一段文字。

- 已有模板：`agent_offline`、`account_api_failure`、`cdt_official_unavailable` 及测试消息。
- 模板里依赖尚未启用功能的半句不发送：掉线时“按之前最高速度估算”、恢复时“已用真实数值替换估算”、官方数据中断时“继续估算”。只陈述实际状态。
- 没有模板的规则（到期、规格带宽、余额、公网 IP、网络、线路、闲置、额度保护、估算偏差）按同一风格写了中性的中文句子，不含估算金额或未经验证的判断。前端定稿后替换即可，发送逻辑不变。
- 额度保护在演练、未授权执行或关闭云写入时，明确写“这次只是演练，没有关机”。

`cdt_usage`、`cdt_forecast_exceed` 仍不启用。

## 前端

- Telegram 表单增加代理认证输入；地址改变时提示保存后会清除旧认证。
- 通知页增加 `not_tested` 状态“已保存，还没测试”，补充新错误码的中文。
- 模拟服务器同步了保存后变成 `not_tested` 的行为。

## 验证

```text
go vet ./...                   exit 0
go test -race ./...            全部通过（internal/notify 13.2s、internal/api 151.4s、internal/store 277.3s）
npm run lint:api               valid
npm run typecheck / test       10/10 通过
npm run build / build:mock     通过
```

新增测试覆盖：成功只发一次并记录渠道健康；明确未发送与 429 的退避时刻；结果不确定不重发；崩溃留下的过期租约不重发；永久拒绝和次数用尽；测试任务结果；Token 保留、同地址保留认证、换地址清除认证、半份认证拒绝、错误不回显秘密、审计不含秘密；API 的 CSRF、写后只读响应、校验错误和测试任务复用；告警正文与恢复正文。所有测试都使用替身发送器，没有联系真实 Telegram。

## 生产状态

部署前只读检查：生产库没有 Telegram 渠道；已有 3 条通知（1 条到期、2 条规格带宽）均为 not_sent。新 worker 不会补发这些历史记录。用户在通知页保存 Bot Token 和 Chat ID 并点“发送测试消息”之后，新产生的告警才会实际发送。

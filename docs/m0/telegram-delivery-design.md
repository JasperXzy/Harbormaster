# Telegram 发送层与待确认的队列语义

2026-11-12。已实现并测试独立发送层 `internal/telegram`，尚未接入生产通知配置 API、告警引擎或 outbox。没有向真实 Telegram 聊天发送消息，没有读取真实 Bot Token。

## 已实现的发送行为

- 固定调用 HTTPS `sendMessage`，JSON 纯文本，禁用链接预览，不设置付费广播。不接受任意 API 方法；文本长度校验为 1–4096 个 Unicode 字符。
- 直接连接、HTTPS 反向代理、带认证 SOCKS5 三种传输。SOCKS5 使用代理端解析 Telegram 域名；认证只发给配置的代理。忽略进程的 HTTP_PROXY 环境变量，凭据不依赖全局代理状态。
- 不跟随任何重定向，不允许 URL 中带用户名密码、query 或 fragment。前端如何提交代理认证仍待用户决定；内部发送层已将认证与地址拆开，所以不会回显 secret URL。
- HTTP 超时 15 秒，连接/TLS 超时 5 秒；TLS 最低 1.2，不提供跳过证书校验选项。
- 只返回固定错误代码，不回传 HTTP 错误中的 bot URL、Telegram 的 description 或代理认证；chat not found 仅映射为 telegram_chat_not_found。
- 返回成功时保存 message_id；429 按 retry_after 提供等待时间。确定连接前失败、已发请求后响应丢失、无效/超大响应分别标记，发送层自身不做重试。

以上协议依据 [Telegram Bot API](https://core.telegram.org/bots/api#making-requests)、[sendMessage](https://core.telegram.org/bots/api#sendmessage) 和 [ResponseParameters](https://core.telegram.org/bots/api#responseparameters)。

## PRD 修订建议：防重复与网络不确定性

PRD F8.7 要求失败重试，F14 要求防重复。sendMessage 的公开参数没有客户端幂等请求键；请求已经发出后若响应丢失，无法根据本地错误证明 Telegram 没有发送。唯一事件键只能防止本地重复入队，不能让远端发送变成严格 exactly-once。

已向用户提出选择，尚未收到回答：建议仅重试明确未发送或限流的情况，发送结果不确定时记录失败并由用户决定是否重试；另一选择是自动重试不确定结果并接受偶发重复。实现 outbox 时需明确这一语义，不宣称在网络响应丢失情况下绝不重复。

其他已提待答：代理认证字段更新规则；CDT 计量未确认期间是否暂不启用用量阈值告警。发送层实现不依赖这两项最终选择。

## 文案对接

Claude 的 `docs/design/telegram-templates.md` 当前草稿包含“按之前最高速度估算”和“已用真实数值替换估算”的掉线/恢复正文。当前生产估算因 CDT 截止时间未确认而未启用，所以不能直接发送这两句；后端将按现有约定先用占位文字，只陈述实际状态，等待前端按未启用估算分支定稿。不修改前端文案文件。

## 测试边界

本地测试使用 HTTP transport doubles 和回环 SOCKS5 服务，不接触 Telegram：验证请求目的地、纯文本与禁用解析、成功 message_id、错误中不泄露 bot token、连接失败与响应丢失区分、429 retry_after、反代认证、不跟随重定向、无效地址和长文本在联网前拒绝，以及 SOCKS5 认证/远端域名解析握手。

设置存储必须沿用 AES-256-GCM，Bot Token 与代理认证仅允许写入，GET 只返回是否已设置。生产配置、异步测试任务、持久化通知和告警仍待下一批实现；本文不将发送层测试当作这些功能已经完成。

实际验证：

```text
go build ./...                       exit 0
go test -race ./internal/telegram    ok 1.641s
go test -race ./...                  exit 0
  internal/api                       24.305s
  internal/cli                       2.840s
  internal/store                     5.188s
  internal/telegram                  cached（上面的独立检查已执行）
  其他有测试的包                      cached / passed
go vet ./...                         exit 0
git diff --check                     exit 0
```

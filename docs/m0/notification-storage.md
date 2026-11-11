# 通知加密存储、队列与只读接口

2026-11-12。此批实现配置存储、队列事务和 GET 接口；生产配置写入、发送 worker、告警评估尚未接通，未向真实 Telegram 发送任何消息。此前关于代理更新、CDT 用量告警和不确定结果重试策略的问题仍待用户回答。

## 配置

迁移 006 增加 telegram_channel 与 notification 表。Bot Token、Chat ID、代理地址和认证组成完整配置，使用 AES-256-GCM 加密后写入 SQLite，AAD 为 `notify/telegram/credentials/v1`。配置替换使用 revision 比较，陈旧写入被拒绝；内容不变的保存不递增版本，也不取消现有任务。读取旧配置必须能成功解密，错误密钥不能覆盖该渠道已有凭据。

修改渠道配置会在同一事务中取消旧的 pending 通知及关联测试任务；已领取的记录带不确定标记，因为网络请求可能已经发出。旧 worker 无法把旧回执记到新渠道，也不会把旧待发消息改投新收件人。没有渠道时记录为 not_sent，之后新增渠道不追发这些历史记录。

代理字段如何保留/清除由后续 API 层根据用户回答处理；本批只接收内部完整配置，不自行确定浏览器 PATCH 语义。

## 队列

- 唯一 event_key 防止同一事件重复入队。通知和异步 Telegram 测试 job 在同一事务创建，重复点击尚未完成的测试会获得同一个 job。
- 发送租约为 60 秒，每次领取生成新 owner。两个数据库连接并发领取只能有一个成功；过期或配置失效的 owner 不能提交结果。
- 采集 worker 只领取 refresh/permission_check，不再将 telegram_test 当作未实现的采集任务判失败。公开测试 job 的结果由通知事务更新。
- 记录尝试次数、最早重试时间、最后安全错误码、成功 message_id、送达时间与历史不确定标记。重试时间向上取整至秒，不早于调用者指定的 retry_after。
- 过期发送租约不等同于“未发送”。领取函数要求调用者显式提供 retryUncertain；测试覆盖停止与重新领取两条分支，但尚未选定或启用生产策略。最大尝试次数同样由调用者提供（1–20），不是向用户暗示已有默认策略。
- 成功/失败、通知状态、测试 job 和渠道健康状态在同一事务更新。状态成功只能在收到明确 message_id 后记录。

`has_uncertain_attempt` 表示历史上至少一次发送没有得到确定回执；后续成功也保留此标记，此时意味着存在重复的可能，而不是最新发送仍不确定。本地事件去重不等于 Telegram 端 exactly-once，见 telegram-delivery-design.md。

## 已接通的只读接口

`GET /api/v1/notify/telegram` 要求管理员会话。只返回配置状态与安全元数据，不返回 Token 或代理用户名/密码。proxy_auth_set 表示已保存认证。新增状态 not_tested：配置保存后尚无成功发送证据，不能显示 ok。改变配置会清除旧健康状态；配置不变则保留。

`GET /api/v1/notifications` 从数据库按新到旧分页，limit=1–200，游标包含内部序号。返回消息内容、发送状态、尝试数、安全错误码、送达时间和不确定标记；不暴露去重事件键、租约 owner、渠道版本或 provider message_id。

GET 不产生发送任务、不联系 Telegram。生产环境尚无通知数据时 items=[]，不使用示例通知填充。POST 测试、配置保存和告警规则接口仍待下一步接入。

OpenAPI 顶部已记录这些只读字段和 not_tested 状态。前端需要重新生成类型并补充“已保存，尚未测试”的展示，以及失败记录的不确定提示；Codex 未修改 web/ 或 docs/design/。

## 实际验证

```text
go build ./...                       exit 0
go test -race ./...                  exit 0
  internal/api                       25.171s
  internal/cli                       2.632s
  internal/notify                    1.574s
  internal/store                     5.518s
  internal/telegram                  1.465s
  其他有测试的包                      cached / passed
go vet ./...                         exit 0
npm --prefix web run lint:api         Your API description is valid.
git diff --check                     exit 0
```

测试覆盖 SQLite/WAL 中不出现明文配置凭据、错误密钥与陈旧版本拒绝覆盖、相同配置不取消任务、配置变更隔离旧 worker、测试任务原子创建/去重、并发领取、重开数据库后的租约状态、不确定策略的两条分支、重试期限/次数、记录分页和 GET 鉴权/脱敏。测试使用本机临时数据库与假凭据。

## 后续主密钥校验更新

上述存储阶段发现的全库主密钥一致性问题已修复，见 [生产主密钥绑定](master-key-binding.md)。错误密钥会在生产业务启动前被拒绝，旧库只有在已有可逆凭据全部认证成功后才建立绑定。

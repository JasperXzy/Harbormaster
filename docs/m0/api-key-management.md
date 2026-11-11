# API Key 管理与 scope 鉴权

2026-11-12，PRD F13.1。已实现生产管理接口、持久化和独立 scope 鉴权组件。小组件摘要字段仍待用户答复；摘要、实例控制和 cron 消费入口尚未接入，不能把创建密钥理解为已启用这些操作。

## 管理接口

- `POST /api/v1/api-keys`：管理员会话、同源 Origin 和 CSRF 保护。输入 name 与 scopes；返回 api_key 元数据和只出现一次的 token。
- `GET /api/v1/api-keys`：管理员会话；按创建顺序倒序游标分页，包含已撤销记录。返回 id、name、scopes、created_at、last_used_at、revoked_at；不返回 token 或其哈希。
- `DELETE /api/v1/api-keys/{key_id}`：管理员会话与 CSRF；软撤销，重复撤销保持原撤销时间，未知 ID 返回 404。

scope 只接受 `summary:read`、`instance:control`、`cron:run`，至少一个，不接受重复值、空集、通配符或自定义 scope。创建后授权集合不可修改；改变权限时创建替代密钥并撤销原密钥。按当前 PRD 的可撤销模型，没有新增自动到期策略，密钥持续有效直到手动撤销。

所有响应使用 no-store。创建响应若丢失，服务端不能恢复明文；用户可通过列表识别并撤销该记录后重新创建。前端应仅在创建结果中展示/复制密钥，不写入持久浏览器存储或调试日志。开发模式提供不含秘密的列表示例，继续保持原有只读模式，创建/撤销返回 501。

## 存储与权限边界

迁移 010 增加 api_key 表。密钥包含 crypto/rand 生成的 256-bit 随机值，数据库只存整个 token 的 SHA-256；独立随机 key ID 用于列表与撤销。API Key 遵循 F13.1 的哈希要求，不存可恢复的明文或密文。

`Store.AuthenticateAPIKey` 要求调用方指定一个已知 scope；只匹配未撤销的 token 哈希，不存在、已撤销或其他类型凭据返回未认证，scope 不足返回 insufficient_scope。只有成功校验 scope 才更新 last_used_at；该时间不等于云动作完成时间。查询与更新时间在同一事务中完成。

密钥不能替代管理员 Cookie、初始化 token 或 agent token。管理接口继续受管理员来源 IP 限制；未来小组件入口将单独遵循 F15.8 的来源例外，不能通过放宽整个面板实现。日志敏感字段过滤已覆盖 api_key。

本阶段没有创建真实用户密钥、添加云写动作、调用 Telegram 或改变既有阿里云只读白名单。测试密钥仅存在于本机隔离测试数据库。

## 验证

```text
go build ./...                       exit 0（无输出）
go vet ./...                         exit 0（无输出）
go test -race ./...                  exit 0
  internal/agentcli                  1.432s
  internal/api                       27.884s
  internal/cli                       2.834s
  internal/collect                   2.605s
  internal/devmode                   5.552s
  internal/logging                   1.353s
  internal/notify                    1.549s
  internal/store                     8.458s
  其他有测试的包                     cached / passed
npm --prefix web run lint:api        Your API description is valid.
git diff --check                    exit 0
```

针对性测试覆盖：数据库确实只存哈希、重启后鉴权、scope 精确匹配及非法配置拒绝、失败鉴权不更新使用时间、撤销立即阻断后续鉴权且保留历史、并发新增不扰乱后续分页、管理员认证与 CSRF、创建响应 no-store、列表不泄露秘密、拒绝调用方指定 token、API Key 不能作为管理员会话或 agent token、日志字段脱敏。

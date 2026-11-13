# API Key 管理与 scope 鉴权

2026-11-12，PRD F13.1。已实现生产管理接口、持久化和独立 scope 鉴权组件。小组件摘要字段仍待用户答复；实例控制消费入口现已接入（见下节），摘要和 cron 消费入口尚未接入。电源执行仍须由运维显式启用。

2026-11-13 补充：[API Key 只读刷新](api-key-refresh.md)现已接入。集成任务查询增加“此 Key 明确加入的刷新任务”；下文最初的仅电源范围按此增量扩展，电源归属与撤销规则不变。

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


## API Key 实例控制

生产增加两个独立入口：

- `POST /api/v1/integrations/instances/{instance_id}/power`：与面板电源接口相同的 action / stop_mode 请求体和 Idempotency-Key，返回 202 + job_id，仅本地入队。
- `GET /api/v1/integrations/jobs/{job_id}`：查询该 Key 创建的电源任务；其他 Key、面板及非电源任务返回 404。管理员仍可从原 /jobs 接口读取全部记录。

仅接受单个 `Authorization: Bearer <API Key>` 请求头，要求 instance:control；summary:read 和 cron:run 不授予控制权限。不存在、撤销或格式错误返回 401 + WWW-Authenticate，scope 不足返回 403 insufficient_scope。遵循 [RFC 6750 的 Bearer 头部认证方式](https://www.rfc-editor.org/rfc/rfc6750)，不实现 OAuth 授权服务器，不从 URL/JSON/Cookie 提取 token。

集成入口复用 HTTPS、Host、受信代理与管理员 CIDR 检查；它使用明确提交的 Bearer 凭据，不要求浏览器 Origin 或 CSRF。Cookie 不作为失败后的回退凭据，Bearer 也不能访问 /accounts、/api-keys 等管理员接口。不支持集成路由以外的任意路径转发。开发模式没有 Bearer 消费入口。

迁移 020 保存任务所属 Key ID，并将事件键分为面板与每个 Key 的独立命名空间。旧面板事件键原样保留并标记 panel，升级后重试仍返回原任务。不同调用方不合并活动任务，即使动作相同也返回 instance_power_busy，避免撤销一个 Key 时取消另一方授权的动作。队列事务再次检查 Key 未撤销及 scope，避免 HTTP 鉴权后、入队前撤销仍能创建任务。trigger 由服务端设为 api，客户端不能指定。

用户已确认撤销策略：取消尚未发送的任务，保留期望状态。实现上，撤销 Key 与取消未写发送标记的 queued/running 任务在同一 SQLite 事务完成，错误码为 api_key_revoked，清空任务租约并发布任务变化。发送标记与撤销事务互斥：撤销先提交则 worker 无法写标记；标记先提交则请求可能已经发出，服务端不再承诺能撤回，保留响应和后续只读状态确认。不会把已开始发送的工作标成“保证未发送”，也不会因撤销而重试。已完成历史不删除，期望状态不回滚。

未在真实服务中创建或使用 API Key，也没有启用真实电源操作。测试使用隔离数据库、测试随机 Key 和模拟/不调用云接口的路径。

API Key 控制阶段实际验证（均退出 0）：

```text
go test ./internal/store -run 'APIKey|Power' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 0.885s

go test ./internal/store -run APIKeyPowerMigration -count=1
ok github.com/JasperXzy/harbormaster/internal/store 0.424s

go test ./internal/api -run Integration -count=1
ok github.com/JasperXzy/harbormaster/internal/api 0.604s

go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 47.717s
ok github.com/JasperXzy/harbormaster/internal/backup 38.030s
ok github.com/JasperXzy/harbormaster/internal/cli 9.366s
ok github.com/JasperXzy/harbormaster/internal/collect 4.985s
ok github.com/JasperXzy/harbormaster/internal/devmode 6.764s
ok github.com/JasperXzy/harbormaster/internal/model 1.639s
ok github.com/JasperXzy/harbormaster/internal/notify 2.041s
ok github.com/JasperXzy/harbormaster/internal/power 9.200s
ok github.com/JasperXzy/harbormaster/internal/store 29.820s
其余有测试的包均 ok (cached)，命令包等为 [no test files]。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 41ms
Woohoo! Your API description is valid.

git diff --check
（无输出）
```

新增覆盖：无 Origin 的合法 Bearer 调用、Cookie 不回退、Bearer 不授予管理员会话、HTTPS/Host/伪造转发头限制、重复 Authorization 拒绝、scope 不足、来源 api、私有任务查询、默认关闭、无同步云调用、不同调用方事件键、撤销前后发送标记边界、撤销事务强制失败回滚、保留意图、SSE 任务变更以及升级保留旧面板重试键。没有真实 API Key 使用或真实云写操作。

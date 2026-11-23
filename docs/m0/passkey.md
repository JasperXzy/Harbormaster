# Passkey 服务端

2026-11-12；对应 PRD F15.3。已交付注册、登录、列表/删除、密码登录开关，以及用户确认的 CLI 恢复策略。前端由 Claude 实现，不修改 web/ 或 docs/design/。

## 接口与浏览器流程

面板契约见 `docs/api/openapi.yaml`。新增接口：

- `GET /session/methods`：公开查询可用方式；未初始化时都为 false。密码开关从持久策略读取；Passkey 可用状态只计当前 RP 的注册凭据。
- `POST /passkeys/registration/options`：已登录、CSRF，提交 name；返回 ceremony_id、expires_at 和 options.publicKey。
- `POST /passkeys/registration/finish`：同一会话、CSRF，提交 ceremony_id 和浏览器 credential JSON；201 返回公开元数据。
- `GET /passkeys`、`DELETE /passkeys/{id}`：仅管理员；删除需要 CSRF。
- `POST /session/passkey/options`：同源 HTTPS，请求 `{}`；返回 discoverable 登录选项，并设置浏览器绑定 cookie。
- `POST /session/passkey/finish`：同源 HTTPS、同一绑定 cookie，提交 ceremony_id 和 credential；成功返回既有 Session 结构和 hm_session cookie。

浏览器将 options.publicKey 经 `PublicKeyCredential.parseCreationOptionsFromJSON` / `parseRequestOptionsFromJSON` 转换后传给 `navigator.credentials.create/get`。浏览器不支持这些转换函数时，前端显式把 base64url 字段转换成 ArrayBuffer。结果提交 `PublicKeyCredential.toJSON()` 或等价结构。请求整体限 16 KiB，不把服务端 session_data 发给客户端。

注册要求 residentKey=required、userVerification=required，登录要求 userVerification=required。单管理员显示名 Harbormaster、内部名称 administrator，无用户名输入；授权只看随机用户句柄和凭据。注册名称为 1–80 UTF-8 字节，最多 20 个 Passkey。列表不暴露 credential ID、公钥、用户句柄或 attestation 原文。

`hm_passkey` 为 Secure、HttpOnly、SameSite=Strict，路径 `/api/v1/session/passkey`；登录成功清除。再次开始登录会替换该浏览器的绑定 cookie，先前流程因此不能继续。注册绑定当前 hm_session 的哈希。登录 options 与 finish 各计一次既有持久限速（每来源五分钟 5 次、全局 30 次），不是每一步单独获得新额度。开发模式只提供空列表和 passkey_enabled=false，不伪造生物验证成功。

## 校验与事务

依赖 [go-webauthn v0.18.2](https://github.com/go-webauthn/webauthn/releases/tag/v0.18.2)；采用库的 [WebAuthn 验证接口](https://pkg.go.dev/github.com/go-webauthn/webauthn/webauthn)。2026-11-12 查询并下载固定版本、校验 Go module sum；不自行实现生产密码学。RP ID 为配置 public_url 的 hostname，允许的 Origin 为该 HTTPS origin；不从请求 Host 或 Forwarded 动态扩展信任。跨源 iframe 禁用，attestation 偏好 none，不启用远程元数据服务。每个挑战持久绑定开始时 RP ID 和 Origin，修改 public_url 后旧挑战不能在新配置下完成。

挑战五分钟有效，数据库最多 100 个未过期挑战；创建时清理过期记录。`DELETE ... RETURNING` 在验证前消费匹配 kind/binding 的挑战，失败也不可重放；错误浏览器绑定不会消费他人的挑战。已有 HTTPS、受信代理、来源 CIDR、Origin 和 CSRF 检查保持有效；API Key 和 agent token 不能使用这些接口。

登录完成将凭据更新（含签名计数器/标志/最后使用时间）与 session 创建放在同一事务，并比较读取到的 credential_json 和认证 epoch。删除凭据、并发更新、CLI 重置或恢复发生在提交前，旧验证结果不能重新建立会话。库报告 CloneWarning 时拒绝登录；支持计数器为零的合法同步 Passkey，不要求同步凭据必定递增。删除操作不清除已经建立的会话，和注销会话分开。

注册提交再次校验当前会话、有效期和 epoch。用户于 2026-11-12 明确确认：CLI reset-password 重新启用密码登录，清除所有会话与未完成挑战、递增 epoch，并保留全部已注册 Passkey；现已实现。

## 数据与备份

迁移 022：

- `passkey_state`：随机 32 字节用户句柄、认证 epoch。
- `passkey`：内部 ID、唯一 credential ID、名称、创建/最后使用时间、库的公开凭据 JSON。
- `passkey_ceremony`：随机 ceremony ID、类型、绑定哈希、epoch、注册名称、库 SessionData、过期时间。

Passkey 私钥只在认证器上，服务端不接收、不保存私钥。数据库里的验证公钥、凭据 ID、attestation 和随机挑战是公开材料，不属于可逆密钥，不新增 AES 凭据 owner；其他 AccessKey/Token 加密要求保持不变。浏览器绑定 token 仅存 SHA-256，和既有 session token 一样。备份自身仍为密码加密，恢复保留公钥和用户句柄、清除所有挑战、增加 epoch 并清除会话。新域名需要重新注册 Passkey；仅换机器且保留同一 HTTPS RP 域名可以继续使用。

## 验证与限制

使用本地生成的 P-256 认证器、公钥 COSE/CBOR、none attestation 和真实 ECDSA assertion，经过生产使用的 WebAuthn parser/verifier：

- 注册、列表、登录并取得可用管理员会话。
- 错误 Origin、RP ID、签名、缺失 UV/UP、重复 credential ID、挑战过期与重放拒绝。
- 计数器回退拒绝，错误浏览器绑定不消费正确浏览器挑战。
- 8 个并发消费只有 1 个成功；删除/计数器更新/reset/restore/过期与登录提交竞争时拒绝旧结果。
- 注册在 session 撤销、重置、过期后不能提交；恢复保留公开凭据并清除挑战。
- CSRF、公开登录 Origin、持久限速及无 HTTPS public_url 的不可用状态。

这些是服务端本地验证，不代表 Safari/Chrome、iCloud/Google 同步 Passkey、安全密钥及前端页面已完成真实端到端验收。没有操作任何真实认证器、云资源或远程机器。密码开关与恢复策略的新增验证见下节；尚未加入 Passkey 重命名、审计界面或多管理员模型。

实际运行结果（2026-11-12）：

```text
go test ./internal/api -run TestPasskey -count=1
ok github.com/JasperXzy/harbormaster/internal/api 1.067s

go build ./... && go vet ./...
exit 0，无输出

go test -race ./...
ok internal/api 59.200s
ok internal/backup 39.418s
ok internal/cli 9.615s
ok internal/collect 5.778s
ok internal/devmode 6.768s
ok internal/notify 1.990s
ok internal/power 9.635s
ok internal/store 35.567s
其余包 cached 或 [no test files]，exit 0

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 141ms
Your API description is valid.

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/hm-server ./cmd/hm-agent
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./cmd/hm-server
均 exit 0，无输出；临时 Windows 产物已清理。

git diff --check
exit 0，无输出
```

## 密码登录策略与已确认的恢复行为

迁移 024 为 passkey_state 增加 password_enabled，默认 true，升级保留既有密码可用性。`PATCH /session/password` 接受 `{enabled: true|false}`，仅管理员会话与 CSRF；开关只影响后续密码认证，不更换密码，也不清除当前会话。重新开启时仍使用已有密码；忘记密码使用 CLI 重置。

关闭前必须存在当前 HTTPS public_url 的 RP ID 对应的已注册 Passkey。由已验证注册记录中的 attestation 原文读取 RP 哈希，不从浏览器传入的候选 ID 或 Host 头推断，不仅凭“数据库有一个 key”就允许关闭。旧域名的 key 不计入当前 RP；公开登录方式查询也使用这个判断。这证明凭据属于当前站点，不证明用户此刻仍持有认证器。

候选凭据的 ID 和完整公开记录在事务内重查，已删除或并发更新的旧快照不能通过关闭/最后一把 key 的检查。密码关闭时禁止删除最后一个当前 RP 的 key（409 last_passkey）；缺少可用候选时关闭返回 409 passkey_required。并发删除两把 key、并发关闭密码与删除最后一把 key 都不能让密码关闭且没有 key。候选刚发生并发更新时可能保守拒绝，客户端刷新后可重试。

密码登录既在读取 hash 前检查开关，也在创建 session 的事务内重查，避免已验证的密码请求越过关闭操作。关闭时仍用统一 invalid_credentials，既有登录限速不变。设置操作再次校验会话，注销/重置与设置竞争不能由旧会话恢复策略。

CLI reset-password 按用户确认，原子更新 Argon2id 密码、设 password_enabled=true、清除 session 与 passkey_ceremony、递增 epoch，保留 Passkey 公钥和计数状态。备份恢复继续保留备份中的密码策略，不擅自重新启用密码；换域名无法使用旧 Passkey 时可用 CLI reset-password 恢复。

新增验证使用本地真实 P-256 注册/登录和 CLI 框架：关闭密码后密码认证失败、Passkey 认证成功、最后 key 删除被拒；CLI 重置后新密码成功、所有旧会话和挑战失效、原 Passkey 仍可签名登录。覆盖缺少 key、不同 RP、CSRF、空/错误请求体、重新开启原密码、候选/会话重查及并发最后 key 保护。没有使用真实认证器或修改正式部署。

密码策略阶段实际检查（均退出 0）：

```text
go test ./internal/api -run 'TestPassword' -count=1
ok github.com/JasperXzy/harbormaster/internal/api 1.225s

go test -race ./internal/store -run 'TestPassword' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 2.243s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok internal/api 73.742s
ok internal/backup 43.340s
ok internal/cli 10.260s
ok internal/collect 6.251s
ok internal/devmode 7.007s
ok internal/notify 1.822s
ok internal/power 10.739s
ok internal/store 44.450s
其他有测试包 cached，命令包等为 [no test files]；exit 0。

# 扩充 CLI 入口恢复断言后单独复跑
go test -race ./internal/cli -run TestProductionBootstrapAndResetDoNotLogCredentials -count=1
ok github.com/JasperXzy/harbormaster/internal/cli 3.976s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 56ms
Your API description is valid.
```

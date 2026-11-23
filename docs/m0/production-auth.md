# 生产认证与首次设置

日期：2026-11-12。属于 M1 的第一个实现阶段；不是 M1 整体完成。

## 用户确认的规则

- 单管理员，无用户名。
- 账号删除采用停用并保留历史的语义，停止采集，不删除云资源。本阶段已更新契约和前端模拟流程，生产账号管理随后实现。
- CDT 无可靠统计截止时间时，仅展示官方累计与独立 agent 数据；实时估算保持“待确认”，暂不启用依赖精确对齐的保护。此决定不表示 24 小时计量验证已通过。

这些是用户在本阶段的明确答复。PRD 原文未修改，建议下一版将以上规则写入 F1、F3、F15 及 M1 验收条件。

## 已实现

- `/setup/status`、`POST /setup`、登录、当前会话、退出登录；单管理员使用 Argon2id。
- 一次性初始化凭证仅存 SHA-256 哈希，24 小时有效。初始化事务同时创建管理员、保存业务时区、创建会话、消耗凭证，并发只允许一次成功。
- Session 使用 256-bit 随机令牌，数据库只存哈希。Cookie 为 HttpOnly、Secure、SameSite=Lax、Path=/，无 Domain。默认绝对有效期 12 小时，可配置 5 分钟至 7 天，不滑动续期。
- 登录/初始化共用持久化限速：每个来源 IP 每 5 分钟最多 5 次，全局同窗口最多 30 次；所有尝试（含成功或格式错误）计数，不因重启清空。返回 429、Retry-After 和 JSON 等待秒数。单进程最多同时进行一次 Argon2 计算。
- 所有生产面板写请求必须同源，包括首次设置与登录；已认证写请求还要求会话绑定的 CSRF。读取不要求 CSRF。
- 生产 API 要求 HTTPS 与配置的公网 Host。只信任明确配置的代理，X-Forwarded-For 从右向左查到第一个不可信地址，避免信任客户端自填的最左地址。可对管理员 API 设 CIDR 白名单，不影响健康检查和独立 agent token 接口。
- 退出立即撤销对应会话；CLI 重置密码撤销全部会话。登录写入前重新核对已验证的密码哈希，避免旧密码验证与重置竞争后重新创建会话。
- SSE 15 秒心跳重新核对会话，过期或撤销会在下一次心跳时断开；服务关闭会取消连接上下文。
- 已认证版本和通用设置读取。尚未交付的业务接口明确返回 501，不使用演示数据替代真实采集结果。

## 配置与启动

使用 `deploy/config.example.json`；同源静态前端部署仍参考 `deploy/Caddyfile.separate`。以下值必须与实际部署一致：

| 配置 / 环境变量 | 作用 |
|---|---|
| `public_url` / `HM_PUBLIC_URL` | 面板 HTTPS 根地址，不含路径、认证信息或查询参数 |
| `trusted_proxies` / `HM_TRUSTED_PROXIES` | 受信任代理 CIDR；环境变量以逗号分隔。默认不信任任何代理 |
| `admin_allow_cidrs` / `HM_ADMIN_ALLOW_CIDRS` | 可选管理员来源白名单；空列表不限来源 IP |
| `session_ttl_seconds` / `HM_SESSION_TTL_SECONDS` | Session 绝对有效期，默认 43200 秒 |
| `HM_MASTER_KEY` 或 `HM_MASTER_KEY_FILE` | 既有 AES-256-GCM 主密钥来源，二选一；文件必须在数据目录外 |
| `HM_SETUP_TOKEN` 或 `HM_SETUP_TOKEN_FILE` | 首次设置凭证来源，二选一；完成初始化后不再需要 |

初始化 token 必须使用安全随机生成器产生至少 32 字节随机数据，例如由运维在受限目录运行 `openssl rand -hex 32` 并将输出重定向到权限为 0600 的文件。不要将值放进命令参数、仓库、配置 JSON 或日志。文件位于数据目录外；Unix 系统拒绝组/其他用户可读的 token 文件。服务不会在日志打印凭证，向导也不再引导用户查日志。

```sh
sh deploy/build-backend.sh
# 先在环境中指定外部密钥文件和初始化 token 文件的路径；这里只展示路径。
HM_MASTER_KEY_FILE=/etc/harbormaster/master.key \
HM_SETUP_TOKEN_FILE=/etc/harbormaster/setup.token \
./deploy/bin/hm-server serve --config deploy/config.example.json
```

首次设置成功后可由运维删除外部 setup.token 并移除对应环境变量；数据库已经消耗其哈希。未使用的 token 过期后，写入新随机值并重启服务即可轮换；以同一个值重启不会延长有效期。已有管理员时，任何初始化 token 都不能重新设置密码。

密码重置继续使用 `hm-server reset-password` 从 stdin 输入新密码；请使用不回显密码的终端输入或秘密管理工具，避免把密码写入 shell 历史。开发模式独立使用本机 HTTP 与示例密码，不放宽生产规则。

## 验证范围与后续

自动测试覆盖首次设置重放/过期/轮换、两个 SQLite 连接并发初始化、限速跨数据库重开、全局限速、Cookie、CSRF、HTTPS/Host/代理头/管理员 CIDR、实际 TLS 客户端 Cookie jar、SSE 撤销、密码重置竞争、输入大小与格式，以及启动/重置日志无凭证。实际输出见 `production-auth-validation.txt`。

本阶段没有公网部署，没有安装真实 agent，没有读写真实云资源或发送 Telegram。下一实现阶段是生产账号管理和持久化任务/只读采集，随后接入流量与告警。当前页面在生产模式读取未实现的业务功能时会明确报未开放；开发预览继续可用。

12 小时会话、24 小时初始化有效期、上述限速阈值是本轮工程默认值；单管理员和账号/流量处理规则来自用户确认。

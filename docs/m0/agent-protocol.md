# hm-agent 上报协议 v1（后端内部）

本协议不属于面板 OpenAPI。已实现服务端生产注册、单条/批量鉴权落库、ACK、撤销、实例状态和速率。Linux nftables 采集、客户端加密状态/离线缓冲已编码并通过本地测试与交叉编译；安装产物及 API 也已准备，尚未安装真实 agent，原生 Linux 计数尚未实测。详见 production-agent-client.md 和 agent-installation.md。

2026-11-11 实测与用户决定：sha-pro 的元数据 instance-id 是底层 ECS ID，和 SWAS API ID 不同。用户同意轻量使用预绑定已纳管 SWAS ID 的一次性注册码，元数据 ID 仅记录为辅助信息；服务端不可把两个 ID 当同一标识比较。ECS 的 sin-pro 已验证元数据 ID 与 ECS API 一致。此处是设计修订，本轮未安装真实 agent。

## 身份与传输

生产上报只通过 HTTPS，实例不开放入站端口。注册入口 `POST /agent/v1/register`：一次性随机 256-bit 注册码在服务端预绑定 account/kind/region/instance，10 分钟过期，只存 SHA-256；事务内消费一次。客户端先持久化加密的注册码和随机 256-bit nonce（64 位十六进制），请求包含 version=1、registration_code、nonce、metadata(instance_id/region_id)、agent_version。双方以注册码为 HMAC-SHA256 密钥，对 `harbormaster-agent-token-v1\0` + nonce 派生长期 token（前缀 hm_agent_ + 十六进制摘要）。响应只含 agent_id、token_scheme=hmac-sha256-v1、report_interval_seconds=10、server_time，不回传秘密。相同 code+nonce 在原 10 分钟期限内可重试，以恢复丢失的注册响应；不同 nonce 不可重用已消费注册码。签发新注册码使旧注册码失效，但直到新注册成功才替换旧 agent token，避免用户只是复制安装命令就导致正在工作的 agent 掉线。请求必须匹配已纳管记录。元数据声明本身不是远程可信证明，安装者持有预绑定注册码是本设计的信任边界；如果要求证明请求确实来自这台云实例，须另确定可用的云侧身份机制，不能声称元数据文本可以防伪。

服务端只存长期 token 的 SHA-256，不存明文；token 泄露仅授予对应 agent 的上报/未来固定测速权限，不接受管理 Cookie/API Key。撤销后立即 401。agent 本地身份与离线队列使用 AES-256-GCM 日志，主密钥在状态目录之外；安装阶段将接入 systemd credential。

`--dev` 仅接受显式回环 HTTP 服务器，使用公开假 token，不访问元数据或防火墙。生产 Linux 客户端使用单独的 register/serve/run-once 流程，并要求外部主密钥及显式公网接口。

## 上报与 ACK

`POST /agent/v1/reports`，`Authorization: Bearer <instance-token>`，JSON UTF-8，最大 16KiB；固定字段，未知字段拒绝。默认 10 秒一次。

```json
{
  "version": 1,
  "agent_id": "demo-agent-sin",
  "counter_epoch": "counter-generation-id",
  "sequence": 1,
  "sampled_at": "2026-11-11T10:00:00Z",
  "uptime_ms": 10000,
  "rx_bytes": 123456,
  "tx_bytes": 234567,
  "agent_version": "dev",
  "rules_version": "nft-public-v1",
  "dropped_samples": 0,
  "family_counters": {"ipv4_rx_bytes": 123456, "ipv4_tx_bytes": 234567, "ipv6_rx_bytes": 0, "ipv6_tx_bytes": 0}
}
```

`rx_bytes`/`tx_bytes` 是同一计数器代际的累计公网字节，非本次增量；均为非负 int64。sequence 在同一 epoch 递增，uptime_ms 使用单调时间。sampled_at 用 UTC，服务器另记 received_at，超过服务器未来 2 分钟拒绝。早于接收时间 7 天的样本拒绝。记录 baseline/ok/clock_skew/counter_reset/stale/gap 数据质量；只有采样时间在接收时间前后 30 秒内的新记录才刷新心跳，时间倒退不刷新，重复样本不刷新。同一 epoch 规则版本不变。可选 family_counters 的两种地址族之和必须等于 rx/tx 总量。

```json
{"accepted_sequence":1,"counter_epoch":"counter-generation-id","server_time":"2026-11-11T10:00:01Z","duplicate":false}
```

`POST /agent/v1/reports/batch` 接收 `{ "reports": [...] }`，1–100 条、最大 128KiB，同一 agent 按序提交，全批事务提交或回滚。ACK 指向批内最后一个 epoch+sequence；客户端必须同时核对两者才能删缓存。

- 200 只在数据库事务提交后返回。完全相同的重试返回 duplicate=true，且不推进在线时间。
- `(agent_id,counter_epoch,sequence)` 唯一。相同键不同计数/时间 409；同 epoch 的倒退序列、单调时间或累计计数 409。
- 新 epoch 表示计数器实际重置。仅 agent 进程重启但内核计数器仍在时应复用持久化 epoch，不能随意归零；M0 合成进程重启会重置计数，因此新 epoch 合理。
- 独立 agent_counter_state 与 agent_retired_epoch 账本在清理原始样本后继续阻止重放。旧 epoch 不能在切换后再次插入；已落库完全相同的重试仍可 ACK。数据库原始记录不跨 epoch 做差。
- 没有上一个有效样本的新 epoch 是基线，不自称恢复了上次重启前未上报的字节；该缺口要记录质量状态，由保护估算处理。
- 401 失效 token，400 格式错误，409 冲突，503 暂时不可写。错误体采用 `{error:{code,message}}`，不返回内部 SQL、token 或原始请求。

## M1 离线与计费边界设计

agent 保留未 ACK 的有界本地时间序列，按序补传，相同 payload 重试；指数退避与抖动，不能无限缓存。最新累计值避免丢总字节，但仅凭断网前后两点**无法精确分摊跨小时主流量**，更不能推定跨月用量；所以保留采样时刻和计费边界样本，缓存溢出要显式报告缺口。M0 demo 只保持进程内计数，不实现持久补传。

计数范围由经过 V7 验证的公网边界网卡/规则定义，至少区分 IPv4/IPv6 的排除网段、网络重配与 NAT 口径。后续引入规则版本时升协议或新增明确可选字段，不能让既有 epoch 混用两种口径。

## 固定动作

M3 的任务只能是有类型的 `icmp_ping`、`tcp_connect`、`trace_route`，参数仅目标、端口、包数、超时和 hop 上限；实现直接调用网络 API，不接受脚本、命令行、可执行文件路径、环境变量或 shell。任务需要 ID、到期时间、领取幂等和结果上限。M0 没有任务执行入口，提交任意 `command` 字段会被拒绝。

## 面板对接与存储

迁移 005 新增预绑定注册码、当前计数账本、已退役 epoch 账本和样本扩展信息。服务端只存注册码/token/nonce 的哈希；原始计数与质量记录不含秘密。已停用账号及已确认消失的实例不接收新上报。

`DELETE /api/v1/instances/{id}/agent/token` 要求管理员会话、Origin 和 CSRF，撤销后状态为 not_installed，保留历史并清除待用注册码。Agent 入口使用独立 HTTPS+token/code 鉴权，不套用管理员 IP 白名单或 Cookie。

实例列表实时覆盖保存的云快照：registered_at 表示注册时间，last_report_at 为最新有效心跳的**服务端接收时间**；pending_first_report=true 时尚未收到新鲜数据，status=offline 但 offline_since=null，不能据此发掉线通知。30 秒无新样本为 offline，速率清空。速率用同 epoch 相邻样本的累计差除以 uptime_ms 差，要求间隔不超过 30 秒、时钟递增且数据质量 ok；不跨 epoch 做差，不把长时间补传的平均值称为当前速率。

SSE 每 5 秒合并变化，发送 agent.status 和 instance.realtime；后者允许 realtime=null 主动清除旧速率。前端需重新生成契约类型并处理 null。新增 data_quality 与 pending_first_report 为可选字段。CDT 对齐未确认期间 realtime_protection 始终为 unprotected 或 not_applicable，不产生 worst_case/protected。

安装命令端点只在配置的发布目录完整且校验通过时签发注册码，否则返回 503，不返回假安装命令。真实机器计数、CPU、吞吐、容器/NAT 重复计数、进程重启与系统重启测试仍需真实主机操作授权。

# hm-agent 上报协议 v1（后端内部）

本协议不属于面板 OpenAPI。M0 已实现合成 agent → 服务端鉴权/落库/ACK；真实注册、nftables、离线缓冲、任务领取尚属 M1/M3。

2026-11-11 实测与用户决定：sha-pro 的元数据 instance-id 是底层 ECS ID，和 SWAS API ID 不同。用户同意轻量使用预绑定已纳管 SWAS ID 的一次性注册码，元数据 ID 仅记录为辅助信息；服务端不可把两个 ID 当同一标识比较。ECS 的 sin-pro 已验证元数据 ID 与 ECS API 一致。此处是设计修订，本轮未安装真实 agent。

## 身份与传输

生产上报只通过 HTTPS，实例不开放入站端口。建议注册入口 `/agent/v1/register`：一次性随机 256-bit 注册码在服务端预绑定 account/kind/region/instance，10 分钟过期，只存 SHA-256；事务内消费一次，返回长期随机 token。请求必须匹配已纳管记录。元数据声明本身不是远程可信证明，安装者持有预绑定注册码是本设计的信任边界；如果要求证明请求确实来自这台云实例，须另确定可用的云侧身份机制，不能声称元数据文本可以防伪。

服务端只存长期 token 的 SHA-256，不存明文；token 泄露仅授予对应 agent 的上报/未来固定测速权限，不接受管理 Cookie/API Key。撤销后立即 401。agent 本地凭据未来通过 systemd credential/仓库外受限来源注入，若需长期保存采用 AES-256-GCM 且密钥在状态目录之外；M0 没有真实 token 的保存实现。

M0 唯一可运行的 agent 模式为 `--dev`，只接受显式回环 HTTP 服务器，使用公开的假 token；生产模式直接报 `real_collection_not_available_in_m0`。不访问元数据、不申请能力、不修改防火墙。

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
  "agent_version": "dev"
}
```

`rx_bytes`/`tx_bytes` 是同一计数器代际的累计公网字节，非本次增量；均为非负 int64。sequence 在同一 epoch 递增，uptime_ms 使用单调时间。sampled_at 用 UTC，服务器另记 received_at，超过服务器未来 2 分钟拒绝。生产的时钟质量与旧样本新鲜度需单独记录，不能用补传样本表示当前在线。

```json
{"accepted_sequence":1,"server_time":"2026-11-11T10:00:01Z","duplicate":false}
```

- 200 只在数据库事务提交后返回。完全相同的重试返回 duplicate=true，且不推进在线时间。
- `(agent_id,counter_epoch,sequence)` 唯一。相同键不同计数/时间 409；同 epoch 的倒退序列、单调时间或累计计数 409。
- 新 epoch 表示计数器实际重置。仅 agent 进程重启但内核计数器仍在时应复用持久化 epoch，不能随意归零；M0 合成进程重启会重置计数，因此新 epoch 合理。
- 旧 epoch 不能在切换后再次插入；已落库完全相同的重试仍可 ACK。数据库原始记录不跨 epoch 做差。
- 没有上一个有效样本的新 epoch 是基线，不自称恢复了上次重启前未上报的字节；该缺口要记录质量状态，由保护估算处理。
- 401 失效 token，400 格式错误，409 冲突，503 暂时不可写。错误体采用 `{error:{code,message}}`，不返回内部 SQL、token 或原始请求。

## M1 离线与计费边界设计

agent 保留未 ACK 的有界本地时间序列，按序补传，相同 payload 重试；指数退避与抖动，不能无限缓存。最新累计值避免丢总字节，但仅凭断网前后两点**无法精确分摊跨小时主流量**，更不能推定跨月用量；所以保留采样时刻和计费边界样本，缓存溢出要显式报告缺口。M0 demo 只保持进程内计数，不实现持久补传。

计数范围由经过 V7 验证的公网边界网卡/规则定义，至少区分 IPv4/IPv6 的排除网段、网络重配与 NAT 口径。后续引入规则版本时升协议或新增明确可选字段，不能让既有 epoch 混用两种口径。

## 固定动作

M3 的任务只能是有类型的 `icmp_ping`、`tcp_connect`、`trace_route`，参数仅目标、端口、包数、超时和 hop 上限；实现直接调用网络 API，不接受脚本、命令行、可执行文件路径、环境变量或 shell。任务需要 ID、到期时间、领取幂等和结果上限。M0 没有任务执行入口，提交任意 `command` 字段会被拒绝。

# SQLite 结构与迁移

M0 使用 modernc.org/sqlite（纯 Go），无需 CGO，文件 `harbormaster.db`；WAL、foreign_keys=ON、busy_timeout=5000ms。当前单连接串行化写入，适用于本阶段少量实例，避免 SQLite 多写者争用；M1 查询并发需要实际负载后调整。

## 已落地的表

| 表 | 用途与关键约束 |
|---|---|
| schema_migration | migration 文件名唯一；事务成功才记完成 |
| app_meta | production/development 标记；禁止同一库切换模式 |
| account | 内部 ID、显示名、站点、更新时间；不保存明文凭据 |
| credential | owner_field 主键、AES-256-GCM 密文 blob、更新时间；不存主密钥 |
| instance | 账号外键；account/kind/region/cloud_id 联合唯一；实际与期望状态分开 |
| billing_subject | 计费对象、规则、地域、CDT eligibility=unknown/eligible/ineligible |
| subject_member | 对象/实例/valid_from 联合主键，valid_to 支持拓扑历史 |
| agent | 实例唯一绑定、32-byte token_hash、撤销时间、版本、最后接收时间 |
| traffic_sample | agent/epoch/sequence 联合主键，累计双向字节非负；按 agent+sampled_at 查询、received_at 清理索引 |
| cdt_snapshot | 官方用量、period_start、可空 covered_until、fetched_at、来源与质量；未知水位不能填抓取时间 |
| administrator | 单管理员，Argon2id PHC 字符串；生产首次设置与 demo 预置使用隔离数据库 |
| session | 随机会话 token 的哈希和有效期；reset-password 同事务清空会话 |
| setup_credential | 一次性初始化凭证哈希与 24 小时有效期，创建管理员的事务中消耗 |
| auth_attempt | 来源 IP 与全局登录/初始化尝试窗口，跨重启保留限速 |

迁移文件位于 `internal/store/migrations/`，通过 go:embed 编入二进制。启动或 migrate 在立即写事务中创建迁移记录、顺序执行未执行文件；失败回滚，不半建库。后续只追加迁移，不修改已应用的 001。不提供破坏性自动 down migration。

时间在数据库存固定精度 UTC RFC 3339（含 9 位小数），确保文本排序一致；对外时间仍为 RFC 3339。字节用 SQLite 有符号 64 位整数，拒绝负数。真实金额后续存精确十进制字符串 + currency，不用浮点计算。

## 密钥与凭据

AES-256-GCM 密文格式：`version(1 byte) || random nonce(12 bytes) || ciphertext+tag`；AAD 使用稳定 `owner/id/field`，跨行复制密文不能解密。HM_MASTER_KEY 为标准 base64 编码的 32 字节；或 HM_MASTER_KEY_FILE 指向数据目录之外的文件，解析符号链接后检查边界。两种来源不能同时设置。不生成或保存数据目录内主密钥；无密钥时生产启动失败。M0 无账号/通知凭据写 API，credential 表尚不接收真实数据。

AccessKey Secret、Telegram Token 等需要可逆使用的值放 credential；agent/API/session token 用高熵随机值只存哈希；管理员密码用 Argon2id。日志不输出配置对象、HTTP 请求体、原始 SDK/网络异常；敏感键值另做日志替换，AccessKey ID 有打码辅助函数。

## 后续表与保留策略（设计，未实现）

- M1：traffic_hourly、traffic_estimate、swas_package_snapshot、job、alert_rule/state、notification outbox、setting；job 的事件键唯一、租约到期可抢占，云查询只在 worker，GET 读快照。
- 原始样本默认 7 天、小时聚合 13 个月；按小时合并同一共享带宽全部成员后再 max，不能汇总成员 max。聚合与标记进度事务提交后才能清理原始数据；跨月、缺口、成员迁移单独处理。
- M2：规则、期望状态变更原因/确认状态、豁免期限、审计和动作记录；outbox 与业务事件同事务，外部 Telegram 发送仍可能成功但 ACK 丢失，不能承诺网络意义上的 exactly-once。
- M3：probe/route 相关表，测速样本默认 30 天。

不在 M0 预建全部后续表或宣称已实现持久任务/自动停机。2026-11-12 用户确认账号停用并保留历史；外键不自动级联删除历史，生产账号写 API 尚未开放。SQLite ≤1GB 和常驻内存目标尚未做长期负载验收。

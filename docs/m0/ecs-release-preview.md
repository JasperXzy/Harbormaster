# ECS 释放影响预览

2026-11-13：本文记录后台只读采集、凭据版本隔离缓存和管理员预览阶段。后续[持久任务协议](ecs-release-queue.md)已实现，仍没有释放提交或执行 worker，execution_enabled 固定 false。没有使用真实凭据调用释放或 DryRun，也没有新增本批真实云查询。

## 已确认规则

用户确认首期仅支持已停止的按量/抢占式 ECS；不支持运行中、包年包月或轻量实例。云盘、自动快照沿用已有删除属性，确认时展示影响；不自动修改属性，也不另外释放 EIP 或手动快照。该确认是产品范围，不能作为真实操作授权。

本批在面板契约顶部先记录变更，再实现 `GET /api/v1/instances/{instance_id}/release-preview`。接口保持管理员会话、HTTPS/主机/CIDR 边界，只读 SQLite；既有异步刷新可更新后台数据。不会因为打开确认页面而查询云。

## 来源与缓存

区域发现已有 DescribeInstances 查询。本批新增固定 DescribeDisks，只允许 RegionId、MaxResults=100、NextToken；每页先检查账号租约与取消，最多 50 页/5000 行。不使用 TotalCount 判断完成，因为该接口明确规定游标分页时它无效。非法/重复游标、空页仍有游标、重复 ID、超页数、畸形列表或错误地域均拒绝发布完整数据。

保留实例真实名称、云状态、InstanceChargeType、SpotStrategy、DeletionProtection 和操作锁是否存在，不复制锁消息。未提供的布尔字段为 null，不把缺失值当 false。磁盘缓存只包含身份、类别/类型、已观测挂载对象与两项删除属性，不保留原始云响应或凭据。

多重挂载使用 Attachments.Attachment，旧共享盘使用 MountInstances.MountInstance；普通盘还核对 InstanceId。In_use 时空 ID 不能证明未挂载。挂载/卸载过渡态、矛盾或缺失归属、未知状态均标为归属不完整。由于无法排除归属不明的盘属于目标，它会阻止该地域的预览被判为可提交。

迁移 042 的 release_inventory 以账号、凭据版本、地域为主键。与实例快照同处 SaveScan 的任务/账号租约和修订检查事务中。失败保留原成功内容与抓取时间，仅更新尝试时间和稳定错误码；更旧的尝试不能覆盖较新结果。成功的空列表替换旧列表，不把历史磁盘带入新预览。账号换密钥后不读取旧身份缓存；账号停用或移除地域保留历史但阻止提交。

## 影响和可用性

只有已发现且仍存在的 ECS、账号/地域启用、完整数据在五分钟有效期内，且以下事实明确时，状态才为 eligible：

- 真实实例名非空，云状态 Stopped，计费类型 PostPaid。
- 明确未开启删除保护，操作锁列表明确为空。任何锁都会阻止，避免安全锁忽略云盘保留属性的官方例外。
- 至少识别到目标系统盘；目标磁盘类型和存储类别已知，整个地域没有归属不明的磁盘。
- 云盘的 DeleteWithInstance / DeleteAutoSnapshot 均有明确值。未知类别或属性缺失不能推断影响。

预览显示现有属性，而不是替用户更改：

| 资源 | 影响 |
|---|---|
| 实例内存、实例本地/临时存储数据 | 删除/不可保留 |
| 固定公网 IP | 如存在则回收 |
| 云盘 DeleteWithInstance=false | 保留；不由本工具修改计费或删除属性 |
| 云盘 DeleteWithInstance=true | 随实例删除；按 DeleteAutoSnapshot 显示自动快照去留 |
| 保留的云盘 | 保留其自动快照，即便该盘的 DeleteAutoSnapshot=true；该属性只在删盘时生效 |
| 手动快照、EIP | 保留；本批不枚举快照 ID，也不发起独立删除 |

保留的资源可能继续计费，不推算节省金额。预览没有保护、停机、解绑、属性修改或创建能力。

响应区分 never_fetched、unavailable、stale、blocked、eligible，并带时间和阻止原因。错误/过期时可展示旧影响，但 preview_token 为 null。抓取时间在未来也判过期。有效期边界为 fetched_at + 5 分钟，达到该时刻即失效。

preview_token 是排序后的影响、实例本地 ID、抓取时间、账号修订和凭据版本的 SHA-256 内容指纹，不是秘密或云幂等令牌。它将后续提交绑定到实际显示内容；相同实例的名称、属性、数据时间或账号配置变化都会使旧确认失效。

execution_enabled 在此阶段始终 false；有 token 只表示预览证据符合首期范围，不意味着已验证云写权限或已开放提交。开发模式提供与当前示例实例状态一致的合成影响：运行中 ECS 阻止、SWAS 不支持，不伪造可执行任务。

## 限制和 PRD 修订建议

DescribeInstances 与 DescribeDisks 是先后查询，没有跨接口原子快照或条件删除版本。后续 worker 必须重新读取并比较名称、状态、保护/锁和完整磁盘影响，然后持久化发送边界；仍不能保证另一云端操作者不会在最终检查后变更属性。此限制必须保留，不能声称 token 提供云端并发锁。

后续提交仍须输入**实际实例名**二次确认，走持久队列、共享实例操作互斥、配置变更检查及发送后不重放。尚未实现这些路径，不将预览列为 F7.4 完成。固定调用层见 [ECS 释放](ecs-release.md)。

建议 F7.4 补充本次确认的释放范围、现有删除属性及安全锁例外；补充数据过期/信息不全时不开放提交、预览与云端并发变更的限制。第 7 节补充 DescribeDisks / ecs:DescribeDisks 作为影响发现来源。PRD 原文未改，RAM 草稿仅新增读取动作，没有向真实身份授权。

## 验证

本地测试覆盖停止/运行/包年包月、名称缺失、保护/锁/未知字段、云盘/本地盘/快照影响、多重挂载/共享盘/归属矛盾、磁盘游标分页及 TotalCount 例外、时间边界/未来时间、失败保留与成功清空、旧尝试、凭据轮换/账号停用/实例消失、事务回滚/外键及会话/无 GET 云调用。既有完整包测试同时检查 RAM 部署文件与只读权限分层。

实际定向命令（exit 0）：

```text
go test ./internal/model ./internal/cloud/aliyun ./internal/store ./internal/api ./internal/devmode ./internal/rampolicy -run 'TestRelease|TestSignedDisk|TestInventory' -count=1
ok github.com/JasperXzy/harbormaster/internal/model 0.735s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 0.347s
ok github.com/JasperXzy/harbormaster/internal/store 0.487s
ok github.com/JasperXzy/harbormaster/internal/api 0.563s
devmode、rampolicy 显示 [no tests to run]，随后单独运行的完整包测试包含它们。

go test ./internal/cloud/aliyun ./internal/model ./internal/store ./internal/api ./internal/devmode ./internal/rampolicy -count=1
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 3.517s
ok github.com/JasperXzy/harbormaster/internal/model 0.487s
ok github.com/JasperXzy/harbormaster/internal/store 7.126s
ok github.com/JasperXzy/harbormaster/internal/api 18.493s
ok github.com/JasperXzy/harbormaster/internal/devmode 1.306s
ok github.com/JasperXzy/harbormaster/internal/rampolicy 0.309s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 63ms
Woohoo! Your API description is valid.
```

首次契约 lint 发现两处 YAML 内联说明中的未加引号逗号及 nullable 缺少 type；已修正并重新通过，没有采用忽略规则。

最终检查（均 exit 0）：

```text
go build ./...
（无输出）
go vet ./...
（无输出）
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 9.583s
ok github.com/JasperXzy/harbormaster/internal/api 122.701s
ok github.com/JasperXzy/harbormaster/internal/backup 45.007s
ok github.com/JasperXzy/harbormaster/internal/cli 20.045s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.546s
ok github.com/JasperXzy/harbormaster/internal/collect 13.375s
ok github.com/JasperXzy/harbormaster/internal/devmode 14.136s
ok github.com/JasperXzy/harbormaster/internal/heartbeat 3.191s
ok github.com/JasperXzy/harbormaster/internal/m0 1.581s
ok github.com/JasperXzy/harbormaster/internal/model 1.339s
ok github.com/JasperXzy/harbormaster/internal/notify 2.222s
ok github.com/JasperXzy/harbormaster/internal/power 21.172s
ok github.com/JasperXzy/harbormaster/internal/probes 12.783s
ok github.com/JasperXzy/harbormaster/internal/rampolicy 1.312s
ok github.com/JasperXzy/harbormaster/internal/retention 2.381s
ok github.com/JasperXzy/harbormaster/internal/store 164.436s
ok github.com/JasperXzy/harbormaster/internal/traceinfo 7.285s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。
git diff --check
（无输出）
```

官方依据（2026-11-13 核对）：[DeleteInstance](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-deleteinstance)、[DescribeDisks](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describedisks)、[DescribeInstances 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstances/api.json)。前次真实只读来源与未覆盖样本见[闲置来源验证](idle-resource-sources.md)，不能把本批本地测试冒充真实正例或释放验收。

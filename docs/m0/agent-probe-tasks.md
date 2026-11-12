# Agent 固定测速任务队列与私有接口

2026-11-12；F10.1–F10.4、F12.6、F14。执行器见 [network-probes.md](network-probes.md)。本阶段实现服务端持久队列、领取/回报端点、结果校验；agent 客户端加密待办和执行循环是接下来的接入步骤。尚未向真实 agent 下发任务、未运行公网探测。

## 队列与身份

迁移 028 为既有 job 表增加 `network_probe` 类型，保留原任务、排序、变更序列、CDT 观测、通知关联、电源任务/意图/API Key 关联及 heartbeat_test。没有关掉 SQLite 外键。新 network_probe_job 存固定请求、绑定实例/agent、当时 token 哈希、账号 credential_version、请求去重键和原始结果。没有保存明文 token。

入队方法 `EnqueueAgentProbe` 只接受已纳管、发现快照仍存在、账号启用且已有未撤销 agent 的实例。请求必须通过执行器的目标、方式、包数和时间限额验证；IP 字面量地址族不一致在入队前拒绝。请求去重键 16–128 个字母/数字/下划线/连字符；相同键及相同实例/参数始终返回原任务，参数变更返回冲突，不因原任务已结束而再次执行。

同一 agent 最多 100 个未完成任务，每次只领取一个。FIFO 按既有 job_order 排序，同一 agent 有运行任务时其他领取者不获得第二个任务。不同 agent 的队列独立。采集 worker 不能领取这些任务，通用 RenewJob / FinishJob / RetryJob 也不能越过专用流程改写。

工程限额：待领取最长 5 分钟、领取后租约 5 分钟。过期任务终止为 `probe_expired`，不重新入队、不重放；一次探测本身仍由执行器限制为最多 65 秒。调度循环和专用领取/入队路径清理过期任务；账号停用、凭据版本变化、token 更新/撤销或资源快照消失使未完成任务失效。备份恢复沿用用户确认的取消未完成任务规则。

## 私有协议

仅通过生产 HTTPS agent 路由，复用既有 Host / 受信反代校验；不以管理员 Cookie 替代 agent Bearer token。开发模式不开放真实任务领取。请求 JSON 拒绝未知字段，不能附带任意 command。

`POST /agent/v1/probes/claim`：

```json
{"agent_id":"agent_example","owner":"<32随机字节的64位十六进制编码>"}
```

服务端从 token 哈希确认 agent 身份及当前绑定，不信任请求中的 agent_id。响应 `{task:null,server_time}` 表示暂无可领取任务；有任务时 task 包含 id、owner、request 和 expires_at。request 是内置 netprobe.Request：host、family、method、port、count、timeout_ms、interval_ms。

owner 是持久化的领取请求标识，不是认证凭据。领取响应丢失后，同 owner 重试返回原任务和原到期时间，不延长租约；其他 owner 不能同时领取此任务。agent 客户端必须先持久化 owner，收到任务后在执行前持久化开始记录，重启后不能把“已经开始但没有结果”当作未执行。这部分将在客户端接入阶段验证。

`POST /agent/v1/probes/result`：agent_id、task_id、owner、result、error_code。result 为逐次探测及时间/解析地址记录；error_code=null 代表完整测量，允许保留失败探测（例如全部超时），并不意味着目标可达。本地执行失败/取消/重启中断使用固定错误码，summary=null。

服务端校验当前 token、agent 绑定、账号凭据版本、任务 owner、租约，校验地址只属于公网且与请求地址族/字面量相符、时间顺序、样本数和序号、RTT 有限范围、固定错误码。它从逐次 RTT **重新计算**摘要，不能靠客户端写一个成功率或平均值替换原始结果。鉴权和结构校验不构成远程测量的可信硬件证明。

结果及 job 的成功/失败在同一事务提交。相同规范化结果重传返回 `{task_id,duplicate:true}`；不同内容返回 409，不覆盖先前结果。撤销 token 后连旧结果重试也不能写库。过期/被取消的任务不接受迟到结果冒充成功。

HTTP 结果：认证失败 401，账号不可用 403，不存在 404，过期/冲突 409，结构错误 400，存储临时故障 503。只返回固定错误码，不输出 token、原始错误或 SQL。管理员不可通过这个接口任意指定执行命令或获取其他 agent 的任务。

## 面板与后续接入

已按“先改契约”增加公开 JobType 的 network_probe 枚举，任务列表/详情可识别新类型；本阶段 Job.result 保持 null，原始结果存专用表。尚无面板创建测速任务或历史查询路由，不要求前端现在显示一个不可用的测速操作。

下一阶段完成 agent 加密待办（领取前标识、开始标记、待回报结果、ACK 清理）、独立执行/投递循环、版本能力与原始 socket 权限配置，再提供 server 本机执行、目标与组合管理、默认五分钟调度及历史接口。线路追踪、离线 ASN 库和网络告警另行接入。当前实现不能代表上述功能已经完成。

## 已有验证

本地真实 SQLite + HTTPS 路由测试覆盖领取鉴权、未知字段拒绝、并发领取、同 owner 幂等领取、请求键冲突、完成/回报幂等、不同结果拒绝、服务端重算摘要、过期不重放、撤销/停用/凭据轮换/资源消失/恢复取消及通用 worker 隔离。迁移测试保留已有 heartbeat_test 的关联和运行状态，不增加虚假 job_change；旧余额和账单保留，foreign_key_check 通过。已有电源迁移测试也经过新迁移。

## 实际检查命令与输出

```text
go test ./internal/store ./internal/api ./internal/collect
ok github.com/JasperXzy/harbormaster/internal/store 4.111s
ok github.com/JasperXzy/harbormaster/internal/api 16.581s
ok github.com/JasperXzy/harbormaster/internal/collect 1.128s

go test ./internal/netprobe ./internal/store -run 'Probe|ValidateRemote'
ok github.com/JasperXzy/harbormaster/internal/netprobe 0.395s
ok github.com/JasperXzy/harbormaster/internal/store 0.677s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 85.688s
ok github.com/JasperXzy/harbormaster/internal/backup 41.941s
ok github.com/JasperXzy/harbormaster/internal/cli 14.017s
ok github.com/JasperXzy/harbormaster/internal/collect 9.896s
ok github.com/JasperXzy/harbormaster/internal/netprobe 1.804s
ok github.com/JasperXzy/harbormaster/internal/store 75.272s
其余测试包同样通过或 cached/no test files；exit 0。

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0（六项为既有警告，未新增）
```

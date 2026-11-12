# 已纳管实例间双向追踪

2026-11-13；PRD F10.10。用户已确认：同一次请求原子创建两端任务，提交时任一端不可用则都不创建；入队后各自领取、执行、保存时间与结果，不要求毫秒同步。沿用现有固定 trace 动作、TLS/agent token、持久队列和[加密恢复](agent-trace-tasks.md)，不新增 agent 指令或云权限。

## 接口与数据

先更新 OpenAPI 顶部变更记录，再实现：

- `POST /api/v1/network/duplex-traces`：管理员 Session + CSRF，返回 `202 {duplex_id, forward:{job_id,trace_id}, reverse:{job_id,trace_id}}`。
- `GET /api/v1/network/duplex-traces/{id}`：冻结的 from/to 实例、名称、产品类型、公网 IP 及发现时间，以及各自完整 trace 详情和已保存归属。
- `GET .../{id}/export`：一个 UTF-8 附件包含双端信息、两份原始逐跳、实际开始/结束时间、已有数据库版本与署名。
- 普通 trace 历史/详情新增可空 duplex_id / duplex_direction，前端可从历史回到整对查询；非双向 trace 为 null。

两个实例必须不同，均为已纳管 ECS 或轻量，并有对应当前凭据版本的完整、新鲜、唯一所选地址族公网 IP。两边的 agent 都需有效注册、当前心跳和独立的 trace 轮询能力证据。使用既有 10 分钟地址新鲜度、默认 30 秒心跳和 5 分钟 trace 轮询判断，不由普通测速轮询猜测线路能力。

请求明确包含 from_instance_id、to_instance_id、family、method、forward_port、reverse_port。ICMP 两端 port=0；TCP 各方向分别指定 1–65535，可用不同端口。每端 max_hops/count/timeout_ms 默认 30/3/1000，沿用单条 trace 的发包预算与固定动作。源和目标都来自本地发现快照，HTTP 请求中没有 DNS、云查询或发包。

request_key 的格式与单向提交相同，命名空间属于双向接口；相同 key、相同解码及默认值返回原双向 ID 和两个任务，即使已经完成、账号/地址后来改变。改变输入复用 key 返回 request_conflict。两个公网地址相同时返回 duplex_same_address，不把同一个出口地址解释成两个确定的端点。

独立 TCP 端口、双向接口的 key 命名空间和相同公网地址拒绝是本次明确记录的工程选择，不是 PRD 已有的字段定义。源/目标类型、公网范围与发现缺口的语义仍沿用已确认规则。

## 事务与执行边界

迁移 036 增加 duplex_trace 和 duplex_trace_leg。前者保存输入、创建时间及双端观察；后者保存 forward/reverse 与已有 trace 的关系，每条 trace 只能属于一对的一个方向。一个事务内提交两个原始 job、trace 历史和关联；第二端源不可用或队列满也回滚第一端，不留下半对任务。数据库中的内部随机 request key、agent token 哈希和 lease owner 均不进入面板或导出。

agent 不需要知道“整对”概念，仍按自己的 fixed trace 协议执行；两个点拥有各自租约，可同时进行，普通测速与线路的单点互斥继续生效。各端原始结果、到达状态、错误和后台归属独立更新。只完成一个方向时另一个仍为 queued/running/failed/null，不能补出结果。TCP reset 表示到达但端口关闭，不能视为服务成功；中间沉默和 ASN 缺口的原语义保持。

领取前会重新检查双端资源的地址证据。任一地址改变、缺失、多地址、过旧或发现不再有效，会取消仍排队的方向（probe_target_changed），保留原端点快照；已开始的测量保留其原始请求和接受结果。入队后只因另一个 agent 离线/撤销，不取消能独立执行的方向；被撤销源自己的任务仍按 token/凭据绑定失效规则终止。

GET 只读持久数据，不执行到期清理、地址刷新、探测或归属查询。名称/地址变化不会改写旧记录。备份恢复按已有政策取消旧 queued/running 工作，保留已完成的一端及双向关系；复用原 key 也不会重跑旧任务。

“去程/回程”指两端分别执行的观察。实际起点可能不同，网络路径和 TCP 目标端口也可能不同；本接口不据此推出对称路由或同步测量。

## 前端与开发模式

前端由 Claude 按契约实现，读取两端 trace/result/enrichment 分别展示，不把单个通用 job 的完成当作整对成功。真实证据仍由各端 started_at、finished_at 和 status 判断。请求完成后通过 duplex_id 轮询即可；历史行含关联入口。

开发模式提供 `demo-duplex`，两端 trace ID 为 3/4，同时出现在普通历史和详情中。合成数据包含不同开始时间和 443/8443 TCP 端口，导出保留 DB-IP 署名。原单向示例 1/2 与每日快照示例保留。POST 仍为 501，只读示例不创建真实任务、注册或网络请求。

## 验证方法与结果

使用临时 SQLite、两个按私有协议创建的本地注册/心跳/轮询身份和注入的逐跳结果，覆盖：IPv4/IPv6、ECS＋轻量混合端点、四个并发重试仅一对、改变输入冲突、第二端撤销/队列满全事务回滚、同地址与发现歧义拒绝、已运行一端与排队另一端的地址变化、单端撤销后的独立完成、重复上报 ACK、历史冻结、备份保留已完成方向/取消旧排队方向及外键完整性。HTTP 测试覆盖 Session/CSRF、严格字段/预算、只读 GET、幂等、导出与敏感字段不外露。未向公网发包。

```text
go test -race ./internal/store ./internal/devmode -run 'Test(Duplex|DemoTraces)' -count=1
ok internal/store 18.844s
ok internal/devmode 6.587s

go test -race ./internal/api ./internal/devmode -run 'Test(Duplex|DemoTraces)' -count=1
ok internal/api 5.289s
ok internal/devmode 6.875s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0，均为已有警告

go test -race ./...
exit 0，全部包通过。本次重新执行：
internal/agentclient 21.532s
internal/api 195.710s
internal/backup 92.631s
internal/cli 42.574s
internal/cloud/aliyun 4.526s
internal/collect 28.150s
internal/devmode 32.263s
internal/heartbeat 5.990s
internal/model 1.381s
internal/notify 3.723s
internal/power 42.648s
internal/probes 23.383s
internal/store 233.514s
internal/traceinfo 17.132s
其余 cached 或 no test files。
```

实际使用 `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o <临时路径> ./cmd/hm-server` 交叉构建六个目标，全部 exit 0：Linux amd64/arm64 37,078,753/35,308,849 字节，macOS 37,278,160/35,787,682，Windows 37,220,864/35,127,296。产物在临时目录 hm-duplex-build-k4gafgel，未部署。agent 程序和协议本阶段未改；现有实现负责各方向的固定任务执行。

结论：双向请求与持久协议组合在本地验证；不是两个真实阿里云实例的端到端线路验收。真实 agent 安装、raw socket 权限、公网执行、实际双端时间差和路径数据仍需要对应部署与授权条件。PRD 修订建议：F10.10 补充已确认的原子提交/独立执行边界，以及排队地址失效与历史保留规则。PRD 原文未修改。

# Agent 线路任务队列与私有协议

2026-11-13；接续[内置线路执行器](network-trace-executor.md)。服务端持久任务、私有领取/回报、结构校验，以及 agent 加密待办、执行/回报与恢复流程已实现。没有新增面板提交入口，也没有执行真实公网探测。

## 存储与兼容

迁移 032 为 `network_probe_job` 新增 `action=probe|trace`，旧记录默认 probe；复用既有 job、实例/token 哈希/凭据版本绑定、五分钟排队期限和五分钟执行租约。公开 JobType 保持 network_probe，通用 Job.result 仍为 null；契约顶部已解释线路任务也属于该类型。没有重建 job 表或改写已有测量结果。

仅类型化的 `EnqueueAgentTrace` 可以建立线路请求，入队先校验固定 TraceRequest。request_key 与普通测速共用全局唯一域，长度 16–128、仅字母数字下划线连字符；同 key、同实例、同动作和同配置返回原任务，修改任一项产生冲突。完成后仍保留去重记录。同一 agent 的未完成测速与线路任务合计上限 100。

同一 agent 同时至多一个运行任务，测速/线路共用这个限制。各动作内部 FIFO；普通测速的旧领取接口只查 probe，不能取得 trace 请求；线路领取接口只查 trace。同 owner 的同动作领取重试返回原租约，不续期。其他 owner 或另一动作在租约占用时得到空任务。

过期任务终止，不重排；撤销/更换 token、凭据版本变化、账号停用或实例不再出现都会取消旧任务。备份恢复沿用用户确认的取消未完成任务语义。通用采集 worker 不能领取、续租或结束这些任务。

## 私有 HTTP 接口

`POST /agent/v1/traces/claim`：JSON `{agent_id, owner}`，owner 为随机 32 字节的 64 位十六进制表示。返回 `{server_time, task}`；无任务时 task=null；任务为 `{id, owner, request, expires_at}`。request 使用 TraceRequest 的 host、family、method、port、max_hops、count、timeout_ms。

`POST /agent/v1/traces/result`：JSON `{agent_id, task_id, owner, result, error_code}`。result 是逐跳 TraceResult，保留每次原始结果；完成时 error_code=null，本地失败/取消/恢复发现已开始任务时使用固定错误码。返回 `{task_id, duplicate}`。

两者仅接受 HTTPS 正确主机名、实例专用 Bearer token；管理员 Cookie、API Key 和 CSRF 不替代 agent 鉴权。开发模式不开放这些入口。领取体最多 4KiB，回报最多 128KiB；未知 JSON 字段、额外 JSON 对象与任意 command 字段拒绝。

错误沿用固定探测错误码：401 invalid_agent_token、403 account_disabled、404 not_found、409 probe_expired / probe_result_conflict、400 invalid_probe / invalid_report、503 probe_unavailable。传输前置检查另有 403 https_required、415 unsupported_media_type。不返回底层数据库、DNS 或系统错误正文。

## 回报校验

在一个 SQLite 事务内核对当前 token 哈希、实例/agent、凭据版本、owner、任务动作与租约。验证地址族、固定目标/解析列表、最多 30 跳/每跳 5 次、顺序连续、时间窗口、有限非负 RTT、ICMP 类型/代码和固定失败原因。响应节点允许私网地址，因为它只是收到的中间路由观测；目标和解析列表仍须全部是公网。

服务端从逐次响应重算 reached 与 stop_reason，不保存客户端自报的任意结论文字。没有结果的跳不能伪造为 0ms；取消/本地失败允许最后一跳未完成，保持 incomplete；完整回报必须完成当前跳，并在终止响应处停止，或达到最大跳数。TCP RST 算到达目标但不表示服务可用。

校验后的规范 JSON 与任务终态原子写入；完全一致的重试返回 duplicate=true，原始观测改变则冲突。迟到或已取消的任务不接受新结果；已经保存的相同结果可以重取 ACK，但仍须通过当前鉴权。结构校验不证明远程 agent 的测量真实，不能代替远程可信证明。

线路完成不会触发普通测速的 latency/loss/unreachable 告警，不能将中间路由器不响应套用为端到端异常。Job succeeded 仅表示完整执行并接受结果，即使停止原因是 unreachable 或 max_hops。

## 客户端执行与恢复

hm-agent serve 的网络任务 worker 已调用 NetworkOnce，轮流处理测速和线路。两种动作共用进程内执行锁以及服务端单点租约，避免同时发包；每轮完成后等待 1 秒、空闲等待 5 秒、错误退避最长 60 秒。空闲时每类约 10 秒领取一次，另一类正在执行时需等其完成；这是轮询选择，不更改流量默认 10 秒上报周期。普通测速与线路各有一个加密待办槽，互不覆盖。run-once 仍只采样/上传流量，不额外执行网络任务。

线路使用固定 TraceOnce 流程：

1. 先把随机 owner fsync 到已有 AES-256-GCM 日志，再请求领取；领取响应丢失时复用 owner，服务端不会延长原租约。
2. 校验任务 ID、owner、公网目标、包数/跳数和租约，再 fsync 开始标记；落盘失败不执行。
3. 执行截止时间取 `min(125秒, 服务端剩余租约 - 完整领取请求耗时 - 5秒)`，不使用本机墙钟推算服务端租约；预算耗尽时不发包。只调用内置 netprobe.Trace。
4. 校验并加密保存完整或部分结果，然后上传。错误正文不进入结果；写盘失败返回固定状态错误，服务停止以保留已有状态。
5. 只有匹配任务 ID 的 ACK，或明确的任务过期/不存在响应，才清除对应待办。ACK 丢失后只重传同一结果。冲突、错 ID、临时网络错误保留结果；重启发现有开始标记但无结果，仅报告 probe_interrupted，绝不重跑。

任务取消时保存部分结果供下次启动上传。流量采样和批量上传不获取网络任务执行锁；长线路探测期间仍可继续采样/上报。服务退出先取消并等待 worker，再关闭共享加密日志。更换身份/token 或重新注册会同时清除旧测速和线路待办；主密钥仍由数据目录外提供。加密日志压缩及流量 ACK 不会丢弃线路记录。

server 测速点的线路执行现已接入，见[本机线路任务](server-trace-tasks.md)。面板线路提交、历史及丰富结果信息仍需接入。没有安装真实 agent，没有声称 Linux 原始套接字或真实公网路径已验收。

## 本地验证

测试覆盖旧任务迁移为 probe、非法动作 CHECK、两种任务共享租约、并发领取只产生一个执行者、同 key 去重/跨动作冲突、旧 agent 无法消费线路请求、错误回报不结束任务、结果规范化、重复 ACK、撤销/换 token/停用/发现消失/凭据变化/过期/恢复终止。模拟 HTTP 覆盖 HTTPS、主机、独立 token、未知字段、体积限制和开发模式关闭；没有调用线路执行器或向公网发包。

结果校验测试包括时间、地址、次数、顺序、NaN/Inf、缺失字段、混合失败、错误 ICMP/TCP 类型、任意文本拒绝，以及执行器真实生成结构经过 JSON 往返后可被接收。

```text
go test -race ./internal/netprobe ./internal/store ./internal/api
ok github.com/JasperXzy/harbormaster/internal/netprobe 1.445s
ok github.com/JasperXzy/harbormaster/internal/store 101.115s
ok github.com/JasperXzy/harbormaster/internal/api 91.384s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid.
You have 6 warnings.
exit 0；原有 license、两个公共操作的 4xx 声明和三个未引用 schema 警告。

go test -race ./...
exit 0，全部包通过。此次重新运行的包：
internal/agentcli     1.685s
internal/agentclient  4.165s
internal/agentstate   1.919s
internal/backup       41.661s
internal/cli          14.228s
internal/collect      10.641s
internal/devmode      7.191s
internal/heartbeat    3.169s
internal/notify       2.217s
internal/power        17.111s
internal/probes       4.347s
其余 cached 或 no test files。
```

## 客户端阶段验证

使用本机真实 TLS 服务、SQLite 持久任务、加密日志与注入的固定线路执行函数，覆盖领取/ACK 丢失后的两次重启、开始标记恢复不重跑、执行时仍能上传流量、取消保存结果、错 ACK/冲突保留、过期清除、私网目标/任意命令拒绝、写盘失败不发包、服务端时钟偏差不延长执行、两种网络任务串行轮换。日志检查验证 token、owner、任务 ID 和目标未以明文写入 state.enc；并验证压缩后的线路、测速和流量待办相互独立。没有真实公网发包。

```text
go test -race ./internal/agentstate ./internal/agentclient ./internal/agentcli
ok github.com/JasperXzy/harbormaster/internal/agentstate 2.067s
ok github.com/JasperXzy/harbormaster/internal/agentclient 6.643s
ok github.com/JasperXzy/harbormaster/internal/agentcli 1.381s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
exit 0，全部包 cached 或 no test files（包含上面已实际运行的三包）。

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o <临时路径> ./cmd/hm-agent
PASS，12012393 bytes
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o <临时路径> ./cmd/hm-agent
PASS，11107975 bytes
```

产物在系统临时目录 `hm-agent-trace-build-yyueloww`，未上传或替换真实机器程序。没有修改公开 HTTP 字段，本阶段无需调整 OpenAPI。

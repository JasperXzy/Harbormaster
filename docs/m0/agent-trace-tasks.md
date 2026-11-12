# Agent 线路任务队列与私有协议

2026-11-13；接续[内置线路执行器](network-trace-executor.md)。本阶段完成服务端持久任务、私有领取/回报及结构校验；agent 客户端的加密待办和执行循环尚未接入线路动作。没有新增面板提交入口，也没有执行真实探测。

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

## 后续接入

继续实现 agent 加密保存领取 owner、开始标记、回报和 ACK；崩溃后不重复执行已经开始的线路任务。执行窗口需使用服务端剩余租约减去请求 RTT 与安全余量，并采用线路的 125 秒上限。随后接入面板线路提交、历史、server 测速点线路执行及丰富结果信息。当前 hm-agent 尚不会轮询新增接口；没有声称真实 Linux/网络已验收。

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

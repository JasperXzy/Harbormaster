# Agent 固定测速任务队列与私有接口

2026-11-12；F10.1–F10.4、F12.6、F14。执行器见 [network-probes.md](network-probes.md)。已实现服务端持久队列、领取/回报端点、结果校验，以及下文记录的 agent 客户端加密待办和独立执行循环。尚未向真实 agent 下发任务、未运行公网探测。

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

owner 是持久化的领取请求标识，不是认证凭据。领取响应丢失后，同 owner 重试返回原任务和原到期时间，不延长租约；其他 owner 不能同时领取此任务。agent 客户端必须先持久化 owner，收到任务后在执行前持久化开始记录，重启后不能把“已经开始但没有结果”当作未执行。客户端现已按下面的恢复测试实现并验证此流程。

`POST /agent/v1/probes/result`：agent_id、task_id、owner、result、error_code。result 为逐次探测及时间/解析地址记录；error_code=null 代表完整测量，允许保留失败探测（例如全部超时），并不意味着目标可达。本地执行失败/取消/重启中断使用固定错误码，summary=null。

服务端校验当前 token、agent 绑定、账号凭据版本、任务 owner、租约，校验地址只属于公网且与请求地址族/字面量相符、时间顺序、样本数和序号、RTT 有限范围、固定错误码。它从逐次 RTT **重新计算**摘要，不能靠客户端写一个成功率或平均值替换原始结果。鉴权和结构校验不构成远程测量的可信硬件证明。

结果及 job 的成功/失败在同一事务提交。相同规范化结果重传返回 `{task_id,duplicate:true}`；不同内容返回 409，不覆盖先前结果。撤销 token 后连旧结果重试也不能写库。过期/被取消的任务不接受迟到结果冒充成功。

HTTP 结果：认证失败 401，账号不可用 403，不存在 404，过期/冲突 409，结构错误 400，存储临时故障 503。只返回固定错误码，不输出 token、原始错误或 SQL。管理员不可通过这个接口任意指定执行命令或获取其他 agent 的任务。

## 面板与后续接入

已按“先改契约”增加公开 JobType 的 network_probe 枚举，任务列表/详情可识别新类型；本阶段 Job.result 保持 null，原始结果存专用表。尚无面板创建测速任务或历史查询路由，不要求前端现在显示一个不可用的测速操作。

客户端加密待办、独立执行/投递循环和原始 socket 权限模板已按下一节接入。随后继续提供 server 本机执行、目标与组合管理、版本能力展示、默认五分钟调度及历史接口。线路追踪、离线 ASN 库和网络告警另行接入。当前实现不能代表上述功能已经完成。

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


## Agent 客户端接入

`hm-agent serve` 启动独立测速循环，与原有流量采样和批量上报循环并行。空闲每 5 秒领取；完成一项后至少等待 1 秒；服务端临时失败或旧 server 不支持接口时退避至 60 秒，不能因此停止流量上报。日志只记录固定事件及重试次数。共享加密日志损坏/无法持久化时退出服务，不在没有开始记录的情况下继续执行。

加密 journal 增加一个有界 ProbeWork，复用 AES-256-GCM、外置主密钥、进程锁、fsync 和现有崩溃恢复/压缩机制：

1. 先保存随机 owner，再请求领取；空队列复用该 owner，不每次轮询写新状态。
2. 收到任务后再次验证参数和 owner，先保存 task 作为开始标记，再调用内置执行器。
3. 执行完成或取消后先保存逐次结果和固定错误码，再发回服务端；结果发送失败只重传同一结果。
4. 收到匹配 task_id 的 ACK 才清除待办；服务端明确返回 probe_expired / not_found 时清除不能再提交的旧待办。冲突或不匹配 ACK 不清除证据。
5. 重启时已有 task、尚无 result，回报 probe_interrupted，不重新运行；已有 result 则继续投递。显式重新注册或更换 token 清除旧身份的待办。

执行 deadline 从“服务端剩余租约 − 整个领取请求耗时 − 5 秒余量”推导，再受 65 秒上限约束，不依赖 agent 墙钟延长租约。进程取消会取消执行器；结果保存后留给下次启动投递。正常退出先取消并等待两个后台 worker，再关闭 journal，避免后台继续写已关闭文件。

TCP 及 ICMP 均通过同一个固定执行器，服务器不能传入命令。Linux unit 增加 CAP_NET_RAW，能力集合为 CAP_NET_ADMIN + CAP_NET_RAW；没有改成 root。升级包仍使用现有构建脚本复制这份源 unit。本次没有安装或更新任何真实服务，旧机器的实际 unit 不会因仓库文件变化而自动获得能力。

### 客户端验证

本机 TLS server 使用真实 agent 路由、SQLite 队列和 token 认证；测试仅替换目标测速函数，返回受控样本，不向公网探测。

- 领取响应在服务端落库后丢失：重启后重用 owner，取得原任务；执行次数为 1。
- 结果响应在服务端落库后丢失：重启后重传加密待办，服务端返回 duplicate，结果行数保持 1，不再执行。
- 仅有持久开始标记就重启：服务端得到 probe_interrupted，执行器调用次数为 0。
- 测速函数阻塞时，流量 Sample + Flush 仍成功写入服务端 traffic_sample；取消后测速结果持久化，下一轮只回报。
- 未匹配 owner、私网目标、未知 command 字段、错误 ACK 和结果冲突均被拒绝；旧 server 的 404 不停止流量能力；明确过期会清理待办。
- journal 经过真实关闭/重开和压缩，任务、结果、计数样本与 ACK 各自保留；原文件不出现 token、owner、目标或任务 ID 明文；调用方修改返回对象不能改写已提交状态。

这证明本地协议与恢复路径，不代表 Linux 原始 ICMP 套接字、真实安装、长期负载或前端网络页已经验收。M3 尚未完成。

### 客户端接入阶段命令与输出

```text
go test -race ./internal/agentstate ./internal/agentclient ./internal/agentcli
ok github.com/JasperXzy/harbormaster/internal/agentstate 2.135s
ok github.com/JasperXzy/harbormaster/internal/agentclient 3.178s
ok github.com/JasperXzy/harbormaster/internal/agentcli 1.416s
```

补充后台循环退出测试后，agentcli 定向 race 为 1.964s，其余两包 cached。

```text
go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
全部测试包 cached 或 no test files，exit 0。

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o <私有临时目录>/hm-agent-linux-amd64 ./cmd/hm-agent
linux/amd64 built 11842078 bytes
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o <私有临时目录>/hm-agent-linux-arm64 ./cmd/hm-agent
linux/arm64 built 10942140 bytes
```

两个二进制输出到 `/var/folders/5p/9wnngjzj1k9f1z4hlc3j9x8m0000gn/T/hm-agent-probe-build-rfk_8pit`，未提交仓库、未上传机器。交叉编译通过不代替目标架构运行验收。本客户端阶段没有面板契约变化。

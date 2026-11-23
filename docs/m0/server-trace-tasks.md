# hm-server 本机线路任务

2026-11-13；沿用[线路执行器](network-trace-executor.md)与[私有线路队列](agent-trace-tasks.md)，补齐面板服务器作为测速点时的后台线路执行。面板提交、历史和导出已接入，见[线路面板接口](trace-panel-api.md)；不会因启动服务自动产生线路探测。

## 队列与运行

`EnqueueServerTrace` 校验固定 TraceRequest 后写入 `network_probe_job`，point_kind=server、action=trace，实例/agent/token/凭据版本均为空。沿用迁移 032，未新增数据库迁移或改变公开 API 字段。请求 key 与已有测速/agent 任务共用唯一域；同请求返回已有任务，跨动作、跨测速点或参数不同则冲突。

server 测速和线路共用最多 100 个未完成任务的限制，以及一次只运行一个任务的租约。server/agent 只能领取各自任务；本机任务不需要实例 token，也不会被普通采集 worker 领取。普通测速与线路各自 FIFO，生产 serve 的本机网络 worker 轮流处理两类动作，避免同类持续排队导致另一类无法运行。空闲每轮等待 2 秒，有完成任务时等待 1 秒；每种动作空闲时约 4 秒检查一次。正在执行另一类任务时需等其完成。

每项任务排队最长 5 分钟，执行租约 5 分钟。领取后再次核对 job、owner、动作、请求全文和租约才执行；修改请求或备份恢复取消的任务不能通过此检查。线路执行时限为 `min(125秒, 剩余租约 - 5秒)`。只调用内置 netprobe.Trace，不执行外部程序或应用数据。

退出/取消先停止线路执行，再以独立、最多 5 秒的本地事务保存部分结果和固定错误码。验证结果结构并重算 reached/stop_reason 后，结果与 job 终态在同一事务写入。任务 succeeded 表示完整执行，不意味着目标可达；线路结果不触发普通测速告警。

进程崩溃留下的 running 任务不会由新进程重新执行，只等待租约到期后标记 probe_expired；与普通测速一致。备份恢复会取消未完成任务。本机 worker 与 agent 流量接收、云采集、外部心跳分别运行；本次未改变它们的业务逻辑。run-once 和开发模式不启动此 worker。

## 平台与验收范围

Linux raw socket 仍需要 CAP_NET_RAW，已有 systemd/Compose 模板包含此能力，但本次未安装或授予真实机器能力。当前 TCP 线路执行器只支持 Linux 的端口保留，macOS/Windows 明确返回不可用；跨平台 server 编译支持不代表每种原始套接字功能都已可用。ICMP 也需要对应系统权限及运行验证。

单元测试通过注入执行函数验证派发、时限、成功/失败/取消持久化、固定错误脱敏、崩溃租约不重跑、server/agent 隔离、相同 key 去重、跨动作互斥及生产循环轮流处理。没有公网发包、SSH 操作、实际安装或真实云写操作。

面板契约、线路提交和历史现已接入；PTR/离线 ASN/地理信息、定时快照及双向追踪继续待实现；真实 Linux ICMP/TCP 线路运行仍未验收。

## 实际验证命令

```text
go test -race ./internal/store ./internal/probes
ok github.com/JasperXzy/harbormaster/internal/store 97.836s
ok github.com/JasperXzy/harbormaster/internal/probes 9.484s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
exit 0，全部包通过。此次重新运行的包：
internal/agentclient 8.716s
internal/api         91.626s
internal/backup      41.696s
internal/cli         14.948s
internal/collect     11.121s
internal/devmode     7.530s
internal/heartbeat   3.170s
internal/notify      2.150s
internal/power       17.531s
internal/probes      11.397s
其余 cached 或 no test files。

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o <临时路径> ./cmd/hm-server
PASS，26806640 bytes
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o <临时路径> ./cmd/hm-server
PASS，25127186 bytes
```

产物位于系统临时目录 `hm-server-trace-build-mok2hl_b`；没有替换当前部署。

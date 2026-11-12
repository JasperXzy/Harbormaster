# hm-server 本机测速 worker

2026-11-12，PRD F10.1 的本机测速点。与 [agent 执行路径](agent-probe-tasks.md)共用内置 ICMP/TCP 执行器，但队列身份和执行入口分开。没有发起公网探测、云操作或实际部署。

## 本阶段行为

迁移 029 将 network_probe_job 扩展为 point_kind=agent/server。已有 agent 请求、token 哈希、凭据版本、领取者和结果原样保留；server 行不包含实例/agent/token/云账号字段，由数据库约束防止混用。

`EnqueueServerProbe` 使用与 agent 相同的有限请求和持久请求键。相同键/请求返回原任务，键被其他测速点或不同参数使用时冲突；不会把已完成任务重新执行。本机最多 100 项未完成任务，按既有 job_order 顺序领取，每次只运行一个。server worker 不领取 agent 任务，agent 也不能领取本机任务。

本机 worker 在生产 `serve` 中独立运行；`run-once`、`migrate`、开发模式不执行本机测速。领取后持久化 running/owner/lease，再进行一次请求内容和租约校验。所有探测通过 netprobe.Run，单次最多 65 秒，保留五秒租约余量。进程取消后停止测量，再用独立的五秒数据库写入窗口保存取消结果；不会因服务取消而再发一次探测。

结果校验/摘要重算与 agent 相同，job 和结果原子落库。DNS、权限或本地 IO 错误保存为固定执行错误，summary=null；完整的连接/echo 超时才计入失败比例。服务中断后遗留的 running 任务不能被另一个 worker 再执行，到期后标为 probe_expired。备份恢复取消的任务不能通过旧租约完成或重新授权。

生产源代码已经接通 worker；本阶段仍没有面板创建测速任务的路由，正常启动但队列为空时不会向公网发送探测。目标/组合管理和结果查询将在下一阶段先写契约再接入。公开 job 类型仍为 network_probe、result 仍为 null，本阶段没有新增公开字段或路由。

## 部署模板

仓库 systemd 模板为 hm-server 专用非 root 用户增加 CAP_NET_RAW，保留 NoNewPrivileges 和其他沙箱设置。Compose 在 cap_drop=ALL 后明确请求 NET_RAW，不请求 NET_ADMIN；TCP 测速不需要原始 socket 能力。

[Compose 官方能力配置](https://docs.docker.com/reference/compose-file/services/#cap_add)说明 cap_add 请求的额外能力。配置文件存在不代表目标平台已赋予实际有效能力：本机无 Docker 运行验收，非 root 容器的 ICMP socket 和 systemd 环境仍需真实部署时验证。能力不足会得到 probe_unavailable，不据此声称目标丢包或不可达。本次只修改仓库模板，没有调整主机或远程服务权限。

## 已确认的后续调度规则

用户已确认：实例目标跟随所选地址族当前唯一公网 IP。发现不完整、没有公网地址或存在多个候选地址时跳过当次测速并保留原因，不能猜测目标。停服期间错过的测量不补测；恢复后安排一次当前测量，再按正常周期继续，历史缺口保留。当前本机 worker 尚未实现组合调度，不能把这两条决定标为已落地。

## 验证

本地真实 SQLite 和可控测量函数覆盖成功/失败落库、服务器重算摘要、固定错误码、取消后本地结果提交、并发 worker 只领取一个任务、server/agent 互不领取、持久键冲突、请求被修改时拒绝授权、恢复后旧租约不能提交，以及丢失领取者后不重放。CLI run-once 测试确认已有本机任务仍是 queued、attempts=0。执行器的真实 loopback TCP 和模拟 ICMP 验证见 network-probes.md；本轮没有扩展为真实原始 socket 验收。

迁移测试从旧 schema 028 升级，验证已有 agent 行所有字段及结果保留、job_change 不增加虚假事件、外键检查通过，且数据库拒绝将保留 agent 身份字段的行改为 server。

## 实际命令与输出

```text
go test -race ./internal/probes ./internal/store ./internal/cli -run 'ServerProbe|ProbeQueue|LocalProbes'
ok github.com/JasperXzy/harbormaster/internal/probes 6.561s
ok github.com/JasperXzy/harbormaster/internal/store 14.681s
ok github.com/JasperXzy/harbormaster/internal/cli 3.077s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 97.994s
ok github.com/JasperXzy/harbormaster/internal/backup 52.609s
ok github.com/JasperXzy/harbormaster/internal/cli 20.091s
ok github.com/JasperXzy/harbormaster/internal/collect 14.682s
ok github.com/JasperXzy/harbormaster/internal/probes 5.888s
ok github.com/JasperXzy/harbormaster/internal/store 92.427s
其他测试包均通过或 cached/no test files；exit 0。

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o <私有临时目录>/hm-server-amd64 ./cmd/hm-server
linux/amd64 built 26315930 bytes
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o <私有临时目录>/hm-server-arm64 ./cmd/hm-server
linux/arm64 built 24692948 bytes
```

产物在 `/var/folders/5p/9wnngjzj1k9f1z4hlc3j9x8m0000gn/T/hm-server-probes-build-0b7yzhkj`，未提交或上传；交叉构建不代替目标系统运行验收。本阶段无面板契约字段变化，未修改 web/ 或 docs/design/。

后续目标/组合管理、唯一实例地址跟随、恢复调度和面板历史接口已接入，见[网络管理](network-management.md)；以上“尚无面板路由”描述为本机 worker 交付阶段的历史状态。

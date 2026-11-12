# 内置线路追踪执行器

2026-11-13；PRD F10.7 / F12.6 的执行基础。入口为 `internal/netprobe.Trace`，支持 IPv4/IPv6 的 ICMP Echo 与 TCP SYN 逐跳探测。尚未接入持久任务、agent 领取/回报和面板 API，不能从生产接口触发。本阶段没有修改公开契约或前端。

## 固定动作与限制

输入为目标、地址族、icmp/tcp、TCP 端口、最大跳数、每跳次数和单次超时。复用测速器的公网地址校验、最多 5 秒 DNS 查询、全部答案检查和数字地址固定；任何非公网答案均拒绝整轮。中间路由器返回私网/链路本地 IP 是合法观测，但不会对这些 IP 发起探测或反查。

最多 30 跳，每跳 1–5 次，每次 100–2000ms；尝试间固定等待 100ms。`max_hops × count × timeout_ms + (max_hops × count - 1) × 100 <= 120000`，包含 DNS 的总截止时间为 125 秒。例如 30 跳 × 3 次 × 1000ms 可用。这些是工程资源限制，不是 PRD 新增的业务默认配置。至多发送 150 个探测报文；每个 TCP SYN-ACK 还可能触发一个固定 RST 清理报文。

使用 Go 原始套接字，不调用 ping/mtr/traceroute 或任意程序，不接受脚本、用户提供的数据包或应用数据。Linux 需要 CAP_NET_RAW。TCP 通过 bind 保留临时源端口，不调用 listen/connect；不会开设 TCP 服务，也不让内核自动重传探测 SYN。选取源 IP 时使用不发送数据的 UDP connect，读取 LocalAddr 后立即关闭。

TCP 端口保留目前实现于 Linux，其他系统返回 probe_unavailable；agent 本来只要求 Linux。ICMP 使用 Go/x/net 的跨平台接口，实际能力取决于系统与权限。编译通过不等于原始套接字运行已验证。

## 匹配与生命周期

- ICMP 接收套接字不连接远端，才能收到中间路由器的 Time Exceeded。IPv4 校验 ICMP checksum，IPv6 显式校验含伪首部的 checksum。EchoReply 必须来自固定目标，匹配 code、ID、序号和完整随机载荷。
- Time Exceeded 只接受 code=0。引用包必须匹配源/目标 IP、协议及至少 8 字节传输首部：ICMP 匹配校验和/ID/序号及可用随机载荷前缀；TCP 匹配两端端口和本次 SYN 序列号。支持 IPv4 options 和有界 IPv6 扩展首部；分片、长度异常或未知首部不猜测匹配。
- TCP 每轮随机起始序列号，每次递增。目标 SYN-ACK 或 RST-ACK 必须匹配端口、ACK=本次 SYN 序列号+1 和 checksum。SYN-ACK 后发送固定 RST，不发送握手 ACK 或应用负载。RST 证明到达目标但端口拒绝，不表示服务可用。
- ICMP 与 TCP 分别读取，单路超时不提前取消另一路；得到有效结果后设置读截止时间，等待全部读协程退出再开始下一次尝试。取消关闭套接字、释放端口；本地设置/权限/IO 失败只返回固定错误码。

## 结果与边界

逐跳保留每次尝试的序号、时间、响应 IP、RTT、类型/代码或超时。同一 TTL 的不同路由器分别保留，不把多路径响应合成一个 IP；无回复时地址和 RTT 为 null。停止原因是 reached、unreachable、max_hops 或 incomplete。收到终止响应后完成当前 TTL 的剩余尝试，再停止增加 TTL；取消或本地失败保持 incomplete。

到达最大跳数但没有目标响应，不证明目标不可达；中间跳不回应也不能直接解释为转发丢包。路由器限速/过滤及 ECMP 会影响观测。本实现不是固定流标识的 Paris traceroute，不承诺各次报文经过同一路径。

线路任务持久化、私有 agent 协议、加密恢复、面板契约与历史仍需接入。PTR、离线 ASN/地理库、线路识别、路径变化、定时快照、双向追踪和文本导出未实现。Linux 真实 raw socket、CAP_NET_RAW、NAT 引用包改写和 IPv6 回包仍需验证；本次没有安装 agent 或对公网发包。

PRD 修订建议：F10.7 的“逐跳丢包”应注明为探测响应缺失比例，不能把中间跳不回复直接判为转发丢包；TCP RST 要与连接成功分开。未修改 PRD 原文。

## 来源

- [RFC 792](https://www.rfc-editor.org/rfc/rfc792.html)：IPv4 ICMP 错误及引用原始数据格式。
- [RFC 4443](https://www.rfc-editor.org/rfc/rfc4443.html)：ICMPv6 类型、错误及校验和。
- [RFC 8200](https://www.rfc-editor.org/rfc/rfc8200.html)：IPv6 首部、Hop Limit 与扩展首部。
- [RFC 9293](https://www.rfc-editor.org/rfc/rfc9293.html)：TCP ACK、校验和及 RST 生成。
- [Go ipv4](https://pkg.go.dev/golang.org/x/net/ipv4)、[Go ipv6](https://pkg.go.dev/golang.org/x/net/ipv6)、[Go ICMP](https://pkg.go.dev/golang.org/x/net/icmp)：TTL/HopLimit 与报文接口。另核对实际依赖 x/net v0.60.0 和本机 Go net 源码；没有升级依赖。

## 实际验证

模拟 packet socket 与实际报文编解码覆盖 IPv4/IPv6、ICMP/TCP、最小引用包、IP options/扩展首部、端点/序号/ACK/校验和/nonce 拒绝、多路径、到达/端口拒绝/超时/错误、DNS 固定、私网混合拒绝、取消与读协程回收。没有使用真实公网网络或原始套接字。

```text
go test -race ./internal/netprobe
ok github.com/JasperXzy/harbormaster/internal/netprobe 1.703s

go test ./internal/netprobe -run '^$' -fuzz '^FuzzTracePacketParsers$' -fuzztime=10s -parallel=2
fuzz: elapsed: 11s, execs: 962418, new interesting: 22 (total: 24)
PASS
ok github.com/JasperXzy/harbormaster/internal/netprobe 11.344s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
exit 0；全部包通过。部分实际输出：
internal/agentcli     1.624s
internal/agentclient  4.981s
internal/agentstate   1.822s
internal/api         96.466s
internal/cli         18.688s
internal/cloud/aliyun 4.435s
internal/collect     14.232s
internal/devmode     9.447s
internal/notify      2.909s
internal/power       21.330s
internal/probes      5.425s
internal/store       96.481s
其余 cached 或 no test files。
```

交叉构建实际执行 `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o <临时路径> ./cmd/hm-server`：linux/darwin/windows 各 amd64/arm64 全部 PASS；同样构建 Linux amd64/arm64 的 hm-agent 全部 PASS。产物在系统临时目录 `hm-trace-build-fw22cx4y`，没有替换用户当前程序。

# 内置 ICMP / TCP 测速器

2026-11-12；PRD F10.1–F10.4 / F12.6 的执行组件。`internal/netprobe.Run` 可供 server 与 agent 的固定任务执行器调用。本阶段交付网络测量与统计组件；任务管理、agent 领取/回报、数据库历史、面板 API、告警和线路追踪尚未接入，不能将这一阶段算作 M3 已完成。

## 已确认的业务口径

用户明确选择：

- 抖动 = 相邻成功探测 RTT 差值绝对值的平均数。丢失样本打断相邻关系，不跨丢包配对；无有效相邻对时为 null，不补 0。
- 只允许公网目标；域名解析出的地址也必须检查，内网、回环、链路本地和云元数据地址不探测。

每次保留 sequence、started_at、rtt_ms（失败时 null）和固定 error_code；完整任务才提供统计。均值/最小/最大只使用成功 RTT，失败比例为失败探测次数 / 已完成探测次数。TCP 这里的“丢失”是连接探测失败比例，不是对 IP 包丢失率的直接测量。全失败时失败比例 100%，RTT 三项及 jitter 均为 null。取消或本地 socket/IO 失败返回不完整原始结果且 summary=null，不把尚未执行的次数填成丢包。

## 执行边界

输入仅为目标 Host、ipv4/ipv6、icmp/tcp、TCP 端口、次数、单次超时、间隔；不接受命令、脚本、可执行文件、任意数据包或 HTTP 请求。TCP 只建立连接后关闭，不发送 HTTP/TLS/应用数据；ICMP 用固定 Echo 消息、每轮加密随机 32 字节载荷、随机标识和逐次序号。

实现采用现有 Go `net` 与 `golang.org/x/net/icmp`，不调用系统 ping/mtr，也不使用云助手。IDNA 域名规范化使用已有 x/net 的 idna 包，新增其传递依赖 x/text v0.43.0 的校验记录；没有升级已有依赖。

- 目标必须是单独的 IP 或域名，端口独立传入；拒绝 URL、userinfo、路径、IPv6 zone 等。
- 每轮一次 DNS 查询，最长 5 秒；检查全部返回地址，任何非公网答案均拒绝整轮。IPv4 DNS 映射表示先 Unmap，再校验实际地址；显式 IPv4-mapped IPv6 目标不支持。
- 同轮只使用所选地址族的首个合格答案，记录该族全部去重答案；连接直接使用固定数字地址，不发生第二次 DNS 或跨地址回退，避免混合多个节点的时延。
- 地址策略独立于 nftables 流量计数规则：普通公网单播地址可用，私有、CGNAT（含 100.100.100.200）、回环、链路本地、组播、保留、文档和基准测试地址均排除。IPv6 当前限定 2000::/3，并排除 IETF 协议分配、文档、6to4 等特殊范围；IPv4 协议专用范围也不用于测速。特殊用途范围里个别可全球转发地址仍属于本实现不支持的目标，不能把本函数当作通用互联网路由资格判定。
- 每轮 1–20 次；超时 100–5000ms；相邻尝试**完成后**等待 100–5000ms；最大理论测量时间 60 秒，包含 DNS 的整体硬上限 65 秒。地址族显式选取，IPv4/IPv6 分开形成序列。这些是实现限额，不是 PRD 对采样频率的新要求；定时任务默认 5 分钟仍待接入。
- ICMP 只接受目标 IP 的 EchoReply，匹配 code=0、ID、sequence 和完整随机载荷；忽略旧序号、其他来源、不同载荷和非 EchoReply。IPv4 校验报文 checksum，IPv6 使用系统原始套接字处理 checksum。每次读取有 deadline，任务取消会关闭 socket。
- 所有错误只输出固定代码，不透传原始 DNS/socket 错误或系统详情。

原始 ICMP socket 在 Linux 需要 CAP_NET_RAW；当前 systemd 模板只给计数所需 CAP_NET_ADMIN，因此**尚不能在当前已安装服务配置下声称可执行 ICMP**。在正式任务接入阶段应同步调整最小能力及安装包。当前未扩权、未部署、未发起公网测速或远程安装。

## 来源

- [Go ICMP](https://pkg.go.dev/golang.org/x/net/icmp)：原始 ICMP 与非特权 echo socket 的平台/API 区别，报文编解码；另核对仓库实际依赖 v0.60.0 的源码。
- [Go net](https://pkg.go.dev/net)：DialContext、数值地址连接、上下文和 deadline；实际 Go 版本的 LookupNetIP 可能返回映射格式 A 记录，已在地址校验前规范化。
- [IANA IPv4 特殊用途](https://www.iana.org/assignments/iana-ipv4-special-registry)、[IANA IPv6 特殊用途](https://www.iana.org/assignments/iana-ipv6-special-registry)：当前地址排除依据。特殊用途范围可路由与否不等于普通公网测速支持。

## 验证范围

本机真实 loopback TCP listener 验证连接建立及立即 EOF，确认没有应用负载；注入拨号边界保证实际测试只连接本机，同时断言生产层传入固定公网数字 IP。ICMP IPv4/IPv6 使用模拟 packet socket 和实际 ICMP 编解码，覆盖来源/ID/序号/载荷/类型/IPv4 checksum 拒绝、超时、取消和本地权限错误；未冒充 Linux 原始 socket 实测。

统计测试覆盖部分失败、全失败、成功 0ms、丢失样本打断抖动配对；目标测试覆盖元数据、私网混合 DNS、IDNA、非法参数、地址族区分、固定一次解析、取消不制造剩余丢包和不泄露原始错误。Linux amd64/arm64 与 Windows 编译检查只证明构建，不证明相应系统实际运行。

## 接入顺序

下一阶段将本执行器接入持久任务与私有 agent 协议：身份由既有 token 绑定，任务固定类型、有限输入、租约、结果幂等回报，采样和流量上报继续独立。后端先提供任务/目标/结果契约再接入面板；本阶段没有新增面板路由或修改前端。

## 实际检查命令

```text
go test -race ./internal/netprobe
ok github.com/JasperXzy/harbormaster/internal/netprobe 1.394s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
全部测试包 cached 或 no test files，exit 0（包含已单独运行的 netprobe）。

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec /usr/bin/true ./internal/netprobe
ok github.com/JasperXzy/harbormaster/internal/netprobe 0.003s
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -exec /usr/bin/true ./internal/netprobe
ok github.com/JasperXzy/harbormaster/internal/netprobe 0.002s
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -exec /usr/bin/true ./internal/netprobe
ok github.com/JasperXzy/harbormaster/internal/netprobe 0.002s
```

`-exec /usr/bin/true` 只替代测试程序的执行以完成目标平台编译，不是目标平台运行结果。最后补充固定错误映射（即使内部错误带包装也不输出原文）和取消前禁止发包测试后，定向 race、build、vet 和 diff 检查再次通过。

# 生产模式进程内存基线

2026-11-13；针对 PRD 第 9 节 hm-server 常驻内存 ≤100MB。此前[离线库测量](offline-ip-database.md)只覆盖读取组件，不能证明面板进程达标。本次结果为**本地已实测，按 RSS 口径未达到目标**，没有将内置国家库或构建通过等同于资源验收。

## 方法

使用提交 9ac1aa9 的真实 `hm-server`，Go 1.27.1、CGO_ENABLED=0、macOS arm64；独立进程，不使用 race/test 二进制。API 模式，默认内置 2026-11 ASN＋国家库，City 和自动下载关闭。两个独立的 0700 临时数据目录，各自在运行时生成主密钥、首次设置 Token、测试密码；仅经进程环境提供主密钥，不写入数据目录或仓库。

监听 127.0.0.1 临时空闲端口，public_url=https://memory-test.invalid，仅信任本机代理。测试请求从 loopback 设置固定 Host/Origin/Forwarded 头，以实际生产校验路径完成 `/setup`、`/session`，随后读取 `/accounts`、`/instances`、`/network/ipdb`、`/settings`。未连接真实账号、未创建测量计划或通知渠道。

实际执行形态：

```text
env HM_MASTER_KEY=<运行时随机值> HM_SETUP_TOKEN=<运行时随机值> \
 HM_DATA_DIR=<隔离目录>/data HM_LISTEN=127.0.0.1:<临时端口> \
 HM_PUBLIC_URL=https://memory-test.invalid HM_TRUSTED_PROXIES=127.0.0.1/32 \
 HM_FRONTEND=api HM_IPDB_CITY_ENABLED=false HM_IPDB_AUTO_UPDATE=false \
 <已构建的 hm-server-darwin-arm64> serve

/bin/ps -o rss=,time= -p <上述子进程 PID>
```

第二组仅额外设置 `GOMEMLIMIT=80MiB`。两组均清除其他 HM_*、GOGC 和 GOMEMLIMIT 继承值。Python 标准库脚本仅作本地 HTTP/进程/ps 测量编排，不属于产品运行依赖。RSS 每秒采样：首次设置前 5 次、设置后 1 次、密码登录后 60 次；表中稳定区间为登录后约 10–60 秒。CPU 为同一区间 ps 累计 CPU 时间的差除以实际时间，不含采样器本身。子进程峰值由 Darwin getrusage 记录，单位为字节；ps KiB 乘 1024。

## 实际结果

完整的非敏感结果见 [server-memory-results.json](server-memory-results.json)。所有值保留整数字节。

| 测量项 | 默认 Go 内存策略 | GOMEMLIMIT=80MiB |
|---|---:|---:|
| 就绪耗时 | 0.263 秒 | 0.190 秒 |
| 设置前 RSS 中位数 | 75,497,472 | 79,101,952 |
| 设置后 RSS | 142,934,016 | 146,685,952 |
| 登录后 RSS 范围 | 203,046,912–210,206,720 | 146,784,256–146,833,408 |
| 稳定区间 RSS 中位数 | 210,206,720 | 146,833,408 |
| 稳定区间 CPU | 0.523% | 0.503% |
| 子进程峰值 RSS | 210,206,720 | 146,849,792 |
| 首次 IPDB 状态读取的 Go HeapAlloc | 153,767,280 | 19,499,672 |

两次均正常退出（exit 0），日志检查未出现随机主密钥、设置 Token、测试密码或会话 Cookie。退出后确认 WAL/SHM 均消失，再以 immutable 只读方式检查隔离 SQLite：账号、任务、线路和快照均为 0；管理员 1；credential 表唯一条目为 AES-GCM 主密钥校验密文 system/master-key-verifier/v1，不是阿里云凭据。测量原始样本、摘要和无密钥日志留在临时目录 hm-production-memory-4tixc82f / hm-production-memory-3xesmlrv。

密码设置/验证仍使用现有 Argon2id m=65536 KiB、t=3、p=1。观察结果表明认证前后的内存保留不能由“ASN＋国家库约 18MB 原始数据”推断。80MiB 实验确实改变了 Go 堆分配，但没有将该场景的 OS RSS 降到 100MB，故没有将其设为产品默认或声称已经修复。

[Go 官方 GC 指南](https://go.dev/doc/gc-guide#Memory_limit)说明内存限制是运行时软目标，无法等同于进程 RSS；二进制和外部映射等不计入该目标。也核对了本机 `go doc runtime/debug.SetMemoryLimit` 与 Go 1.27.1 runtime 源码：Darwin 释放页使用 MADV_FREE_REUSABLE，Linux 的处理路径不同。本次没有测量物理 footprint 或逐类页归属，因此不把 RSS 差异直接解释为已确认的内存泄漏、有效工作集或某个确定的 OS 回收原因。

## 结论与 PRD 建议

- 当前 macOS 空库、首次设置及一次密码登录后的 60 秒 RSS 场景未达 100MB；单独的离线库组件测量不能作为整体验收证据。
- 建议在第 9 节补充验收平台、进程指标、暖机时间和负载：至少生产 Linux、默认国家库、10 台实例、周期采集及认证/备份等峰值场景分别记录；明确“常驻”与瞬时峰值的判断方法。保留 100MB 目标，不凭本次实验直接提高上限。
- 待在受控 Linux 环境验证 RSS、Go 管理内存与映射占用，再决定运行时预算、数据加载或其他实现调整。现有 main-server SSH 只读条件不能代替上传并运行临时服务的具体授权；已有内核测试请求也不是此实验的批准。尚未远程部署或安装服务。
- 本次没有 agent、10 实例负载、City、月度下载校验或长时间运行证据，其他非功能项仍按后端状态表记录。

PRD 原文未修改；本次仅提交验证记录，不变更接口或运行时策略。

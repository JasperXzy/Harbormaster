# Linux agent 客户端

2026-11-12。本批实现客户端，不代表 V7 或 M1 的真实机器验收已经完成。未安装到真实机器，未改变云资源或真实主机防火墙。

## 已实现

- `hm-agent register`：从固定阿里云 IMDSv2 地址读取 instance-id、region-id；不访问 RAM 凭据或 user-data，不跟随重定向，不使用代理。使用一次性注册码前先将 code+nonce 加密持久化，注册响应丢失可直接重试。当前不回退 IMDSv1；sha-pro 的 IMDSv2 仍需实测。
- `serve`：默认每 10 秒采样，独立网络协程批量送达；暂时失败采用带抖动的退避，采样不等待网络。HTTPS 必须使用受信任证书，不提供跳过校验选项，不接受服务器下发任意命令。
- `run-once`：采集一个真实样本并发送一批缓存；`--dev` 仍是独立的回环 HTTP 合成数据模式。
- 明确配置公网边界网卡 `--interfaces eth0`，可多个逗号分隔；不自动猜测隧道、网桥或多默认路由中的公网边界。
- 纯 Go nftables API，只建立自己的 `inet harbormaster` 表。入向 prerouting/-300，匹配接口与公网源；出向 postrouting/300，匹配接口与公网目的。分别统计 IPv4/IPv6，涵盖本机和转发。没有 DROP、NAT、重定向或全局 flush。
- 专用表已存在时，核对两个链的 hook/priority/accept policy、全部规则表达式、四个命名计数器和所有权标记。发现外部修改或同名非本工具表时拒绝继续，不覆盖。内核 generation 标记随表保留，进程重启继承；表重建、系统启动、单调时钟或任一地址族计数倒退切换 report epoch。
- `remove-counters` 是固定的本地回滚动作：仅删除与配置和规则完全吻合的专用表。须先停止服务，不改其他表。

## 本地安全与持久化

状态目录默认 `/var/lib/harbormaster-agent`，权限必须 0700。密钥按现有 `HM_MASTER_KEY` / `HM_MASTER_KEY_FILE` 规则加载，文件必须在状态目录之外；部署时将使用 `/etc/harbormaster/agent.key` 与 systemd credential，安装脚本尚未交付。代码不输出长期 token、注册码、元数据 token 或请求正文。

`state.enc` 为 AES-256-GCM 加密追加日志，包含身份、最后序列及未 ACK 样本；每次追加后 fsync。帧带长度且整体认证，启动只裁去未写完的末帧，完整帧认证失败则停止。目录锁阻止两个 agent 进程并发写入。每 1000 次操作原子压缩，先同步新文件、再 rename 和同步目录；ACK 只有 epoch+sequence 完全匹配才清理对应前缀，序号账本不随队列清空。

默认最多 8640 个样本，即每 10 秒采样时约 24 小时。满后丢弃最旧样本，后续样本带 dropped_samples，累计丢失数保留；停止数天后恢复时，超过服务器 7 天接收窗口的旧样本也显式记录缺口（保留 5 分钟传输余量）。这不能恢复跨小时/跨月缺失的分布，不用于伪造精确计费。新鲜样本继续累计，不无限增长缓存。

## 当前手动调用形式

以下为调用形态，尚未在真实主机执行。密钥和注册码通过仓库外安全来源提供，不应把真实值写入这些命令或提交。

```sh
HM_MASTER_KEY_FILE=/etc/harbormaster/agent.key hm-agent register \
  --server https://harbor.jasperxzy.com --registration-code-stdin
# 通过 stdin 提供面板生成的一次性注册码；不在参数中传长期 token。
# 响应丢失时不再次输入新代码，执行 hm-agent register 重试持久化请求。
HM_MASTER_KEY_FILE=/etc/harbormaster/agent.key hm-agent serve --interfaces eth0
# 停服务后，回滚计数表：
HM_MASTER_KEY_FILE=/etc/harbormaster/agent.key hm-agent remove-counters --interfaces eth0
```

公网接口是显式本地配置。采样周期暂固定为 10 秒，后续配置化要同时保持离线判定、缓冲容量和服务端协议一致。

## 验证与未验证边界

已在 macOS 运行 Go 单元/race 测试：私网边界、接口校验、加密状态不含明文、独占锁、末帧写入中断恢复、认证失败拒绝、压缩后继续追加、ACK 不匹配不清队列、溢出及过期缺口、注册响应丢失重试、同 generation 序列连续、单地址族重置、固定 IMDSv2 路径。

实际命令：`go build ./...`、`go test -race ./...`、`go vet ./...`；另以 `GOOS=linux GOARCH=amd64/arm64 CGO_ENABLED=0 go build -trimpath -o /tmp/hm-agent-linux-<arch> ./cmd/hm-agent` 交叉构建，两架构均 exit 0。详细最终输出见同批提交的验证记录。

本机无 Linux 内核环境，尚未执行原生 nftables netlink 规则验证、NAT/转发流量测试、GRO/GSO/flow-offload 偏差测试和 24 小时 CPU/内存/计费对账。仅交叉编译不能证明规则在目标内核可用。后续先准备安装产物与回滚，再取得真实主机操作许可。现阶段未启用 CDT 实时保护。

# agent 发布与安装

2026-11-12。已完成发布目录、安装命令 API、校验下载、systemd 单元和安装回滚脚本。本轮只在本机临时目录中执行模拟系统命令的安装测试；没有在真实机器安装服务或修改防火墙。

## 发布与接口

`sh deploy/build-agent.sh` 生成 Linux amd64/arm64 静态二进制、install.sh、hm-agent.service 和 SHA256SUMS，输出在已忽略的 `deploy/bin/agent-releases/`。服务端通过 JSON `agent_release_dir` 或 `HM_AGENT_RELEASE_DIR` 配置独立只读发布目录，示例为 `/opt/harbormaster/agent-releases`。

`POST /api/v1/instances/{id}/agent/install-command` 先验证全部文件和 SHA256 清单，再创建 10 分钟的一次性注册码。未配置/文件缺失/校验失败返回 503 agent_release_unavailable，不签发或消耗注册码；已确认消失的实例返回 409 agent_not_supported，停用账号返回 409 account_disabled。签发新命令使旧注册码失效，但成功注册前不撤销正在工作的 token。

下载位于 `/agent/v1/releases/{filename}`，只允许固定的五个文件，生产必须 HTTPS、匹配配置主机名，不接受目录索引、任意文件名或越过根目录的符号链接。Go 后端不依赖前端构建；代理示例已把 `/agent/*` 转给后端。

生成的一行命令固定安装脚本和发布清单的 SHA256，下载后验证，再经 stdin 向安装脚本传注册码，避免落在 sudo 参数日志和子进程环境变量中。**命令文本本身含一次性秘密**，前端不可写入调试日志或分析事件。发布过程中更换文件会使旧命令验证失败，应重新生成命令。

## 目标主机变更

脚本需 Linux、systemd >=247、curl、sha256sum、openssl、iproute2 和系统用户管理命令；不自动安装系统包。仅在唯一默认公网接口可明确确定时自动采用，否则退出要求本地配置。

安装以下内容：

- 用户/组 hm-agent（系统用户、无登录 shell）。
- `/usr/local/lib/harbormaster/hm-agent`。
- `/etc/systemd/system/hm-agent.service`。
- `/etc/harbormaster/agent.env`（接口列表及上报周期）、`agent-managed` 安装标识。
- `/etc/harbormaster/agent.key`（新安装随机生成，0600，已有密钥不覆盖）。
- `/var/lib/harbormaster-agent`（0700，加密状态与队列）。

注册阶段在安装进程中短暂以 root 读取主密钥并完成绑定，不添加计数规则。常驻服务用 hm-agent 用户，只有 CAP_NET_ADMIN，无 CAP_NET_RAW、无完整 root；通过 systemd LoadCredential 获取密钥的只读副本，密钥不进入状态目录或 unit 文本。此配置依据 [systemd 执行环境文档](https://freedesktop.org/software/systemd/man/latest/systemd.exec.html) 的 credential、状态目录与能力配置。

启动服务后会创建专用 `inet harbormaster` 计数表，只操作该表。未知同名安装文件/用户不自动接管；升级保留现有身份与状态。若尚有未送达缓存，主动重新注册会失败并回滚，避免把旧实例缓存迁给新绑定；先恢复旧服务送达缓存后再升级。

## 本地上报周期

默认 10 秒；`hm-agent serve --report-interval 10s` 可改为 1–60 秒的整数秒。systemd 安装使用 `/etc/harbormaster/agent.env` 的 `HM_AGENT_REPORT_INTERVAL=10s`；修改后需重启服务。安装脚本仅接受 `1s`–`60s` 形式，升级保留该值，旧 env 缺失时补默认值。unit 自带 10s 缺省值，因此更新 unit 不要求先改旧 env。

长周期需要先通过面板告警规则调整 `agent_offline` 的 `offline_after_seconds`，使其大于采样周期并留出网络余量。例如 60s 采样可配 180s 掉线阈值。后端不会自动覆盖用户阈值；30 秒掉线验收仍只针对默认 10s 周期。修改周期前先升级服务端，旧服务端拒绝未识别的非默认周期字段。详情见 [采样周期设计](agent-cadence.md)。

## 失败恢复与卸载

下载/校验在停旧服务前完成。替换前备份 binary/unit/env 到临时目录。失败时停止新服务、尝试恢复原文件与启用状态，原服务之前运行则重新启动。初装失败只尝试删除规则完全匹配的专用计数表，绝不 flush 现有防火墙。

加密状态和主密钥始终保留，因为远端注册码可能已经消费，回滚它们可能丢失新的 token。脚本结束会删除临时下载/备份目录。若文件系统或 systemd 本身导致恢复失败，脚本只报告已尝试恢复，不宣称回滚成功。

明确停用时先在面板撤销 token，再执行以下固定操作；这里是操作说明，本轮未执行：

```sh
sudo systemctl disable --now hm-agent.service
sudo env HM_MASTER_KEY_FILE=/etc/harbormaster/agent.key \
  /usr/local/lib/harbormaster/hm-agent remove-counters --interfaces eth0
```

接口须与安装配置一致。计数表缺失或被修改时回滚命令会拒绝，不应改成全局 nft flush。随后可移除上述 binary/unit/env/安装标识并 daemon-reload；数据、密钥、系统用户默认保留，是否清除由用户单独决定。

## 验证结果

```text
go build ./...                       exit 0
go test -race ./...                  exit 0
  internal/agentrelease              2.426s
  internal/api                       24.043s
  internal/cli                       2.543s
  internal/config                    1.308s
  internal/store                     4.951s
  其他有测试的包                      cached / passed
go vet ./...                         exit 0
sh deploy/build-agent.sh             exit 0
sh -n deploy/agent/install.sh         exit 0
npm --prefix web run lint:api         Your API description is valid.
```

测试包含：实际安装 shell 流程在临时文件树内首次安装、注册失败升级恢复旧 binary、保留新凭据状态、重启原服务；校验文件损坏/目录逃逸；缺少发布文件不签发注册码；管理员 CSRF；重复签发轮换；发布下载 HTTPS 边界；已释放实例拒绝。测试中曾发现本机 sha256sum 不接受 GNU 长选项及省略 stdin 文件名，已改为通用 `sha256sum -c -` 并通过。

这些测试不能代替目标主机 systemd 与 Linux nftables 内核验收。V7 的 24 小时计数偏差、资源开销、NAT/转发/offload 影响仍待实测。

## main-server 只读预检查

2026-11-12 通过已有 SSH 别名检查：Linux x86_64、systemd 259、Docker 运行；主机名下的 caddy/nginx unit 均 inactive，但 80/443 已由其他方式监听，因此不能直接套用一个占用相同端口的新代理。公网域名 harbor.jasperxzy.com 在主机 getent 查询中未返回地址，仍需核对 DNS。非交互 sudo 可用，根盘可用约 50G。没有读取任何服务环境变量、凭据或私钥，也没有执行部署写入。后续应先识别已有代理再制定部署变更。

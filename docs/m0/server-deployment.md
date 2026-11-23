# hm-server 独立部署准备

2026-11-17 最新结果：用户已授权实际部署并接管 CDT-Monitor 入口，现已完成前后端分离的只读试运行；使用现有 cdt.jasperxzy.com 域名和证书，旧应用加密归档后退役。实际发布、运行检查、初始化方式及回滚依据见[部署记录](main-server-rollout.md)。下文是 2026-11-12 的历史准备方案，其中“保留旧服务”“尚待授权”“未部署”等描述不代表当前状态。

2026-11-12。配置、构建和检查脚本已准备；尚未在 main-server 创建服务、构建/启动新容器、修改代理、配置 DNS 或导入真实凭据。

## 已核对的目标环境

main-server 为 Linux x86_64，Docker 已运行。80/443 由 `1panel/openresty:1.31.8.1-2-1-noble` 容器占用，该容器采用 host 网络。现有 CDT-Monitor 仍运行，保持并行。

容器 `/www` 对应主机 `/opt/1panel/www`；主机 `/opt/1panel/www/conf.d` 挂载到 OpenResty 的 conf.d，根配置确实 include 此目录的 `*.conf`。只读检查未找到 harbor.jasperxzy.com 的现有虚拟主机，也未发现本计划的 Harbormaster 配置/数据/发布目录。

从 main-server 经 Google 公共 DNS 查询该域名 A/AAAA，两者均返回 Status=3（NXDOMAIN）。本机代理 DNS 返回的 198.18.* 地址属于本地代理结果，不能拿来配置公共 DNS。启用前需重新查询；此前查询结果不等于届时的状态。

## 前后端边界

- Go 后端只监听 `127.0.0.1:8080`，`frontend=api`，独立构建和启动。
- 前端由 Claude 构建，将审核过的静态产物放入主机 `/opt/1panel/www/harbormaster-web`。后端 Docker 构建不复制 web/，不需要 Node。
- 现有 OpenResty 新增本域名的独立配置：`/api`、`/agent`、健康检查转发给 Go，其他页面从静态目录提供，应用路由回退 index.html；缺失 assets 文件返回 404。
- API 使用同源 HTTPS。代理覆盖 X-Forwarded-For、设置 X-Forwarded-Proto；后端只信任回环代理，保持现有会话、Origin/CSRF 与 agent token 鉴权。
- SSE 关闭代理缓存/缓冲，读超时 90 秒；后端 15 秒心跳保持连接。agent 下载同样走后端路径。

## 交付文件

| 文件 | 用途 |
|---|---|
| deploy/Dockerfile | Go 多阶段构建，scratch 运行镜像，默认 UID/GID 65532，包含 CA 根证书 |
| deploy/Dockerfile.dockerignore | 仅传入 Go 源码和依赖清单，不把 web、Git、部署数据/凭据带入构建上下文 |
| deploy/compose.yml | Linux 主机网络、回环监听、非 root、只读根文件系统、默认丢弃能力并仅请求 ICMP 所需 NET_RAW、独立数据/密钥/发布目录挂载、健康检查与日志轮转 |
| deploy/server/config.json | 主机和容器共用的后端配置，不含任何秘密 |
| deploy/server/hm-server.service | 原生二进制的替代启动方式，独立 hm-server 用户与 systemd credentials；与 Compose 二选一 |
| deploy/openresty/harbormaster.conf | 现有 OpenResty 的独立虚拟主机草稿，TLS 文件就绪后才能启用 |
| deploy/check-container.sh | Linux Docker 环境中的容器启动检查，只使用临时数据和临时密钥，结束后删除检查容器与临时目录 |

Docker 构建采用与 go.mod 一致的官方 `golang:1.27.1`，已从 Docker Hub 官方标签接口确认 Linux amd64/arm64 均存在。[Dockerfile 独立忽略文件](https://docs.docker.com/build/concepts/context/)用于限制构建上下文。文件绑定挂载沿用主机权限，不能假设 Compose 会自动改 UID；这也是 [Compose 文件 secrets](https://docs.docker.com/compose/how-tos/use-secrets/) 的权限限制，所以本配置使用显式只读绑定与预先设置的文件权限。

## 拟部署路径和权限

以下均未创建：

| 主机路径 | 内容与权限 |
|---|---|
| /etc/harbormaster/server.json | 非秘密配置，0644 |
| /etc/harbormaster/server-master.key | 独立生成的 32-byte base64 主密钥，0600，不放数据目录 |
| /etc/harbormaster/server-setup.token | 独立生成的初始化 token，0600，不放数据目录 |
| /var/lib/harbormaster-server | SQLite/WAL 数据，0700，仅运行用户可写 |
| /opt/harbormaster/agent-releases | 已校验的 agent 发布文件，只读挂载 |
| /opt/1panel/www/harbormaster-web | 经前端审核的静态文件；由前端单独交付 |
| /opt/1panel/www/harbormaster-tls | 域名证书和私钥的示例位置，实际可使用已有证书管理路径并调整 vhost |
| /opt/1panel/www/harbormaster-acme | HTTP 验证目录，仅在选用相应证书签发方式时需要 |
| /opt/1panel/www/conf.d/harbormaster.conf | 仅该域名的新配置，不替换全局配置或其他站点 |

Compose 默认运行 UID/GID 65532，可用 HM_RUN_UID/HM_RUN_GID 调整。数据目录及两个秘密文件的属主必须与运行 UID 一致，秘密文件保持 0600；父级配置目录由管理员控制。原生 systemd 方式则让管理员持有源秘密文件，由 LoadCredential 提供服务用户可读的只读副本。两种运行方式都不传入明文秘密环境变量，不把密钥打进镜像。

初始化完成后 token 在数据库内已消费；保留私密源文件不会再次开放向导。若删除源文件，同时移除对应绑定/LoadCredential 配置，避免后续启动因文件缺失失败。首次部署使用空数据库，真实账号只能在受保护的 HTTPS 流程中导入；不会把本机验证库或主密钥自动复制到主机。

## 执行顺序（尚待授权）

1. 确认 DNS 指向 main-server、证书来源和前端发布版本。准备上述新目录、私密源文件及后端配置，不覆盖任何未知已有文件。
2. 构建并交付后端。Docker 方式：`docker build -f deploy/Dockerfile -t harbormaster-server:local .`，在批准的 Linux 环境执行 `sh deploy/check-container.sh harbormaster-server:local`。原生方式使用已交叉编译的 `deploy/bin/hm-server-linux-amd64`。
3. 只选择 Compose 或 systemd 中的一种启动方式，对同一数据目录只运行一个服务实例。先确认 /healthz、/readyz 和初始数据库可用。
4. 准备前端与证书后，再安装独立 vhost，执行 `openresty -t`；检查通过后才 reload 现有代理。该方式不会自动创建 1Panel 的网站管理记录，不能把手工配置视为已由 1Panel 托管。
5. 用 HTTPS 验证首次向导、登录、账号管理、SSE 和 agent 下载，再在批准范围内导入账号。初始化 token 由用户在私密终端读取并填写，不发送到对话或日志。
6. 初次部署回滚：停止新后端，撤下新增 vhost并在语法检查后 reload；保留数据目录与外部主密钥以免丢失凭据。不会停止/改动 CDT-Monitor 或其他站点。后续数据库升级需要独立的兼容性与备份检查，不以简单更换旧镜像代替数据回滚。

## 实际检查与边界

```text
ssh main-server 'sudo docker compose --env-file /dev/null -f - config --quiet --no-path-resolution' < deploy/compose.yml
exit 0，未创建容器、未读取部署秘密文件

go run github.com/rhysd/actionlint/cmd/actionlint@latest -shellcheck= .github/workflows/go.yml
实际解析工具 v1.7.12，exit 0；工具依赖只在 Go 缓存，不加入项目依赖

go run ./cmd/hm-server migrate --dev --config deploy/server/config.json --data-dir <临时目录>
{"level":"INFO","msg":"migrations_applied",...}
exit 0，临时示例库已清理；证明配置可加载，不代表生产部署

go build ./...
exit 0

sh -n deploy/check-container.sh
exit 0

git diff --check
exit 0
```

本机没有 Docker 引擎、OpenResty 或 systemd-analyze，未执行镜像构建/容器启动检查，也未对含待配置证书路径的 vhost 做真实 OpenResty 语法检查。CI 已增加容器构建及启动检查作业（push=false），但本分支未 push，不能声称 CI 作业已运行。完整 Go race 测试在前一提交已通过；本批不修改 Go 行为。


### 手动电源操作开关

仓库中的两个服务配置显式设 `instance_power_enabled: false`。只有运维设置 `instance_power_enabled=true` 或 `HM_INSTANCE_POWER_ENABLED=true` 后启动 serve，服务才接受并执行管理员电源任务；这不会附加 RAM 权限。开发模式拒绝开启，run-once 保持只读采集。启动日志 cloud_writes 与 /api/v1/system/info 的 instance_power_enabled 反映启用状态。

本轮仅完成代码、配置模板与模拟测试，没有改变真实机器配置，也没有授权或执行真实云写操作。详见[电源接口与确认](instance-power.md)。

2026-11-12 本机测速接入后，systemd/Compose 源模板增加 NET_RAW，未增加 NET_ADMIN；仅表示请求能力，非 root 容器的有效能力与真实原始 socket 仍待部署验收，见[本机测速](server-probe-tasks.md)。未修改正在运行的任何服务。

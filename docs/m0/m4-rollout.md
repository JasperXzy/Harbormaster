# M3–M4 完成与 main-server 生产上线记录 (`v0.4.0`)

核对日期：2026-11-21。按用户明确确认的设计与安全门禁边界，完成 Harbormaster 剩余全部 **M3（网络延迟与线路监测）** 与 **M4（扩展能力与多渠道通知）** 前后端功能，通过全量竞态测试与构建校验，并滚动发布至 `main-server`（`https://cdt.jasperxzy.com`）。

## 1. 用户确认的设计与线上安全边界

1. **`main-server` 高危/计费云写开关保持默认关闭**：
   - 保持 `HM_INSTANCE_POWER_ENABLED=true`（允许实例开关机、额度保护停机与主备切换拉起备机）；
   - 严格保持 `HM_EIP_MANAGE_ENABLED=false`（`F7.2` 更换弹性公网 IP）、`HM_INSTANCE_CREATE_ENABLED=false`（`F7.3` 创建 ECS）与 `HM_INSTANCE_RELEASE_ENABLED=false`（`F7.4` 释放按量 ECS）默认关闭；
   - `GET /api/v1/system/info` 完整返回 4 项布尔能力标记，前端弹窗在开关关闭时展示清晰的配置提示而不发起写操作。
2. **M3 / M4 细节设计（全部采用推荐方案）**：
   - `F11.2` 突发性能 CPU 积分告警：`TotalCredit / (BaselineCredit * 60 * 24 / 100) < threshold_percent`（默认 `20%`），缺失或过期样本跳过判断。
   - `F11.3` 阿里云系统事件通知：ECS 计划维护仅从 `ecs_history`（`Scheduled/Executing/Inquiring`）触发且遵守静默时段；抢占式回收预警仅从 `cms_spot`（最近 5 分钟内发布）触发且作为紧急通知绕过静默时段；两套原始记录均完整保留。
   - `F7.2` 弹性公网 IP 安全保护：引入 `managed_eip` 归属账本，**仅允许解绑和释放由 Harbormaster 自身创建的受管 EIP**；遇到用户原有的存量 EIP 或固定实例公网 IP 时，API 与 Telegram `/changeip` 均返回 `409 unmanaged_eip_protected` / `eip_rotate_unsupported` 拒绝破坏性操作。
   - `F9.4` 闲置计费资源：不在缺乏可靠官方实时报价时捏造月度金额（`monthly_cost: null`）。
   - `F16.8` 版本中心：不向只读容器挂载宿主机 `docker.sock`，提供在线 GitHub Release 最新版本检查与一键复制的 Docker Compose / Systemd / `hm-agent` 升级命令。
   - `F16.9` 登录前自定义品牌：在保留 HTTPS / Host / CIDR 边界的前提下开放 `GET /api/v1/public/branding` 与 `GET /api/v1/public/branding/logo`，供 `/login` 与 `/setup` 展示自定义面板名称与图标。

## 2. 分阶段提交（分支 `be/m0-foundation`，未 push / 未合并 `main`）

| 阶段 | 提交 | 内容摘要 |
|---|---|---|
| Stage M4-1 | `b5e7287` | `055` 迁移：`swas_traffic_usage`、`cpu_credit_low`、`system_event` 告警评估器，`POST /integrations/cron/run`（`cron:run` API Key），公开品牌接口与版本中心 |
| Stage M4-2 | `a86dda9` | `056` 迁移：AES-256-GCM 加密 SMTP 邮件与 Webhook 通知渠道（支持 HMAC-SHA256 签名与自定义请求头）、多渠道 Outbox 并发投递与每日流量简报（`GET/PUT /notify/daily-report`、`POST /notify/daily-report/send`） |
| Stage M4-3 | `6530cb3` | `057` 迁移：跨账号主备切换（`F6.8`）、受 `managed_eip` 严格保护的更换弹性 IP（`F7.2`）与 Telegram `/changeip` 二次确认（`F8.4`）、ECS 目录与创建（`F7.3`）及备份恢复重加密补齐 |
| Stage M3+M4-4 | `f995861` | 新增 `/network` 网络页面（延迟矩阵、24h/7d RTT/抖动/丢包曲线、单向与去回程双向 Traceroute 及文本导出、目标/组合/每日线路快照、离线 IP 库状态），全面升级实例、自动化、费用、通知、设置与登录/初始化向导 |
| Deploy & Perf | `760f2fa`, `f995843` | 部署示例配置补齐 M4 安全开关；在 `056` 重建 `job` 表前为全部入向 `job_id` 外键列建立索引，使 88 MB 生产库迁移耗时由全表扫描降至 `0.53s` |

## 3. 本地全量验证记录

```text
test -z "$(gofmt -l cmd internal)"          exit 0
go vet ./...                                exit 0
go test -race ./...                         全部通过（internal/api 166.7s, internal/store 313.0s, internal/backup 51.0s, internal/power 31.8s, internal/release 22.5s, internal/probes 17.1s, internal/notify 15.3s）
npm --prefix web run lint:api               valid (48ms)
npm --prefix web run typecheck              exit 0
npm --prefix web test                       15/15 pass
npm --prefix web run build / build:mock     exit 0
```

## 4. `main-server` 在线备份、滚动发布与核验

- **切换前在线备份**：`/var/backups/harbormaster/m4-760f2fa/`（目录 `0700`，文件 `0600`），通过 SQLite Online Backup API 备份 `harbormaster.db`（`88,920,064` bytes，`PRAGMA integrity_check=ok`，外键错误 `0`，切换前迁移数 `54`），并备份 `server.json`、`compose.readonly.yml`、`runtime.env`、`index.html` 与 `agent-releases-prev`。主密钥与初始化凭证全程未读取、未复制、未输出。
- **发布组件**：
  - 后端镜像：`harbormaster-server:m4-f995843`（版本 `0.4.0`，提交 `f995843`，通过 `deploy/check-container.sh` 容器非 root、只读根文件系统、代理头与密钥零泄漏自检）
  - 内置 Agent 发布包：`/opt/harbormaster/agent-releases` 更新至 `v0.4.0`（`linux/amd64` + `linux/arm64`，`SHA256SUMS` 校验全部 `OK`）
  - 前端静态产物：`/opt/1panel/www/harbormaster-web/` 原子发布 `index-BF6KsEKw.css`、`index-BaTiOgaW.js` 与 `index.html`（SHA-256 `c8c1dc98278f3d6ffe8e8ad2538340d0bfe708490ad64153211ceb763ceb3dba`）

## 5. 回滚步骤（如需回退至 `v0.2.2`）

1. 停止 `hm-server` 容器，将 `/var/backups/harbormaster/m4-760f2fa/` 中的 `harbormaster.db`（因本次包含 `055`–`057` 迁移）、`server.json`、`compose.readonly.yml`、`runtime.env` 及 `agent-releases-prev/*` 拷回原路径（保持 `harbormaster.db` 属主 `65532:65532`、`0600`）。
2. 在 `/opt/harbormaster/releases/20261117-nzs6aj96` 执行 `sudo docker compose --env-file runtime.env -f compose.yml -f compose.readonly.yml up -d hm-server`。
3. 将 `/var/backups/harbormaster/m4-760f2fa/index.html` 原子拷回 `/opt/1panel/www/harbormaster-web/index.html`。

# M0–M2 完成与 main-server 生产上线记录

核对日期：2026-11-21（UTC `2026-11-20T18:31Z`）。按用户明确确认的设计与部署授权，完成 M0–M2 全部剩余前后端功能、隔离网络命名空间 Linux 内核计数自检，并上线到 `main-server`（`https://cdt.jasperxzy.com`）。

## 1. 用户确认的设计与安全边界

1. **云写开关（`main-server`）**：开启实例电源控制（`instance_power_enabled: true` / `HM_INSTANCE_POWER_ENABLED=true`），严格保持 ECS 释放关闭（`instance_release_enabled: false` / `HM_INSTANCE_RELEASE_ENABLED=false`）。
2. **CDT 单位、告警与额度保护执行**：采用 `1024^3` 字节（GiB）口径默认启用 `cdt_usage`（`80/90/95/100%` 档位周期内去重）与 `cdt_forecast_exceed`（月底预测超额与恢复）；额度保护规则新增可选开关 `allow_unverified_execution`（默认 `false`），允许管理员显式授权在完整本地采集窗口估算越线时立即触发真实停机。
3. **M2 细节设计（采纳全部推荐方案）**：
   - `F2.4`：本地多行备注 `note`、单分组 `group`、多标签 `tags`，仅存 SQLite，不回写阿里云。
   - `F6.3`：新建/启用/修改后的每日定时开关机从**下一个**计划时间点（`activated_at`）起生效；`catch_up_minutes`（默认 15 分钟）仅补偿已启用规则在服务短暂重启后的漏跑。
   - `F5.1` / `F16.5`：跨整点累计样本按时间比例分摊写入 `agent_hourly_traffic`（`estimated=true`），仅用于历史图表与归档；据此安全清理超期原始秒级样本（默认 7 天）、小时桶（默认 13 个月）、终态任务/通知与审计记录。
   - `F13.3`：`GET /api/v1/integrations/summary` 由 `summary:read` API Key 鉴权并豁免 `admin_allow_cidrs`，保留未知字段为 `null` 的语义。
   - `F15.7`：审计详情记录非敏感 `before`/`after` 前后值与中文 `summary`；凭据仅记 `credentials_changed` 布尔标记。

## 2. 分阶段提交（分支 `be/m0-foundation`，未 push / 未合并 `main`）

| 阶段 | 提交 | 内容摘要 |
|---|---|---|
| Stage 1 | `2823e6c` | `PATCH /settings` 数据保留策略、实例本地备注/分组/标签（`050` 迁移）与审计 `before`/`after` 及中文摘要 |
| Stage 2 | `1420f43` | `agent_hourly_traffic` 时间分摊归档、计费对象周期归因与共享带宽每小时收发、阶梯超额单价与费用估算（`051` 迁移）及后台数据清理 |
| Stage 3 | `eb097b1` | 默认启用 `cdt_usage` 与 `cdt_forecast_exceed`（`052` 迁移）、暴露 `cdt_estimation_deviation`、额度保护 `allow_unverified_execution` |
| Stage 4 | `4e43c06` | 每日定时开关机、意外停止保活、每月 1 号自动开机、面板外关机意图确认（`053` 迁移）与优先级协调（额度保护 > 定时关机 > 保活/月初开机） |
| Stage 5 | `e7b926b` | Telegram 远程控制白名单与二次确认按钮、外部关机内联确认按钮（`054` 迁移）及 `/api/v1/integrations/summary` 小组件摘要接口 |
| Stage 6 | `4278091` | 新增 `/automation` 自动化页面，全面升级实例、流量、总览、费用、通知、设置与 Passkey 登录页及 `VITE_API_MODE=mock` 对齐 |

## 3. 验证记录

### 3.1 本地全量检查

```text
test -z "$(gofmt -l cmd internal)"          exit 0
go vet ./...                                exit 0
go test -race ./...                         全部通过（internal/api 186.9s, internal/store 411.3s, internal/power 42.1s, internal/notify 21.3s）
npm --prefix web run lint:api               valid (54ms)
npm --prefix web run typecheck              exit 0
npm --prefix web test                       13/13 pass
npm --prefix web run build / build:mock     exit 0
```

### 3.2 `main-server` 独立网络命名空间 Linux 内核计数验证（M0 `agent-kernel-test-plan.md`）

在 `main-server`（Linux `7.0.0-14-generic x86_64`）使用 `mktemp -d /tmp/hm-agent-kernel.RLJswN`（`0700`）上传测试二进制并在隔离网络命名空间执行，完成后立即删除临时文件与目录：

```text
sudo -n unshare --net -- env HM_AGENT_KERNEL_TEST=1 \
  /tmp/hm-agent-kernel.RLJswN/hm-agent-kernel-test -test.run '^TestKernelCounters$' -test.v -test.timeout 30s
=== RUN   TestKernelCounters
    native_linux_test.go:235: verified IPv4/IPv6 public ingress/egress and private exclusions; L3 counters rx4=84 tx4=84 rx6=124 tx6=124; reopen, reset and foreign-table refusal passed
--- PASS: TestKernelCounters (0.32s)
PASS
```

## 4. `main-server` 生产备份、发布与在线核验

- **切换前在线备份**：`/var/backups/harbormaster/m2-4278091/`（目录 `0700`，文件 `0600`），通过 SQLite Online Backup API 备份 `harbormaster.db`（`75,599,872` bytes，`PRAGMA integrity_check=ok`，外键错误 `0`，切换前迁移数 `49`），并保存原 `runtime.env`、`compose.readonly.yml`、`server.json` 与前端 `index.html`。外部主密钥与初始化凭证未读取、未复制、未写入日志。
- **后端镜像构建与容器自检**：
  - 镜像：`harbormaster-server:m2-4278091`（版本 `0.2.0`，提交 `4278091`）
  - 镜像 ID：`sha256:30362e90592648ddb9e436c578a0b8f54e9de8a6c8b098a4953014a71f482cf4`
  - `sudo sh deploy/check-container.sh harbormaster-server:m2-4278091` 通过（默认 `65532:65532` 非 root、代理头校验、SSE、密钥零泄漏）。
- **配置切换与启动结果**：
  - `/etc/harbormaster/server.json` 与 `/opt/harbormaster/releases/20261117-nzs6aj96/compose.readonly.yml` 同步设置 `instance_power_enabled=true`（`HM_INSTANCE_POWER_ENABLED="true"`），保持 `instance_release_enabled=false`（`HM_INSTANCE_RELEASE_ENABLED="false"`）与 `server_not_managed=true`。
  - 容器 `harbormaster-hm-server-1` 状态 `running (healthy)`，用户 `65532:65532`，只读根文件系统 `true`，启动日志：`{"msg":"server_started","listen":"127.0.0.1:8080","development":false,"frontend":"api","cloud_writes":true}`，主机内秘密扫描匹配 `0`。
  - 数据库迁移自动推进至 `(54, '054_m2_telegram_control.sql')`，`foreign_key_check=0`，`cdt_usage` 与 `cdt_forecast_exceed` 已启用，在线 `hm-agent` 切换后持续正常上报（最新样本 `2026-11-20T18:31:45Z`）。
- **前端原子发布与公网核验**：
  - 先向 `/opt/1panel/www/harbormaster-web/assets/` 写入新哈希产物 `index-BF6KsEKw.css`（SHA-256 `08344b2046b5bd269ae5a74fef13dd17aa646bfafd5284a0a054af0357a4100a`）与 `index-BnJBwajQ.js`（SHA-256 `d928431028dc054ec61794c73584c5db13afe428720ab28c4c9d89d8719df6eb`），再原子替换 `index.html`（SHA-256 `b3a3142ca25033d08afd5600f6b432705cd7403010744ae67dd854dea46f82b4`）。
  - 公网 `https://cdt.jasperxzy.com` 检查：`/healthz`、`/readyz`、`/api/v1/setup/status`、`/api/v1/session/methods`、`/`、`/automation`、`/instances`、`/traffic`、`/notify`、`/settings` 均返回 `200`；未认证访问 `/api/v1/protection/rules`、`/api/v1/automation/schedules`、`/api/v1/integrations/summary` 均按预期返回 `401`；公网静态资源 SHA-256 与构建产物完全一致。

## 5. 回滚步骤（如需回退至切换前只读版本）

1. 停止 `hm-server` 容器，将 `/var/backups/harbormaster/m2-4278091/` 中的 `harbormaster.db`（因本次包含 `050`–`054` 迁移）、`server.json`、`compose.readonly.yml`、`runtime.env` 拷回原路径（保持 `harbormaster.db` 属主 `65532:65532`、`0600`）。
2. 在 `/opt/harbormaster/releases/20261117-nzs6aj96` 执行 `sudo docker compose --env-file runtime.env -f compose.yml -f compose.readonly.yml up -d hm-server`。
3. 将 `/var/backups/harbormaster/m2-4278091/index.html` 原子拷回 `/opt/1panel/www/harbormaster-web/index.html`。

## 6. `v0.2.1`（`d2abde5`）已到期包年包月 ECS 续费查询热修复

- **现象与根因**：`JasperXzy` 账号下上海包年包月 ECS `Ali_ShangHai`（`i-uf65eqxuucx20ck92au3`）于 `2026-11-20T16:00:00Z`（北京时间 `11-21 00:00`）到期并锁定（`locked: true`）。`DescribeInstanceAutoRenewAttribute` 对已到期/锁定实例返回业务错误，被归类为 `aliyun_unreachable` 并上浮为整账号 `ecs_read` 失败，导致账号被误判为 `连接失败`。
- **修复内容（提交 `d2abde5`）**：
  - `internal/cloud/aliyun/renewal.go`：新增 `subscriptionRenewalQueryable(row, now)`，对 `locked == true` 或 `ExpiredTime <= now` 的包年包月实例直接跳过 `DescribeInstanceAutoRenewAttribute` / `QueryAvailableInstances`，保持实例 `expires_at` 展示而不再对过期实例发无效续费查询。
  - `internal/cloud/aliyun/inventory.go`：辅助续费查询仅在鉴权/权限/取消错误（`permission_denied`、`access_key_invalid`、`cancelled`）时上浮至账号级 `observe(...)`，其余单实例业务异常仅记录于实例 `renewal.last_error`，不再污染账号 `sync_status` 与 `ecs_read`。
  - `web/src/pages/Billing.tsx`：将到期时间比较由按整天取整改为精确毫秒比较（`Date.parse(i.expires_at) - now <= 0`），当天凌晨已到期的实例立即显示红色 `已过期` 标签而非 `今天到期`。
- **热更新与在线核验（`2026-11-20T19:06Z`）**：
  - 后端镜像更新为 `harbormaster-server:m2-d2abde5`（版本 `0.2.1`），通过 `deploy/check-container.sh` 自检并平滑重建 `harbormaster-hm-server-1`（`running / healthy`）；前端原子更新 `index-CHa5Hang.js` 与 `index.html`。
  - 在线重跑 `permission_check` 后确认：`JasperXzy` 恢复 `sync_status="ok"`、`consecutive_failures=0`、`last_error=null`，6 项只读权限（含 `ecs_read`、`cms_read`、`bss_read`）全部 `passed`，`Ali_ShangHai` 快照 `renewal.last_error=null` 且保留到期时间 `2026-11-20T16:00:00Z`。

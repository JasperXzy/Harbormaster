# 实时估算与额度保护只读发布

2026-11-18（Asia/Singapore）。代码 `8a467e9`，版本 `0.1.0-trial.20261118.2`，镜像 `harbormaster-server:trial-8a467e9`。在原 main-server Compose 部署上更新后端；未修改前端产物、agent 发布文件、反代、DNS 或 TLS。没有 push 或合并 main。

## 分阶段提交

- `8f19ba4 feat: add conservative billing-object metering`
- `4523dcb feat: persist qualified and provisional traffic estimates`
- `3603e8a feat: add durable quota protection and guarded rehearsals`
- `8a467e9 feat: reconcile estimates at trusted official cutoffs`

每阶段检查通过后才提交并进入下一阶段。数学、持久化、接口、守卫及调度验证见[实现与实际测试](realtime-metering.md)，前端接入见[契约交接](frontend-protection-handoff.md)。前端已有工作没有纳入这些提交。

## 实际发布与验证

只从已提交的 cmd/、internal/、Go 依赖清单和后端部署文件导出源包，没有打包工作区中 Claude 未提交的前端。Linux amd64 使用 `deploy/Dockerfile` 构建，Go 模块校验通过。首次 SSH 会话在构建输出期间断开，但镜像实际已生成；重新执行相同构建全部命中缓存并退出 0，镜像内 `version` 确认提交和版本一致。

实际命令（在构建目录执行）：

```text
sudo docker build -f deploy/Dockerfile --build-arg TARGETARCH=amd64 --build-arg VERSION=0.1.0-trial.20261118.2 --build-arg COMMIT=8a467e9 --build-arg BUILD_TIME=2026-11-17T19:34:18Z -t harbormaster-server:trial-8a467e9 .
exit 0

sudo docker run --rm --network none --read-only harbormaster-server:trial-8a467e9 version
{"build_time":"2026-11-17T19:34:18Z","commit":"8a467e9","program":"hm-server","version":"0.1.0-trial.20261118.2"}

sudo sh deploy/check-container.sh harbormaster-server:trial-8a467e9
container_setup_session_empty_accounts_sse_cloud_writes_disabled_verified
container_nonroot_ready_proxy_headers_verified_secrets_not_logged
exit 0
```

切换前用 SQLite 在线备份 API 创建一致副本，`PRAGMA integrity_check=ok`，备份 0700 目录、文件 0600：`/var/backups/harbormaster/quota-8a467e9/`。包含迁移前数据库、非秘密配置和镜像选择文件；没有复制或改动外部主密钥。切换前样本 2127、有效 agent 2、迁移 45。

用户明确确认 main-server 不在纳管实例中，因此只在 `/etc/harbormaster/server.json` 设置 `server_not_managed=true`、空 `server_instance_id`；这不是数据库内容。原 JSON 和 Compose 只读覆盖层的 `instance_power_enabled=false`、`instance_release_enabled=false` 均保留。

Compose 使用原路径 `/opt/harbormaster/releases/20261117-nzs6aj96`，原两个 compose 文件不变，只替换 `runtime.env` 中 HM_IMAGE。在配置检查通过后，单独重建 hm-server 服务。等待健康检查时第二个 SSH 会话断开；没有盲目重复重建，而是独立重新查询容器、公网和数据库，确认新版本已健康运行。

2026-11-17 20:40:26 UTC 的[运行证据](quota-protection-runtime-evidence.json)：

- 镜像为新版本，容器 healthy、UID/GID 65532、根文件系统只读。
- 本机 `/healthz`、`/readyz` 为 200；公网 `https://cdt.jasperxzy.com/readyz` 也为 200。
- JSON 两个真实写开关为 false，容器两项环境覆盖也为 false。
- 迁移 48，样本 2330，两台 agent 上报均在最近数秒，没有重新安装、轮换 token 或撤销旧注册。
- 四份估算缓存已生成；真实自动发送台账 0，生产保护规则 0。
- 当前对账均明确不可比较，原因 `official_basis_unverified`，没有输出虚构 0% 偏差。
- 一次 `docker stats --no-stream` 观察到容器内存 52.11 MiB。这只是当前少量资源的瞬时值，不代表 10 实例长期内存验收。

现有主密钥、管理员会话、账号、历史记录及 agent 身份保留。没有访问被纳管机器的 SSH，也没有开关机、创建、释放或更换 IP。只读云采集照常运行。生产 Telegram 发送器没有启用，本轮假云验证只写隔离 outbox。

## 当前数据和前端边界

生产官方来源依然没有可信截止时间，字节和计费口径仍是用户批准的暂解释；真实执行资格全部为 false。默认一小时延迟窗口是本地假设，不能当作阿里云统计延迟结论。新拓扑表从本次完整发现开始积累历史，不回填伪造的成员关系；首次窗口仍在积累。空成员计费对象目前明确标为无法归属，不因没有 agent 自动算零。有真实 agent 区间的范围已经保留已计算部分，但完整覆盖不足时总估算继续为 null。

后端能力与隔离演练已经交付，当前前端需要 Claude 接入新 estimation、规则和判定接口；未改前端意味着旧页面仍可能显示“暂无法估算”。没有自动创建生产停机规则，也没有把未核实结果显示成真实保护。前端可直接使用开发模式的合成数据联调，不需要真实账号。

若需回退，保留新数据库及当前上报；旧镜像并非已验证兼容迁移 48，不能仅换标签冒充数据库回滚。迁移 45 的一致副本及旧配置已保留，恢复需维护窗口并单独处理备份之后的新上报。本轮没有执行恢复或删除生产数据。

## 实库断网副本验证

为验证真实数据结构能进入完整的 unverified 路径，而不修改生产的一小时窗口，使用 SQLite 在线副本，在临时目录只将副本的窗口设为 60 秒。暂停副本的账号采集计划、旧未完成任务、心跳和探测计划；以新镜像、相同的只读外部主密钥挂载启动隔离容器。容器使用 `--network none`、只读根文件系统、丢弃全部能力，未映射端口，电源／释放均关闭。

实际输出：

```text
{"live_clone_check":"passed","network":"none","ready_scope_count":1,"production_settings_changed":false,"recorded_dispatches":0}
```

有真实 agent 数据的海外范围得到 ready、unverified、正的完整增量及估算总值，执行资格仍为 false。空成员范围仍保留未知。结果见[实库副本证据](quota-protection-live-clone-check.json)。整个检查没有外部网络，未向真实账号发出查询或写请求，没有改动生产的设置／规则／用量；结束后移除了容器和临时副本，未复制外部主密钥，也未将副本中的凭据或会话材料写入仓库。

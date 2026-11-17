# main-server 只读试运行部署

2026-11-18 后续修复：补齐首次部署遗漏的 agent 发布包，解决安装命令的 agent_release_unavailable；后端只读挂载内可见，公网六文件下载校验通过，无需重启或安装目标主机服务。见[发布记录](agent-release-publication.md)。

2026-11-17。用户已明确授权实际部署到 main-server、前后端分离、接管 CDT-Monitor 的入口并停用旧服务；电源和释放必须保持关闭。这取代此前并行保留 CDT-Monitor 的计划，不扩大真实云写操作或 agent 安装权限。

## 发布准备

- 使用现有 `https://cdt.jasperxzy.com`，不新建 harbor 域名。已核对公共 DNS 经 Cloudflare；main-server 的现有 Let's Encrypt 证书覆盖此域名，有效至 2027-02-09，源站与公网 TLS 校验均成功。
- 后端业务代码固定为 `103f3a9a3856dd678ea8fbe8ce5716306e27d32d`，镜像 `harbormaster-server:trial-103f3a9`；版本 `0.1.0-trial.20261117`。构建参数中的 BUILD_TIME 为本次固定发布标签时间 `2026-11-17T08:00:00Z`，不用于计算运行时间。
- 镜像 ID：`sha256:da2335aa44304cf56dd6ffdc07450ca7ac4df7821d70660b3912c2598e7e0319`。后端 Docker 构建只接收 Go 源码/依赖及占位资源，无前端、仓库元数据或凭据。
- 前端取 Claude 当前工作区的 50 个源码/配置文件，复制到仓库外的私有构建目录，独立执行生产构建；不修改或提交 Claude 的源文件。源码清单 SHA-256 为 `a811db871c6012db34bff78f160b8896390dbe2b2ed51ca7c7455d6a21a980ee`。此清单标识本次未提交前端快照，不能用后端 Git 提交号代替它。
- 正式配置为 `frontend=api`、`public_url=https://cdt.jasperxzy.com`，监听 `127.0.0.1:8080`；静态产物由 OpenResty 单独提供。Compose 叠加 `compose.readonly.yml`，显式锁定 HM_DEV=false、两个云写开关 false，关闭离线归属库自动更新。本次“只读”指云查询；管理员设置及 SQLite 正常可写。
- 使用空生产数据库。服务器独立生成的主密钥和初始化 token 只写 `/etc/harbormaster`，位于数据目录之外、0600；不复制本机验证库、AccessKey、旧应用会话或旧应用主密钥。

## 已执行的构建与检查

```text
（独立前端快照目录）npm ci && npm run build && npm test
added 60 packages; found 0 vulnerabilities
vite v8.3.8: 45 modules transformed; built in 275ms
index.html 0.80 kB; index-Dlr7PrdD.css 20.88 kB; index-Dm1wZtDu.js 346.16 kB
tests 3; pass 3; fail 0; exit 0

（main-server，精简源码上下文）docker build -f deploy/Dockerfile ... -t harbormaster-server:trial-103f3a9 .
all modules verified; image exported; exit 0
sudo sh deploy/check-container.sh harbormaster-server:trial-103f3a9
container_setup_session_empty_accounts_sse_cloud_writes_disabled_verified
container_nonroot_ready_proxy_headers_verified_secrets_not_logged
exit 0
```

容器检查只使用临时密钥、临时密码及空库，结束后容器与临时数据已清理；没有初始化正式管理员。前端的独立生产包含 index、favicon 及两个哈希资源，没有 mock/ScenePanel 分块或 sourcemap。

计划在健康检查通过后替换现有 `cdt.jasperxzy.com.conf`，保留证书路径、日志及 ACME webroot；API/agent/健康检查转发到 Go，页面走静态根目录、未知页面回退 index、缺失资源返回 404。SSE 关闭代理缓冲；API 和入口页设置不缓存。配置与依据见 [Nginx 官方说明](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering)、[Cloudflare 缓存说明](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/)。Docker 仅通过[构建参数](https://docs.docker.com/build/building/variables/)传递版本元数据，不传秘密。

旧 CDT-Monitor 仅有一个 Compose 服务、回环端口 43210，数据为 `/opt/cdt-monitor/data`；已找到旧反代 root.conf。切换前需备份配置；旧服务停下后才备份 SQLite/WAL 数据，避免复制活库。实际切换、归档与公网验证结果将在下方记录。

## 实际切换结果

只读试运行已部署并接管 `https://cdt.jasperxzy.com`。2026-11-17T08:07:53Z，OpenResty 语法检查通过后 reload；实际浏览器加载到 `/setup`，显示 Harbormaster 初始化 token、管理员密码与确认密码表单。正式管理员未被测试脚本创建，账号数为 0；数据库迁移 45 项、外键错误 0。现有 DNS/证书沿用，不需要用户调整。

部署准备单独提交为 `30102c5`；后端业务代码仍是上述 `103f3a9`。容器为 `harbormaster-hm-server-1`，非 root、只读根文件系统、自动重启 unless-stopped，健康状态 healthy。正式启动日志白名单：

```json
{"msg":"server_started","listen":"127.0.0.1:8080","development":false,"frontend":"api","cloud_writes":false}
```

正式环境再次核对配置与环境中的电源/释放开关均 false，主密钥/初始化 token/归档密钥文件均 0600。日志全文在主机内与两个正式秘密值比较，匹配 0；未将秘密内容传回工具输出、仓库或对话。

| 公网检查 | 实际结果 |
|---|---|
| `/`、`/instances`、`/settings` | 200，正确的 Harbormaster 入口及 SPA 回退 |
| `/healthz`、`/readyz` | 200，Cloudflare DYNAMIC，无缓存 |
| `/api/v1/setup/status` | 200，initialized=false，无缓存 |
| `/api/v1/session` | 401，未登录不能读取会话 |
| `/api/v1/not-a-route` | 401，API 路径没有回退 HTML |
| `/assets/not-present.js`、`/.git/config` | 404 |
| CSS、JS、favicon | 公网字节 SHA-256 与发布清单一致 |
| 源站 HTTPS 入口 | TLS 校验成功，HTML 与发布包字节一致 |

公网 HTML 被现有 Cloudflare Web Analytics 加入 beacon 脚本，原样字节校验最初因此失败；核对差异后，仅剔除此已观测脚本并归一化行首尾空白再比较，结果一致。未改变 Cloudflare 配置，也未据此放宽 JS/CSS/favicon 的字节校验。默认 Python User-Agent 的一次附加检查被 Cloudflare 返回 403，后续使用固定部署检查 User-Agent 完成上表；实际浏览器也已正常加载初始化页。

## 旧服务退役与备份

公网验证通过后，先把旧容器重启策略改为 no，再停止容器；冻结旧库后，打包整个旧项目目录、原 vhost 和 proxy root.conf。归档为 AES-256-GCM，随机 32-byte 独立密钥及 12-byte nonce。已验证完整解密、tar 可读、旧数据库/旧主密钥/Compose 文件存在且明文摘要与打包时一致。随后通过旧项目的 Compose down 移除唯一旧服务与网络，并清理旧项目目录及旧 proxy root.conf。

- 加密归档：`/var/backups/harbormaster/20261117-nzs6aj96/cdt-monitor.tar.gz.aesgcm`，1555744 bytes，0600，父目录 0700。
- 归档密钥：`/etc/harbormaster/cdt-archive.key`，标准 base64 的 32-byte 密钥，0600，仅 root 可读，与 Harbormaster 主密钥不同。备份归档时需另行妥善保留此文件。
- 归档 SHA-256：`11fa7e1c658cc03ab2f3b9a5fa780659d071a0f5bb5b837730e05a447c377bb2`。
- 原镜像仍在 Docker 本地缓存，ID `sha256:5891c01d64ba86e014d92fa94bd5a33934eee2f234897580d389b0c2c180ac4a`；未运行旧容器，不依赖可变的 latest 标签确定回滚版本。
- 原 vhost/proxy 文本另保留在同一 root 私有备份目录中。现有证书、续签 webroot 和站点日志路径继续使用，不属于旧应用数据清理范围。

归档格式为字节串 `HM-CDT-ARCHIVE` 后接一个 `0x01` 版本字节，再接 12-byte nonce、AESGCM.encrypt 返回的密文及 16-byte tag；AAD 固定为 UTF-8 `harbormaster/cdt-retirement/v1`。解密结果是 gzip tar。恢复时先在 root 私有临时目录验证和解包，以原镜像恢复旧 Compose 项目；仅在后端就绪后还原旧 vhost/proxy，执行 OpenResty -t 再 reload。不要直接把旧数据库作为 Harbormaster 数据库，也不要把解密后的旧主密钥留在公共目录。

退役后的复查：`docker ps -a` 不再返回 cdt-monitor，`/opt/cdt-monitor` 不存在，在线 conf.d/proxy 文件没有 43210 引用；OpenResty -t、Harbormaster healthy、公网 readyz/setup/status 均通过。主机其他服务未被本部署修改。脱敏运行证据见 [main-server-runtime-evidence.json](main-server-runtime-evidence.json)。

## 初始化与运维入口

用户在自己的终端执行以下命令读取初始化 token，将结果填入 HTTPS 初始化页并自行设置密码；不要把输出粘贴到对话。token 单次使用、有效期 24 小时。

```sh
ssh main-server 'sudo cat /etc/harbormaster/server-setup.token'
```

首次向导中再添加账号；未自动导入旧 CDT-Monitor 的账号、Token 或历史数据，符合 PRD 取消旧数据导入的边界。Telegram 写配置/实际发送及精确额度保护尚未接通；本次没有安装 agent，也没有执行真实云操作。

实际运行文件：`/etc/harbormaster/server.json`、`/var/lib/harbormaster-server`、`/opt/harbormaster/releases/20261117-nzs6aj96`。前端发布目录位于 `/opt/1panel/www/harbormaster-releases/20261117-nzs6aj96`，`harbormaster-web` 是同父目录内的相对符号链接，OpenResty 容器也能正确解析。启动/停止均使用两个 Compose 文件，保留只读覆盖：

```sh
cd /opt/harbormaster/releases/20261117-nzs6aj96
sudo docker compose --env-file runtime.env -f compose.yml -f compose.readonly.yml ps
```

反代是更新现有 1Panel 站点的实际 vhost 文件，未通过其 API 修改网站管理元数据；后续在 1Panel 编辑站点时需保留本次独立静态/API 路由。没有为了发布而 push 或合并 main。

PRD 修订建议：记录用户此次“提前退役 CDT-Monitor、先做只读试运行”的决定，和 M1/M2 功能验收分开。部署和旧服务退役不代表精确额度保护、通知送达或 24 小时 agent 计量已经通过验收。PRD 原文未改。

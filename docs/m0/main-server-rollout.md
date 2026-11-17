# main-server 只读试运行部署

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

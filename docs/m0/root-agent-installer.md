# root 安装命令不再依赖 sudo

2026-11-18，Asia/Singapore。用户在 root 会话执行面板安装命令时遇到 `sudo: command not found`。命令生成器固定写了 sudo，导致没有安装 sudo 的最小 Linux 系统无法从 root 启动脚本；失败发生在安装脚本执行之前。

## 修复

先按 `id -u` 选择固定解释器：UID 0 使用 sh；普通用户且存在 sudo 时使用 sudo sh；其他情况输出 `installer_requires_root_or_sudo` 并在下载之前退出。解释器参数只从这两个固定分支选择，不接受外部命令内容。

下载脚本和 SHA256 固定校验、临时文件清理及单次注册码的 stdin 传递均保留。注册码不进入 sudo 参数或环境变量。只修改服务端生成的命令，不修改已发布的安装脚本、agent 二进制、服务能力或既有注册协议；已工作的 agent 不需要升级或重装。

契约已先更新顶部记录及安装命令说明，JSON 字段不变；未修改 Claude 的前端。此次用户贴出的真实命令未被复述、复制到文件或写入测试。排查时该实例的登记记录已经消费并过期，agent 仍有新鲜上报，因此没有撤销正在使用的身份，也没有替用户执行安装。

## 验证

新增测试实际执行生成的 shell 命令，以临时 PATH 和固定命令替身模拟四种情况：root 没有 sudo、root 有 sudo 但应跳过、普通用户有 sudo、普通用户没有 sudo。验证实际选择、失败提示、参数数量和注册码 stdin 内容；下载与系统操作均为临时夹具，不执行真实安装或网络请求。

```text
go test ./internal/agentrelease ./internal/api -run 'Test(InstallCommand|CommandIsValid|Installer|ReleaseMust)' -count=1
ok github.com/JasperXzy/harbormaster/internal/agentrelease 6.668s
ok github.com/JasperXzy/harbormaster/internal/api 0.635s

go build ./...
exit 0，无输出
go vet ./...
exit 0，无输出
go test -race ./internal/agentrelease ./internal/api -run 'Test(InstallCommand|CommandIsValid|Installer|ReleaseMust|Downloads|Version)' -count=1
ok github.com/JasperXzy/harbormaster/internal/agentrelease 3.599s
ok github.com/JasperXzy/harbormaster/internal/api 3.271s
npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 105ms
Your API description is valid; exit 0
```

## Linux 验证与线上更新

修复提交 `c4cf7c4e0a4c43896e893d2efdc3941af9f9f8b8` 已构建并部署为 `harbormaster-server:trial-c4cf7c4`，版本 `0.1.0-trial.20261118.1`。镜像 ID 为 `sha256:30763dba0a59f5ee16870eca50e4e828f4b14ce572cbbe926c386f8b725cf6d7`；使用实际构建起点 `2026-11-17T17:41:04Z` 作为版本元数据。

在 Linux 构建镜像内以 network=none、cap-drop=ALL 运行新测试，root/普通用户四个 shell 分支全部通过；临时服务容器的初始化、会话、SSE、空账号与关闭云写开关检查也通过：

```text
go test ./internal/agentrelease -run '^TestInstallCommandPrivilegeSelectionAndStdin$' -count=1
ok github.com/JasperXzy/harbormaster/internal/agentrelease 0.052s
sh deploy/check-container.sh harbormaster-server:trial-c4cf7c4
container_setup_session_empty_accounts_sse_cloud_writes_disabled_verified
container_nonroot_ready_proxy_headers_verified_secrets_not_logged
exit 0
```

替换前通过 SQLite online backup 生成一致副本并执行 integrity_check，结果 ok。副本和旧 runtime.env 位于 `/var/backups/harbormaster/root-installer-c4cf7c4`，目录 0700、数据库 0600，凭据保留原加密；没有把主密钥复制进备份。

在 `2026-11-17T17:44:16Z` 开始替换后端容器。原 Compose 文件、只读覆盖、数据目录、主密钥、会话和 agent 发布目录均保留，只更新 runtime.env 的镜像引用。健康状态 healthy，版本与提交号一致，电源/释放两个环境开关均 false；没有新增迁移，仍为 45 项，也未使用回滚。回滚只需恢复该备份目录中的 runtime.env 并以同样两个 Compose 文件替换为旧镜像；本次数据库结构未变，不应为了回退二进制而覆盖正在接收新样本的数据库。

在主机内比较替换前后该实例的有效 token_hash，确认身份未变，只输出比较结果，不记录哈希或真实 token。新后端在 `2026-11-17T17:44:22.253394641Z` 收到该 agent 的新报告，说明现有安装继续工作，无需重装。本轮验证没有生成生产注册码，也没有读取或修改目标主机；用户自行完成的安装与受控 V7 计量验收分开记录。

发布文件、校验摘要、前端和云写开关均未变化。运行证据见 [root-installer-runtime-evidence.json](root-installer-runtime-evidence.json)。

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

线上应用与 Linux 容器验证结果在部署后追加；本修复不重新发布 agent 文件，也不变更云写开关。

# Agent 版本与升级提示

2026-11-12，PRD F12.8 后端实现。已有版本、最后上报时间和安装命令接口保持不变；新增相对本地可安装包的升级判断，通过实例列表和 agent.status SSE 返回。

## 发布与比较

```sh
sh deploy/build-agent.sh deploy/bin/agent-releases v1.2.3
```

版本同时通过 Go linker 写入 amd64 / arm64 的 `buildinfo.Version`，并写入发布目录的 `VERSION`。SHA256SUMS 覆盖两个二进制、unit、安装脚本和版本文件。服务端校验完整清单后才信任版本。旧的四文件清单须重新构建；缺少 VERSION 时安装命令返回 agent_release_unavailable，不签发注册码。

版本必须是最多 64 字节的完整 SemVer，允许省略 v 前缀，写入时补齐；支持预发布和 build metadata。省略构建参数时仍为 dev，不擅自分配正式发布号。比较使用 [Go 官方 semver 包](https://pkg.go.dev/golang.org/x/mod/semver)，版本优先级按数字及预发布规则处理，build metadata 不影响升级判断。直接依赖使用现有 go.sum 已校验的 golang.org/x/mod v0.42.0。

`Agent.available_version` 是服务器本地 agent_release_dir 中的版本，不代表网上最新版；`update_available` 为 true 时，前端可提示使用现有 install-command 接口升级。已安装版本与发布包版本相同或更新时为 false；任一端 dev / 不可识别、未安装或已撤销时为 null。发布目录缺失、文件不完整、校验失败或版本非法时，available_version 也为 null。

检查结果最多缓存 30 秒，实例轮询和 SSE 复用缓存，避免每个请求都读取两个二进制。安装命令始终重新完整校验，不使用此提示缓存。发布包发生变更时，提示最多滞后 30 秒；损坏的包无法因为旧提示而获取新的安装命令。

没有新增远程执行或自动升级：用户明确执行安装命令后仍走原有校验、绑定、状态保留与回滚流程。本阶段只完成后端字段和开发示例；前端提示样式与文案交给 Claude。

## 前端对接修正

2026-11-12 检查设置页调用方时，发现 `web/src/pages/Settings.tsx` 仍用 installed version 与 SystemInfo.latest_agent_version 的字符串不等判断升级。该判断会把不同的 dev 标签、较新的已安装版本以及缺少发布包的情况误判为“有新版本”。需要 Claude 将判断改为 `i.agent.update_available === true`，并在显示 available_version / latest_agent_version 时处理 null；同时从更新的 OpenAPI 重新生成类型。这里仅记录交接，不修改前端。

后端 SystemInfo.latest_agent_version 已改为同一个经过完整校验的本地 agent 版本缓存，未配置有效发布目录时为 null，不再返回 hm-server 自身 buildinfo.Version。开发模式系统信息与合成升级示例统一使用 v0.2.0。

新增回归测试先复现了旧逻辑：未配置发布目录时仍返回 latest_agent_version=dev；修复后验证无发布包返回 null、配置有效 v1.2.0 发布包后返回 v1.2.0，测试通过。

此修正后再次运行 go build ./...、go vet ./...、go test -race ./...、OpenAPI lint 和 git diff --check，均 exit 0。本次 race 输出：internal/api 26.304s、internal/cli 2.675s、internal/devmode 5.375s，其他有测试的包 cached / passed。

## 验证

```text
go build ./...                         exit 0（无输出）
go vet ./...                           exit 0（无输出）
go mod verify                          all modules verified
go test -race ./...                     exit 0
  internal/agentrelease                2.910s
  internal/api                         26.684s
  internal/cli                         2.884s
  internal/devmode                     5.711s
  internal/store                       8.329s
  其他有测试的包                       cached / passed
npm --prefix web run lint:api           Your API description is valid.
sh -n deploy/build-agent.sh             exit 0
sh deploy/build-agent.sh                exit 0（Linux amd64 / arm64，dev）
git diff --check                       exit 0
```

另外在本机执行：

```text
go run -ldflags '-X github.com/JasperXzy/harbormaster/internal/buildinfo.Version=v1.2.3-rc.1+smoke' ./cmd/hm-agent version
{"program":"hm-agent","version":"v1.2.3-rc.1+smoke"}
```

临时目录中运行带版本参数的发布脚本，确认两种架构均为 Linux、包含指定版本标签，VERSION 内容正确，五个校验和全部匹配；非法版本在创建输出目录前拒绝。临时产物已删除。这里只验证交叉编译产物，本机没有运行 Linux 二进制或安装服务。

测试覆盖：1.9 / 1.10 数字比较、同版本不同前缀、降级不提示、rc.2 / rc.10、正式版与预发布、忽略 build metadata、dev / 未知值、版本元数据篡改和缺失、缓存到期及目录切换、撤销后清空判断、SSE 字段、缓存提示不能绕过安装校验。未执行真实云写操作、远程升级或 push。

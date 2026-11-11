# 本轮实际运行验证

2026-11-11，开发机 `go version go1.27.1 darwin/arm64`。完整构建、运行和跨平台命令输出在 [validation.txt](validation.txt)。以下不是真实阿里云环境验收。

## 构建与静态检查

- `go build ./...`：exit 0，包括恢复占位 assets 后再次构建。
- `go vet ./...`：exit 0，无诊断。
- `go mod verify`：`all modules verified`，exit 0。
- `test -z "$(gofmt -l cmd internal)"`：exit 0。
- `sh deploy/build.sh`：exit 0；实际检测到 Claude 已提供 web/dist，输出 `frontend: staged dist for go:embed`，构建两个 CGO_ENABLED=0 二进制。
- `web/node_modules/.bin/redocly lint docs/api/openapi.yaml --config web/redocly.yaml`：`Your API description is valid.`，exit 0。只使用前端已有校验器，没有修改 web/。

## 测试

第一次 `go test ./...` 全通过；加入契约开发模式测试后运行 `go test -race ./...`，结果如下（原始输出）：

```text
?       github.com/JasperXzy/harbormaster/cmd/hm-agent       [no test files]
?       github.com/JasperXzy/harbormaster/cmd/hm-server      [no test files]
?       github.com/JasperXzy/harbormaster/internal/agentcli [no test files]
?       github.com/JasperXzy/harbormaster/internal/agentproto [no test files]
ok      github.com/JasperXzy/harbormaster/internal/api      1.759s
?       github.com/JasperXzy/harbormaster/internal/buildinfo [no test files]
?       github.com/JasperXzy/harbormaster/internal/cli      [no test files]
ok      github.com/JasperXzy/harbormaster/internal/config   2.067s
ok      github.com/JasperXzy/harbormaster/internal/devmode  5.739s
ok      github.com/JasperXzy/harbormaster/internal/logging  1.662s
ok      github.com/JasperXzy/harbormaster/internal/security 1.661s
ok      github.com/JasperXzy/harbormaster/internal/store    4.744s
?       github.com/JasperXzy/harbormaster/internal/ui       [no test files]
ok      github.com/JasperXzy/harbormaster/internal/ui/prepare 1.662s
```

覆盖重点：AES 随机 nonce、篡改与跨行 AAD 拒绝；主密钥路径/符号链接边界；配置优先级及禁止公网开发监听；敏感日志字段；SQLite WAL、外键、幂等迁移、生产/开发库隔离；上报鉴权、重复/冲突、回退计数、旧 epoch、token 撤销；健康与就绪区别；开发登录、CSRF、游标分页、密码重置撤销会话；前端产物复制与缺失恢复。

## 启动与联通

临时目录、随机本地端口、临时合成密码/密钥，验证完成后停止所有 smoke 服务并删除临时数据；没有将任何真实凭据写入输出。

- hm-server `version` 正确输出 JSON；`healthcheck` 返回 `ready`。
- `migrate` 输出 `migrations_applied`；再次执行不重复建表。
- `run-once` 输出 `run_once_complete`，`cloud_queries=0`、`cloud_writes=0`。
- Cookie 登录后 15 个 GET 响应，按当前 OpenAPI 检查 required/type/enum/nullable 和数值边界。这是实际响应结构检查，不冒充完整 OpenAPI 标准兼容性测试。
- 无登录访问 401；缺 CSRF 写请求 403；不支持的写操作 501；已登录 API 未命中 404；SSE 返回 `event: ping` / `data: {}`。
- hm-agent `run-once --dev --server ...` 得到 `demo_report_accepted`；SQLite 存一条样本；完全相同 payload 重试 ACK duplicate=true，仍只有一条。
- reset-password 使用 stdin 输入，返回 `password_reset_complete`，旧 Cookie 随即 401。
- 无主密钥的生产 migrate 按预期 exit 1；仅在环境里提供随机 32-byte 测试密钥后 migrate 成功。未保存这个密钥。
- hm-agent 不带 --dev 按预期 exit 1：`real_collection_not_available_in_m0`。
- 有 web/dist 的二进制返回嵌入页面，未知页面路径返回同一 index；再通过准备器的不存在 source 参数模拟无 dist（未移动/删除 web/），构建另一个二进制，确认占位页和 SPA 回退。

六种 hm-server 交叉构建与 Linux 两种 hm-agent 构建的实际结果见 validation.txt。交叉编译成功仅证明构建，不声称在对应 OS/真实 ECS 上运行过。CI 配置已写入，未 push，因此没有远程 CI 执行结果。

## 未执行

真实账号查询、真实机器 nftables、24 小时精度/性能测量、官方 CDT 水位测量、真实 Telegram 发送、真实开关机。V1–V9 的状态以各项记录为准。

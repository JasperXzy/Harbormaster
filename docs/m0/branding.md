# 面板名称与 Logo 后端

2026-11-13，对应 PRD F16.9。管理员配置、图片规范化、持久化与审计已实现；Claude 负责界面展示和编辑流程，web/ 与 docs/design/ 未修改。登录前是否展示仍待用户选择，当前接口保留管理员认证要求。

## 契约与前端接入

已先修改 OpenAPI 并添加顶部变更记录。

| 方法 / 路径 | 行为 |
|---|---|
| GET /api/v1/settings/branding | 返回 revision、name、logo、updated_at |
| PUT /api/v1/settings/branding | JSON `{revision,name}` 更新名称，保留图片 |
| PUT /api/v1/settings/branding/logo?revision=N | 原始 PNG/JPEG 请求体上传图片，保留名称 |
| DELETE /api/v1/settings/branding/logo?revision=N | 移除图片，保留名称 |
| GET /api/v1/settings/branding/logo | 读取当前规范化 PNG；无自定义图片时 404 |

初始 name=Harbormaster、revision=0、logo=null、updated_at=null。null 表示前端使用自己的默认图标；后端没有新增视觉稿或默认图标设计。恢复默认名称时明确保存 Harbormaster，空名称不表示重置。

名称先去首尾空白，保存 1–100 个 Unicode 字符，拒绝控制字符和无效 UTF-8；前端按纯文本显示。图片元数据包含同源 URL、image/png、宽/高与实际存储字节数。URL 带当前 revision，前端在变更后使用新响应的 URL；读取的是当前图片，没有历史图片下载功能。

全部接口沿用 HTTPS、配置主机名、受信代理和管理 CIDR 限制。读取需要有效会话；写入另需同源 Origin 和 CSRF。API Key/agent token 没有管理此配置的入口。图片响应为 image/png、nosniff、no-store；会话撤销后不可继续读取。登录页/首次设置页需要的匿名品牌信息没有据此自动开放，相关问题已提出，尚未收到选择。

开发模式提供默认的合成只读元数据，无自定义图片，写请求返回 501；可以在没有云账号的本地生产模式完成上传联调。默认读取不触发云请求或图片下载。

## 存储与并发

迁移 040 新增单行 panel_branding 表，保存名称、共享 revision、规范化 PNG BLOB、尺寸和最后变更时间；不使用用户文件名或文件系统路径。元数据读取仅查询图片长度和尺寸，图片内容通过独立接口读取。默认迁移不生成伪审计历史。

名称、上传和移除共用 revision。所有有效变更在同一 SQLite 事务内更新配置、递增 revision 并保存 branding_updated 审计；过期修订返回 409 branding_changed。前端应重新读取并让用户核对当前值后重试，不能用旧表单覆盖其他编辑。

相同规范化名称、相同规范化图片或再次移除空图片为 no-op：不改变 revision、updated_at，不重复记审计。请求仍须持有当前修订，旧请求不会越过并发检查。审计只保存 name/logo 字段名和新修订，来源由可信会话上下文确定，不保存名称前后值或图片内容；配置前后值政策仍等待此前问题的答案。

名称和图片是非凭据配置；真实凭据继续使用已有 AES-256-GCM 存储和外部主密钥。加密数据库备份自然包含本表；换主密钥恢复保留名称、图片和修订，原有会话/旧任务清理策略继续生效。未对真实数据目录执行迁移或恢复。

## 图片处理与实现选择

本批选择以下工程边界，PRD 没有规定这些具体数字；前端可按契约给出文件选择提示，后续改变先修改契约：

- 输入只接受 Content-Type 匹配的 PNG/JPEG 原始字节，最多 2 MiB；不接受 URL、SVG 或任意文件路径。
- 在像素解码前读取尺寸，宽高分别限制 1–512。随后重新绘制为 8 位像素并编码静态 PNG，源文件的元数据和尾随内容不会进入存储或响应。
- 存储 PNG 也最多 2 MiB。上传不做外部网络获取，不创建后台/云任务；无新增 Go 依赖。

超大请求为 413 branding_logo_too_large，不支持的媒体类型为 415；内容或尺寸无效为 422 validation_failed（fields.logo）。名称/图片校验失败、修订冲突或审计写入失败均保留原配置。原始输入不写日志或审计。

这项实现不代表前端已适配、登录前品牌展示已决定，或整体 M4/资源占用目标已经验收。

## 验证

实际专项命令与输出，均 exit 0：

```text
go test ./internal/branding ./internal/store -run 'TestLogo|TestBranding' -count=1
ok  github.com/JasperXzy/harbormaster/internal/branding  1.606s
ok  github.com/JasperXzy/harbormaster/internal/store     1.164s
go test ./internal/api ./internal/devmode ./internal/backup -run 'TestBranding|TestDevelopmentContractAndSessionBoundary|TestRestoreWithNewMasterCancelsOldWorkAndPreservesHistory' -count=1
ok  github.com/JasperXzy/harbormaster/internal/api      1.057s
ok  github.com/JasperXzy/harbormaster/internal/devmode  1.479s
ok  github.com/JasperXzy/harbormaster/internal/backup   1.126s
go build ./...  # exit 0，无输出
go vet ./...    # exit 0，无输出
```

覆盖：16 位输入转 8 位 PNG、PNG/JPEG、尺寸/长度/类型/截断内容、尾随数据移除、默认/重开库持久化、共享修订并发与刷新后重试、no-op、审计失败整体回滚、会话/CSRF/CIDR/注销、HTTP 图片响应，以及加密备份换主密钥恢复。没有真实云写入、Telegram 发送、部署或前端文件修改。

最终全量检查，均 exit 0：

```text
go test -race ./...
ok  github.com/JasperXzy/harbormaster/internal/api       118.970s
ok  github.com/JasperXzy/harbormaster/internal/backup     45.391s
ok  github.com/JasperXzy/harbormaster/internal/branding    1.599s
ok  github.com/JasperXzy/harbormaster/internal/cli        20.640s
ok  github.com/JasperXzy/harbormaster/internal/devmode    14.352s
ok  github.com/JasperXzy/harbormaster/internal/store     159.256s
# 其余有测试的包全部 ok / cached；命令包等为 no test files。
npx --yes @redocly/cli lint docs/api/openapi.yaml
Your API description is valid. # 76ms，6 条原有警告
gofmt -l cmd internal          # 无输出
git diff --check -- internal docs/m0 docs/api/openapi.yaml # 无输出
```

首次契约检查发现内联说明中的逗号被 YAML 解析为多余字段，已修正说明后重新通过；六条原有警告为 license、两个公开认证入口缺 4xx 以及三个未引用组件，本批未增加警告。代码未在全量检查后改变。

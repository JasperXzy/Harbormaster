# M0 验证与工程搭建

日期：2026-11-11。基准 PRD v0.5、WORK_SPLIT。工作分支 `be/m0-foundation`；未提交、未推送、未合并。初始搭建之后，已完成首轮真实账号只读查询和机器只读检查，未执行云资源写操作。用户已允许生成 go.sum，以及构建时将前端产物复制到 internal 后嵌入。

后续工具已补齐：`hm-server m0 import/set-regions/plan/query`，见[凭据导入与只读查询](credentials-and-queries.md)。账号文件 AES-256-GCM 加密、主密钥独立保存。最新真实环境结果见[首轮实测](live-verification-2026-11-11.md)，下表已同步。

## 验证状态

| 项 | 状态 | 关键结果 |
|---|---|---|
| [V1](V1-api.md) | 需要真实环境验证 | 主要资源、BSS、CMS、CDT 读取已通过；其余接口及写权限仍未验证 |
| [V2](V2-quota.md) | 与 PRD 不符 | 20/200GB 已确认；官方自然月重置，不能任意重置日；资格需看升级/线路 |
| [V3](V3-cdt-watermark.md) | 与 PRD 不符 | 实际 CDT 响应无 cutoff，不能直接精确对齐；延迟和补账仍待长时观察 |
| [V4](V4-dominant-traffic.md) | 与 PRD 不符 | 每小时按整个包合并入出再 max 已确认；不能只按实例创建日期判断 CDT |
| [V5](V5-enhanced95.md) | 已确认 | 5 分钟平均带宽取入出较大值，再按增强型 95 计算峰值 |
| [V6](V6-swas.md) | 与 PRD 不符 | sha-pro 元数据可读，但返回底层 ECS ID；用户已同意注册码预绑定 SWAS ID |
| [V7](V7-agent-metering.md) | 需要真实环境验证 | 未跑 sin-pro 24 小时，不声明 5% 达标 |
| [V8](V8-hkg-network.md) | 已确认 | hkg-pro 通过 EIP 接入 hk-share，按主流量 |
| [V9](V9-economical-stop.md) | 需要真实环境验证 | 文档行为已核对；停开机实验须另行授权，本轮不执行 |

每项记录均包含方法、实际结果、结论、PRD 修订建议与所需条件。PRD 原文未修改。V2/V4 的“与 PRD 不符”不表示已完成真实账号验收；表内同时说明已确认的文档部分与未验证部分。

## 本轮实现

- 两个 Go 程序、配置（JSON + HM_ 环境变量 + CLI 覆盖）、JSON 日志、SQLite WAL 与嵌入式事务迁移、AES-256-GCM 辅助和主密钥校验。
- hm-server 的 serve、run-once、migrate、version、healthcheck、reset-password；run-once 只做 M0 本地检查，明确输出云查询/云写入为零。密码重置更新已存在管理员并撤销会话，不替代正式首次设置。
- `/healthz` 活性、`/readyz` 数据库/迁移就绪检查；开发模式内置合成账号/实例/额度数据、契约只读 API、本机登录与 SSE。
- hm-agent 的 serve、run-once、version；仅 --dev 合成计数，默认每 10 秒输出或向回环 hm-server 推送，服务端实例 token 鉴权、幂等落库。
- 前端缺失也能编译；统一构建命令先复制现有 web/dist 到内部嵌入目录，缺失时恢复占位页；未知页面回退 index.html，API 未命中不回 HTML。
- CI：gofmt、go mod verify、go vet、race tests、构建及六种 server 平台交叉构建（agent Linux 两种架构）。

## 本地联调

从仓库根目录运行：

```sh
sh deploy/dev.sh api
```

前端地址 `http://127.0.0.1:5173`，独立 Go API 地址 `http://127.0.0.1:18080`。示例密码 `demo-password-only`（公开假密码，不是用户凭据）。此脚本使用 Vite 同源代理。只启动 Go 无需 Node；完整联调需要 Node 24。不需要真实阿里云账号或主密钥。独立构建、部署和可选内嵌方式见[前后端分离说明](frontend-backend-separation.md)。

另一个终端运行：

```sh
./deploy/bin/hm-agent serve --dev --server http://127.0.0.1:18080
./deploy/bin/hm-server healthcheck --listen 127.0.0.1:18080
./deploy/bin/hm-server migrate --dev --data-dir /tmp/harbormaster-separated-dev
./deploy/bin/hm-server run-once --dev --data-dir /tmp/harbormaster-separated-dev
```

开发 API 只供只读页面和会话联调，写入口暂返回 `501 not_implemented`，不会接受并保存真实 AccessKey/Telegram Token。demo 的业务数据是合成快照，agent 上报落库的验证与面板 fixture 分开，尚无真实计费估算。正式认证、初始设置、云采集、Telegram 发送、任务引擎、自动化和真实 agent 采集均未交付。

reset-password 从 stdin 读取新密码，12–1024 字节，不支持明文命令行参数；开发库已有示例管理员，生产空库返回 administrator_not_initialized。生产模式须先通过仓库外的环境/密钥文件提供 HM_MASTER_KEY，配置示例见 `deploy/config.example.json`。数据库模式互斥，不把开发库改为生产库。

## 构建注意

`go build ./...` 直接编译当前 staged assets；新签出默认是占位页。由于 Go 禁止跨包 `go:embed ../../web/dist`，**要自动读取当前 web/dist 必须用 `sh deploy/build.sh`**（用户已批准该步骤）。启动内嵌页面须传 `--frontend embedded`；默认 `--frontend api` 只提供 API。`internal/ui/assets` 是构建暂存区，不应提交复制来的前端 bundle；交付源码保留占位 index.html，发布二进制可已包含前端。前端构建存在但缺 index.html 时报告错误，避免悄悄发布坏产物。

## 设计与待答问题

- [agent 协议](agent-protocol.md)、[数据库](database.md)、[契约评审 B1–B21](contract-review.md)。契约仍是草稿；前端现已重新生成类型并处理 null 与 cutoff_unknown。
- 两账号凭据已导入并通过实测；已使用本机已有 SSH 配置完成 sin-pro/sha-pro 只读检查，无需重新提供凭据。下一步仍需确定 V7 计数安装与 24 小时观察窗口。
- 用户在 M1 前确认：是否永久单管理员；删除账号是否删除历史；“最近峰值”是否继续称为“最坏上界”；无官方 cutoff 时的对齐策略。轻量预绑定方案已获同意，尚未实现注册。
- 前端：确认上述契约修改，补齐 Telegram 文案模板；多出口实例表示与代理认证字段更新语义需在冻结前解决。
- V9 等真实写操作需要独立的具体授权；当前只读凭据/SSH 的提供不构成开关机授权。

Go 驱动、JSON 配置、标准库命令行、单写连接、AES 密文版本、内部协议字段为后端工程选择；1 小时会话与示例初始密码只用于 M0 本机 demo，不代表替用户决定生产产品规则。

实际命令与输出见 [validation.txt](validation.txt)，运行检查范围见 [运行验证](validation.md)。

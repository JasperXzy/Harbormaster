# 前后端分离与当前交付

日期：2026-11-12。用户已授权同一执行者承担前后端，模块边界保持独立。

## 边界

- `web/` 是 React + TypeScript 静态应用，只通过 OpenAPI 描述的 `/api/v1` 和 SSE 获取面板数据，不读取 SQLite、不保存云凭据、不直接调用阿里云。
- `cmd/`、`internal/` 是 Go 后端及 agent。hm-server 默认 `frontend=api`，未知页面路径返回 JSON 404。前端改版不需要修改数据库、计量算法或云适配器。
- `docs/api/openapi.yaml` 是接口依据，目前仍为草稿。本轮补充 `SystemInfo.data_mode/read_only`，明确保护状态、未知值与统计截止时间含义，类型已重新生成。
- 前端外观可由 Claude 替换。接口变更须先更新契约和变更记录，再同步 Go 响应及生成的 TypeScript 类型。

## 开发与构建

从仓库根目录：

```sh
# 前端模拟模式，无 Go 或云账号要求
sh deploy/dev.sh mock
# 独立 Go 示例 API + Vite，公开示例密码 demo-password-only
sh deploy/dev.sh api
# 后端单独构建，不运行 npm、不复制前端产物
sh deploy/build-backend.sh
# 前端单独构建
cd web
npm ci
npm run build
```

默认浏览器地址 `http://127.0.0.1:5173`，联调 API 地址 `http://127.0.0.1:18080`。可用 `HM_DEV_LISTEN` 和 `HM_DEV_DATA_DIR` 修改开发 API 的监听地址和数据目录。脚本退出会停止它启动的后端。

部署时分别交付 `web/dist/` 与 Go 二进制。`deploy/Caddyfile.separate` 提供同源反向代理示例：API、agent 和健康检查先转给 Go，其余页面回退静态 `index.html`。这样 Cookie / CSRF / SSE 无需跨域配置。此示例尚未在公网部署验证。

原定的嵌入分发仍保留：先构建 `web/dist/`，再运行 `sh deploy/build.sh`，启动时传 `--frontend embedded`。缺少 dist 时嵌入占位页；`go build ./...` 无需前端依赖即可编译。

## 已验证

实际执行的命令及结果见 [分离验证记录](separation-validation.txt)。Go API 经 Vite 登录及五个主页面读取通过；独立 mock 页面可运行；390×844 布局无页面横向溢出，无 NaN / Infinity。未知截止时间不显示伪造的剩余额度；日数据有缺口时停止累计曲线与预测。截图保存在 `docs/design/previews/`。

## 当前限制与待定事项

当前是 M0 工程与页面联调交付，不是完整生产产品。Go 示例 API 的配置/云资源写入口仍返回 501；真实账号定时采集、生产首次设置、Telegram 发送、自动化任务引擎、真实 agent 采集和 24 小时计量验证尚未交付。真实只读查询结果仍在 M0 验证记录中，没有冒充页面实时数据。

默认独立部署、同源代理和 Node 24 是本轮工程选择。PRD 未明确的生产规则仍需用户确认：是否永久单管理员、删除账号的历史保留政策、无官方 cutoff 时的估算策略及“最坏上界”的命名。已批准轻量实例预绑定注册码方案；真实 agent 安装窗口与任何云写实验须另行授权。PRD 原文未改。

本轮无真实云写入、无公开部署、无 git 提交、推送或合并。

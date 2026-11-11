# Harbormaster 面板前端

Vite + TypeScript + React，不使用界面组件库。构建产物在 `web/dist/`，默认由独立静态服务器托管；hm-server 只提供 API。可选内嵌打包仍可用，但不是前端运行的前提。

## 命令

本项目开发与 CI 统一使用 Node 24。都在 `web/` 目录下执行。

| 命令 | 作用 |
|---|---|
| `npm install` | 安装依赖 |
| `npm run dev` | 本地启动（<http://localhost:5173>），使用**模拟数据**，不需要后端 |
| `npm run dev:api` | 本地启动，把 `/api` 代理到本机的 hm-server（默认 `http://127.0.0.1:8080`，可用环境变量 `HM_SERVER_URL` 修改） |
| `npm run build` | 类型检查 + 正式构建到 `web/dist/`（不包含任何模拟数据代码） |
| `npm run build:mock` | 构建一份带模拟数据的静态站到 `web/dist-mock/`，用于演示 |
| `npm run typecheck` | 只做类型检查 |
| `npm run gen:api` | 从 `docs/api/openapi.yaml` 重新生成 `src/api/schema.gen.ts` |
| `npm run lint:api` | 校验 `docs/api/openapi.yaml` 的格式 |
| `npm test` | 检查未知用量、额度池和日数据缺口处理（Node 24，无额外测试依赖） |

**给后端 / CI 的构建命令**：`cd web && npm ci && npm run build`。

路由用 history 模式（`/instances`、`/traffic` 等），静态服务器需要 SPA 回退到 `index.html`；`/api/*` 和 `/agent/*` 必须优先转发给后端，不能回退 HTML。生产示例见 `deploy/Caddyfile.separate`。浏览器始终使用同源路径，无需存储后端地址、AccessKey 或长期凭据。

仓库根目录运行 `sh deploy/dev.sh api` 可一次启动独立 Go API 和 Vite；开发示例密码为 `demo-password-only`。`sh deploy/dev.sh mock` 则只启动前端，操作仅影响示例。两种模式都明确标出演示数据；连接当前 M0 后端时只读，尚未开放的操作不会伪装为成功。

## 模拟数据

`npm run dev` 时页面左下角有"模拟场景"按钮，可以切换：正常运行、加载很慢、空数据、接口出错、数据过期、采集程序掉线、截止时间未知、首次安装、未登录。

- 数据按契约的结构在 `src/mocks/data.ts` 里生成，全部是示例数值。
- `src/mocks/server.ts` 在浏览器里按契约应答请求，并每 10 秒模拟一次事件流推送。
- 类型来自契约：改了 `openapi.yaml` 后执行 `npm run gen:api`，模拟数据与页面里不符合新契约的地方会在类型检查时报错。

## 目录

```
src/
  api/          接口调用、事件流、任务等待、契约类型、枚举与错误码的中文
  components/   可复用组件（外壳、卡片、徽标、额度条、弹窗、状态占位、图表）
  lib/          格式化、路由、主题、取数钩子、共享数据、过期判定
  mocks/        模拟数据与模拟后端（只在模拟模式下加载）
  pages/        安装向导、登录、总览、实例、流量、通知、设置
  styles/       tokens.css（设计变量）、base.css（组件样式）
```

设计规范见 `docs/design/README.md`，接口契约见 `docs/api/openapi.yaml`。

# API Key 只读异步刷新

2026-11-13，补齐 PRD F13.2 的 API Key 刷新消费入口。生产新增只读任务提交和任务可见性，不执行云写操作。小组件摘要和 URL Cron 仍未实现，不因此将 F13 整体标为完成。

## 接口

- `POST /api/v1/integrations/instances/refresh`：单个 `Authorization: Bearer <API Key>`、`instance:control`、HTTPS/正确 Host/既有来源 CIDR 规则。必需 JSON 对象；`{"account_id":"..."}` 选择一个启用账号，`{}` 或空字符串选择当前全部启用账号。
- 返回 `202 + job_id`，只提交 SQLite 任务，由已有采集 worker 查询资源、官方流量等只读数据。电源执行开关可保持关闭。
- `GET /api/v1/integrations/jobs/{job_id}`：可查询此 Key 原有电源任务，以及此 Key 通过成功刷新请求明确加入的刷新任务。保留原 operationId，避免无必要的客户端生成名称变化。

未知显式账号返回 404，停用账号返回 409 `account_disabled`。JSON 根必须为对象，不接受 null、未知字段、任意命令、调用方指定 trigger、body token 或 null account_id；缺失/非法 JSON 为 422，Content-Type 不符为 415。账号 ID 最多 128 字节，与当前内部 ID 范围一致。

Cookie 和 URL 参数不能替代 Bearer；summary:read / cron:run 不授予刷新权限。Key 鉴权失败不回退到管理员 Cookie，Key 也不能凭刷新权限访问管理员列表。开发模式仍没有 Bearer 消费入口；其已有前端联调接口保持只读。

## 合并与权限边界

使用现有 `refresh` 任务及其采集租约，不新增云查询执行器。同一账号/相同全部账号快照的在途只读任务可以合并，包括管理员、调度器或其他 Key 已提交的刷新；任务的原始 trigger 保留。新建任务的 trigger 固定为 api。重复加入同一任务只保留第一次加入时间。

迁移 `038_api_key_refresh.sql` 记录 `(job_id,key_id,joined_at)`；一个只读任务可有多个明确请求方。Key 的有效性/scope、启用账号集合、任务创建/选择和可见性记录在同一事务完成。可见性写入失败回滚任务创建，不留下孤立任务。GET 不加入任何任务，也不因账号相同就自动授予历史访问。

全部账号请求捕获提交当时的启用账号列表。若在途 `all` 任务保存的集合不同，返回 409 `refresh_scope_changed`，不覆盖其账号列表或声称新账号已包含。调用方可在原任务结束后重试，或指定账号。已完成任务不会永久阻止下一次刷新；新请求会创建新任务。本入口不提供电源动作那样的跨完成事件键：客户端不能把同一个 Idempotency-Key 当作永久刷新身份。

原电源任务继续隔离调用主体，不能跨 Key/面板合并。只读刷新也不改变实例期望状态，不授予任意任务或权限测试任务的查询权限。

## 撤销、重启与恢复

撤销立即阻断该 Key 的后续请求和历史查询。入队事务再次校验 Key，避免鉴权与撤销之间的竞态。用户此前确认的“取消发送前电源任务、保留意图”规则继续有效。

启用账号的后台采集由账号纳管独立授权，且一个刷新任务可能服务其他请求方；因此撤销参与 Key 不取消这类共享只读工作，不赋予撤销方终止其他请求的能力。其他已加入的有效 Key 可继续查看结果，未加入者仍为 404。刷新任务不需要 Key token 执行，任务表和关系表均不保存 token。

关系及任务随 SQLite 持久化。备份恢复按已确认的通用政策取消旧未完成任务，保留失败结果及参与关系，不重放旧刷新。新迁移不推测旧刷新由哪个 Key 请求，也不改写已有电源归属或任务序号。将来实现历史清理时应先清理关系行，遵守外键约束。

## 验证

专项测试实际结果：

```text
go test ./internal/store ./internal/api -run '^TestAPIKeyRefresh|^TestIntegrationRefresh' -count=1
ok  github.com/JasperXzy/harbormaster/internal/store  1.049s
ok  github.com/JasperXzy/harbormaster/internal/api    1.024s
```

覆盖跨请求方只读合并、GET 不授权、完成后新刷新、账号集合变化冲突、显式账号/停用账号、并发去重、关系写入失败回滚、当前 scope/撤销复核、原电源归属保留、037→038 迁移、备份恢复不重放、前后权限隔离、严格请求体、无同步云查询以及期望状态不变。HTTP 测试驱动已有模拟采集 worker，验证队列最终完成。

全量检查实际通过，输出节选：

```text
go build ./...       # exit 0，无输出
go vet ./...         # exit 0，无输出
go test -race ./...  # exit 0，全部包通过
ok  github.com/JasperXzy/harbormaster/internal/api      113.064s
ok  github.com/JasperXzy/harbormaster/internal/backup    44.704s
ok  github.com/JasperXzy/harbormaster/internal/cli       19.743s
ok  github.com/JasperXzy/harbormaster/internal/collect   12.874s
ok  github.com/JasperXzy/harbormaster/internal/devmode   14.556s
ok  github.com/JasperXzy/harbormaster/internal/power     20.656s
ok  github.com/JasperXzy/harbormaster/internal/store    147.663s
npx --yes @redocly/cli lint docs/api/openapi.yaml
Your API description is valid. # exit 0，6 条原有警告
gofmt -l cmd internal          # exit 0，无输出
git diff --check -- internal docs/m0 docs/api/openapi.yaml # exit 0，无输出
```

未在真实服务创建或使用 API Key，未调用真实云接口；测试凭据与任务均为本地隔离数据。未修改 web/ 或 docs/design/。

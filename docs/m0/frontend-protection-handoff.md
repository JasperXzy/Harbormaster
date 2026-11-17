# 估算与保护的前端对接

当前后端新增接口均见 `docs/api/openapi.yaml` 顶部 2026-11-18 记录。没有修改 web/ 或设计目录，也没有替前端定稿界面／Telegram 文案。

- `QuotaStatus.estimation`：`mode=unverified` 只展示和演练，可能重复或漏计；`status=incomplete/unavailable/stale` 不展示为精确完整用量。`known_increment_bytes` 是已计算部分，可能含明确投影／上界分配；完整未知值仍为 null。原严格字段只在有资格的 aligned 结果下填充。
- `Instance.traffic_estimation` 是整个账号＋CDT 区域的共享值，不是该实例的官方计费量。online 不表示 protected。用 `reasons` 解释来源、单位、拓扑、覆盖或速率问题；`to_at` 是共同采样时刻，`calculated_at` 是计算时间，均不是官方截止。
- `GET/PUT /traffic/estimation-settings`：默认一小时是可修改的本地延迟假设。revision 防止覆盖旧配置，可设置强制公网带宽推算的实例。
- `/protection/rules`：本地 CRUD，停机仍受全局电源开关、来源资格、自身排除、豁免和频控约束。`/protection/status` 显示实际能力与自身身份。规则默认表单建议试运行并显示 60 秒提前量；所有动作字段由用户明确保存。
- `/protection/simulations`：账号／区域（轻量加实例）的过期模拟，永远强制演练，不改真实用量。界面同时提供清除，明显显示模拟标记和截止时间。
- `/protection/decisions`：显示触发值、阈值、演练／阻止原因、目标和 job_id；入队是历史事件，不表示云端已停止，实际结果继续查询现有任务接口。
- 实例的 `is_server_self` 可为 null，表示身份未知；`quota_exemption_until` 显示临时豁免。已知自身要纳入停止规则，必须显式确认并提交当前绑定实例 ID。身份未知不能绕过。
- `/traffic/reconciliations` 与 `QuotaStatus.reconciliation`：只有 `comparable` 才有偏差结果。重复截止、未知和不可比不能显示为 0%／已恢复。偏差规则已确认默认触发／恢复均为 3 次，启用状态需要保留 `recovery_samples`。

当前真实电源与释放开关继续关闭；新功能不需要改变反代、DNS、证书或 agent 安装包。开发模式有估算样例和上述列表的只读响应，生产接口可独立于前端替换使用。

现有通知页直接使用固定规则字典，不认识新 key 会报错。新客户端请先补齐 `cdt_estimation_deviation`（以及额度事件 `quota_protection`）的展示，再用 `GET /alert-rules?include_reconciliation=true` 获取新偏差规则。默认列表保持原有 12 个生产类型，避免影响当前页面。

# 网络告警、恢复与去重

2026-11-12，PRD F10.6 / F8.8–F8.10。已接入本机和 agent 的新测速结果，创建本地告警/恢复记录；没有实际发送 Telegram，没有公网测速或部署。

## 判定口径

沿用 PRD 第 10 节的触发规则，没有把三种触发条件统一改为三次：

| 规则 | 默认判定 |
|---|---|
| network_latency | 平均 RTT 严格大于用户为该组合配置的 latency_ms，连续有效采样跨度达到 600 秒；至少两次有效结果，不会只因计时器经过十分钟而触发 |
| network_loss | 完整测量的失败比例严格大于 5%；恰好 5% 不触发 |
| network_unreachable | 连续 3 次完整测量全部失败，即每次 succeeded=0 |
| 恢复 | 用户确认：连续 3 次有效健康结果。跳过、缺测、未知 RTT 和本地执行失败打断相应累计，不把已有告警标为恢复 |

延迟与失败比例都按所选方式判断。TCP 的失败比例是连接尝试失败比例；完全失败只说明该探测方式/目标端口不可达，不推断云实例已停机或已释放。三条规则独立：完全失败会满足失败比例条件，但没有 RTT，不会拿 0 当作延迟正常，也不会恢复延迟告警。

规则均为非紧急。默认失败比例和不可达适用于所有已启用组合；延迟阈值在 PRD 中没有给固定数值，必须配置，因此初始不对任何组合猜测延迟阈值。

## 契约与配置

先更新 OpenAPI，再实现字段。沿用 `GET /api/v1/alert-rules`、`PATCH /api/v1/alert-rules/{key}`；新增三条 key，生产规则总数现在为 10。params 完整替换，保留既有 session/Origin/CSRF 和字段校验。

示例（150ms 只是示例，不是系统默认）：

```json
{
  "params": {
    "probe_thresholds": [{"plan_id":"plan_example","latency_ms":150}],
    "duration_seconds":600,
    "recovery_samples":3
  }
}
```

- `network_latency`：probe_thresholds 中启用的每个条目必须给 latency_ms，duration_seconds 默认 600。
- `network_loss`：probe_thresholds 是每组合覆盖项；空列表对所有启用组合使用 5%。条目可给 loss_percent，省略仍为 5%。
- `network_unreachable`：probe_thresholds 可按组合禁用，consecutive_failures 默认 3。
- 三条规则都支持条目 `enabled:false` 和全局 enabled；recovery_samples 默认 3。每条规则最多 100 个覆盖项，不是最多监控 100 个组合。未列出的组合在失败比例/不可达规则中仍使用默认值。
- 参数只能用于对应规则；拒绝不存在的组合、重复条目、非法数值及混用其他规则字段。已退役组合的旧配置保留但不生效。

通知新增可空 `probe_plan_id`，前端可以回到对应组合/历史，不必解析占位正文。实际文案继续留给前端定稿，后端只用固定占位标题和有限数值，不输出凭据、原始网络错误或目标地址。

## 一次性处理与持久状态

迁移 031 增加三条规则、通知组合关联及每规则/组合的进度表。完整结果在保存结果和任务状态的**同一个事务**内评估；agent 重复提交在进入这个钩子前返回 duplicate，定时评估也不会重读同一条结果增加计数。

进度记录处理过的 run 序号、组合/目标修订、连续异常起点与次数、恢复次数、上次调度时间/采样时间和实际地址。规则、事件状态和通知的提交保持原子性；重复发现同一未恢复问题不重复通知，恢复后再次出现进入新 episode。

连续性按相邻有效调度/采样判断：与计划周期相比，调度或采样时间差超过 10 秒容差、样本时间倒退、实际地址变化或配置修订变化，都会重新累计。后台失去新结果超过“周期 + 75 秒”时也打断累计，已有事件保留为未恢复。迟到结果超过该新鲜度窗口仍留在历史，但不追溯触发旧告警。这些容差用于调度/执行抖动，不是对缺测时间的补测。

skipped 和本地执行失败不制造失败比例、RTT 或恢复；较早任务在更晚的跳过记录之后才回报，不能越过序号屏障继续旧累计。100% 完整失败有失败比例，但没有可用平均 RTT，因此三条规则各自处理有效性。

只修改某个组合的告警阈值时，其他组合的窗口不重置；修改阈值等待新排队的结果，不按新阈值重新解释旧运行记录。停用规则、组合或目标，以及显式更换测速点、目标对象、方式/地址族等，会静默关闭旧条件并取消未发通知，不当成健康恢复。自动跟随实例 IP 变化则重新累计，不伪造恢复。

## 静默与发送边界

网络规则 urgent=false，沿用每日静默/维护期判断。静默期间产生的告警或恢复记录为 not_sent，保留 suppression_reason；结束后不会补发旧事件。之后新发生的恢复事件按它发生时的静默状态处理。

本阶段只生成 outbox/通知历史，未接通尚待用户决定的 Telegram 发送策略，也没有实际发送消息。配置了告警不能据此宣称 Telegram 已送达。

## 验证范围

真实 SQLite 和本地测量夹具覆盖：十分钟之前不触发、严格 >5%、三次完全失败、三次恢复、未知中断恢复、缺测打断异常、同一缓存重复轮询不累计、agent 结果重复回报不累计、静默不补发、修改另一组合不干扰本组合、参数校验、监控对象更换没有假恢复，以及新 episode 更新账号上下文。

API 测试覆盖认证/CSRF、三条规则的默认参数、缺失/非法字段和无效组合。开发模式同步暴露相同规则配置和可空通知字段；没有修改前端文件。真实网络故障、连续十分钟实际测量和 Telegram 送达尚未验收。

## PRD 补充建议

在第 10 节补充用户已确认的“恢复需连续 3 次有效健康结果，缺测不等于恢复”，并明确延迟的持续时间基于连续采样证据，TCP 失败比例不同于直接测量的 IP 丢包率。原 PRD 未修改。

## 实际检查命令与输出

```text
go test ./internal/store -run 'TestProbeLatency|TestProbeLoss|TestProbeAlertQuiet|TestProbeRuleOverride'
ok github.com/JasperXzy/harbormaster/internal/store 0.540s

go test -race ./internal/store ./internal/api -run 'ProbeAlert|ProbeLatency|ProbeLoss|ProbeRuleOverride|AgentProbeDuplicate'
ok github.com/JasperXzy/harbormaster/internal/store 5.605s
ok github.com/JasperXzy/harbormaster/internal/api 3.023s

go test ./internal/store -run TestProbeNewEndpointEpisodeUpdatesAccountContext
ok github.com/JasperXzy/harbormaster/internal/store 0.632s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 95.557s
ok github.com/JasperXzy/harbormaster/internal/backup 45.223s
ok github.com/JasperXzy/harbormaster/internal/cli 16.502s
ok github.com/JasperXzy/harbormaster/internal/collect 11.777s
ok github.com/JasperXzy/harbormaster/internal/devmode 7.500s
ok github.com/JasperXzy/harbormaster/internal/probes 4.803s
ok github.com/JasperXzy/harbormaster/internal/store 95.363s
其他测试包同样通过或 cached/no test files；exit 0。

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0（六项均为既有警告）
```

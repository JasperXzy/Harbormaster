# 数据源健康告警

2026-11-12。本批接通三个有明确证据来源的生产规则、持久化状态去重、通知记录和规则 API。评估不访问云接口、不发送 Telegram、不执行停机；实际发送策略仍等待用户回答。

后续新增的 instance_expiring 规则见 [到期提醒](expiration-alerts.md)，由成功采集的到期时间驱动，不属于本文的三个数据源健康规则。

规格配置不匹配的 spec_bandwidth_insufficient 规则见 [规格带宽告警](bandwidth-alerts.md)，同样由新采集快照驱动。

## 已实现

| 规则 | 默认值 | 判断来源 |
|---|---|---|
| account_api_failure | 连续 3 次失败 | 账号已保存的采集结果与 consecutive_failures |
| cdt_official_unavailable | 连续 3 次失败 | 当前凭据版本的 CDT 观测记录，按记录倒序读取，遇到成功即停止计数 |
| agent_offline | 30 秒 | 仍纳管、账号启用、实例仍运行、token 未撤销且曾有新鲜上报的 agent，按服务端最后有效接收时间判断 |

账号失败次数按采集尝试记录，包括实际执行的重试；重复运行评估器不增加次数。新凭据、地域调整或重新启用账号会清空旧失败计数并等待新结果。无法解密/解析或缺失账号凭据也会留下明确的采集失败状态，不把它们默认为健康，不把坏凭据传给云接口。

CDT 规则不使用用量数值、单位或统计截止时间，因此不依赖尚未确认的计费口径。cdt_usage 与 cdt_forecast_exceed 暂不列入生产 GET /alert-rules；是否允许暂定字节的用量告警仍是待答问题，本批没有替用户决定。

## 去重、恢复与停止监控

迁移 007 增加 alert_rule、alert_state。每个规则/目标保存 active 与 episode；进入故障时 episode 递增，通知事件键包含该序号。评估、状态变化和通知入队在同一事务，重启后持续故障不会重复入队。

只有确认的健康证据才触发 recovery。未获取数据、换凭据后等待新结果、注册后尚无首份有效上报均不冒充恢复，也不凭空产生新的故障次数。故障尚未恢复但计数因配置变化暂不足新阈值时保留原事件，等待真实健康证据。

关闭规则、停用账号、撤销 agent、实例已停止或不再纳管，会静默关闭相应活动事件。相应尚未发送的队列记录取消；已领取的记录保留可能已经发出的不确定标记，旧 worker 回执不能再完成这些记录。历史记录保留。

沿用现有 agent 协议设计：尚未收到首次新鲜上报只标 pending_first_report，不把安装等待当作从在线转为掉线。这是当前工程选择，不代表“首次上报超时提醒”已经实现。

## 接口与运行

- GET /api/v1/alert-rules 返回上述三个规则；PATCH /alert-rules/{key} 要求管理员会话、Origin 和 CSRF，支持开关和对应参数。错误规则名返回 404，无效参数返回 422。
- consecutive_failures 范围 1–1000；offline_after_seconds 范围 10–86400，默认 30。这些是本地配置边界，不代表云侧指标时间粒度。
- 实例状态、SSE agent.status、/settings 的 agent_offline_after_seconds 与告警使用同一个持久化超时值。关闭掉线告警不会关闭状态监测。当前速率仍要求最近 30 秒的有效样本；较长离线阈值不会把旧速率伪装成当前速率。
- serve 启动独立的一秒评估循环，关闭时等待它退出；run-once 在完成只读采集后评估一次。
- SSE 的 notification.created 表示新记录，notification.updated 表示已有记录的发送/取消状态变化，按 id 更新即可。与其他更新一样每 5 秒合并，重连后重新读取列表。
- 文案暂用固定占位标题与规则/目标/计数事实，不声称正在估算或保护。Claude 草稿中的“按最高速度估算”“已替换估算值”不适用于当前未启用估算的生产状态。

## 时延与 PRD 修订建议

PRD M1 写了“停掉 agent 30 秒内收到掉线告警”，同时默认判定阈值就是 30 秒。阈值判定之后仍有调度、队列及网络传输，因此不能仅根据代码的 30 秒阈值宣布达到端到端 30 秒验收。建议把“距最后新鲜上报的判定阈值”和“检测/消息送达时延”分别测量、分别验收。

目前评估周期为 1 秒；实际调度与 Telegram 到达时延尚未实测。发送 worker 未接通，不能声称已满足 PRD 的消息送达标准。真实 agent / Telegram 验收仍需授权与配置。

## 实际检查

```text
go build ./...                       exit 0
go test -race ./...                  exit 0
  internal/api                       26.653s
  internal/cli                       3.040s
  internal/cloud/aliyun               1.683s
  internal/collect                   2.469s
  internal/devmode                   5.731s
  internal/notify                    1.801s
  internal/store                     6.767s
  其他有测试的包                      cached / passed
go vet ./...                         exit 0
npm --prefix web run lint:api         Your API description is valid.
git diff --check                     exit 0
```

覆盖阈值前不触发、重复评估不增加失败数、持续事件去重、真实恢复、重新故障、重开数据库、不同凭据版本隔离、首次 agent 报告等待、实例停止/撤销、共享掉线阈值、规则关闭取消队列、通知创建与更新事件、规则 API 鉴权和非法参数。未修改前端或设计文件。

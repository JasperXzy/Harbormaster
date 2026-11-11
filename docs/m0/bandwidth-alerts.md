# 规格带宽告警

2026-11-12，接通 PRD F11.1、第 10 节“发现时”提醒以及 F8.8 / F8.9 的恢复与去重。读取来源和比较口径见 [规格带宽检查](spec-bandwidth-check.md)。本阶段只生成通知记录，没有接入 Telegram 发送。

迁移 012 新增 spec_bandwidth_insufficient，默认开启、非紧急、warning 级别，params 为 `{}`。判定使用新采集 ECS 快照中的 network_capacity.public_outbound_exceeds_spec；比较的是公网出方向配置上限与规格能力，不表示实测吞吐已经达到上限，也不意味着共享带宽为该实例独占。SWAS 没有相同规格来源，不参与此规则。

首次确认不匹配时入队一次。同一问题持续存在、包括重复扫描和周期评估，不再重复通知；新采集结果明确为 false 时记录 recovery，之后重新出现不匹配才建立新事件。规格或拓扑未知、字段缺失、查询失败均不作为恢复依据。

告警状态、通知与资源快照共同提交，通知插入失败则回滚扫描事务。规则关闭、账号停用或资源已确认不再纳管会关闭活动事件并取消待发记录；仅停止 ECS 不改变其配置能力，因此不会凭运行状态清除该告警。通知文字仍为固定占位内容，前端及 Telegram 文案交给 Claude。

## 真实只读验收

使用已有隔离验收数据库和仓库外主密钥：

```text
go build -o deploy/bin/hm-server-readonly-check ./cmd/hm-server
HM_MASTER_KEY_FILE=<已有仓库外主密钥文件> ./deploy/bin/hm-server-readonly-check run-once --data-dir <已有隔离验收目录>
{"time":"2026-11-11T22:59:21.820Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

随后只读检查通知元数据：sg-server-nano 和 hk-server-nano 各产生一条 spec_bandwidth_insufficient 的 alert，创建时间均为 2026-11-11T22:59:19.041654000Z，delivery_status 均为 not_sent。没有发信、变更规格、修改带宽或执行实例操作。

## 自动化检查

```text
go build ./...                       exit 0（无输出）
go vet ./...                         exit 0（无输出）
go test -race ./...                  exit 0
  internal/api                       27.770s
  internal/cli                       2.858s
  internal/collect                   2.806s
  internal/devmode                   5.533s
  internal/notify                    1.572s
  internal/store                     9.855s
  其他有测试的包                     cached / passed
npm --prefix web run lint:api        Your API description is valid.
git diff --check                    exit 0
```

针对性测试覆盖：持续不匹配去重、未知与失败来源不恢复、确认恢复后再次告警、SWAS 不误报、规则/账号/资源取消、通知失败时状态与快照一起回滚，以及参数为空的规则拒绝其他规则阈值。生产 GET /alert-rules 已返回该规则，开发模式也有示例；前端目录未修改。

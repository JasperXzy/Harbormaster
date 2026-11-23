# 延迟测速结果保留与清理

2026-11-13。实现 PRD 第 9 节“测速结果默认保留 30 天，可配置”及 F16.5 的延迟测速分支：已结束的 server/agent ICMP、TCP 探测原始结果与组合运行历史可自动清理。线路追踪、agent 流量原始样本/小时聚合、云监控历史、任务元数据和审计保留政策仍是独立工作，不将本批算作完整数据生命周期或数据库 ≤1GB 已验收。

## 配置与运行

非秘密配置 `probe_history_retention_days` 默认 30，环境变量 `HM_PROBE_HISTORY_RETENTION_DAYS` 覆盖 JSON 文件；允许 1–3650 天。该上限是实现边界，未修改 PRD。配置随进程启动加载，`GET /api/v1/settings` 的 `retention.latency_probe_days` 只读展示当前值。开发模式返回明确的合成配置示例，不启动清理 worker。

生产 `serve` 启动后立即检查，再按小时检查。每次事务最多处理 500 个结束任务和 500 条历史行，5 秒上下文预算；仍有待处理批次时让出 1 秒后继续，出错 1 分钟后重试。worker 在 SQLite 关闭前随服务退出并等待结束。GET、migrate 和 run-once 不触发此清理；测试未对真实数据目录运行清理。

按 UTC 数据年龄计算截止时间，不按业务日历月份换算。任务依据 `finished_at`，跳过记录依据 `created_at`；恰好处于截止时刻的记录继续保留。减少保留期会使更早数据进入清理范围；增加保留期不能恢复已经清除的数据。后台调度和数据库繁忙可能使实际清理滞后，不将配置值当作精确的销毁时刻。

## 存储与边界

迁移 039 增加 `network_probe_job.result_digest` / `result_pruned_at`，以及按结束时间索引的 `latency_probe_retention` 待办表。结束状态触发器只登记 action=probe 的任务；旧的已结束延迟任务在升级时进入待办表。清理后移除待办项，避免反复扫描已归档任务；组合历史另有全局时间索引。

事务内完成：

1. 对到期、已结束的延迟任务，删除 `result_json` 中的原始报告；保留规范化报告的 SHA-256 摘要及清理时间。
2. 对确实曾接收原始报告的任务，将 `Job.result` 写为 `{action:"probe",result_expired:true,result_expired_at:"<UTC>"}`；保留原 status、error、时间、来源和请求键。未收到报告的旧失败任务不被伪装成曾有结果。
3. 删除到期组合运行历史，包括没有云任务的旧跳过记录。活动任务和截止边界内的结果不受影响。alert progress 的序号与连续判定状态不被重置，清理不产生恢复通知。

原始结果、待办及历史清理任一步失败会整体回滚。没有关闭外键或清除凭据，没有更新期望状态或发起资源操作。SQLite 的空闲页可供后续复用，本批不执行 VACUUM，也不承诺物理文件立即缩小。

保留期后的组合历史为空代表没有保留的结果，不是丢包为零或连接恢复。`ProbePlan.last_run` 为最近仍保留的运行，清空旧历史后可以为 null。分页是现有游标语义，后台清理可使后续页减少；读取本身不修改数据。

## ACK 与重放

agent 私有协议仍为原有固定动作和请求/响应格式。原实现以完整规范化报告比较重复上报；清理后改为以摘要比较。摘要仅在清除报告时由已有规范化字节计算，不是凭据，也不对外返回。

接受重传仍先校验当前 agent token、实例/账号启用状态、任务所属 agent、owner、token 绑定、凭据版本及报告格式/时间。相同报告返回 Duplicate ACK，不再执行任务或推进告警计数；变化的报告返回冲突。保留 request_key 与任务身份，旧请求重试不会生成第二次测量。摘要不能绕过已撤销 token 或已更换的绑定。

保留任务、请求键和去重元数据意味着总库大小还存在长期增长来源；本批没有将 ≤1GB 目标标为完成。agent 流量跨整点聚合、其 7 天/13 个月清理仍等待此前提出的口径决定。未擅自删除线路快照或套用其保留期限。

## 验证

实际专项测试：

```text
go test ./internal/store ./internal/retention ./internal/config -run 'TestProbeRetention|TestRunner|TestProbeHistoryRetention' -count=1
ok  github.com/JasperXzy/harbormaster/internal/store      0.662s
ok  github.com/JasperXzy/harbormaster/internal/retention  1.290s
ok  github.com/JasperXzy/harbormaster/internal/config     0.256s

go test ./internal/api ./internal/cli -run 'TestProbeRetention|TestServer' -count=1
ok  github.com/JasperXzy/harbormaster/internal/api        1.310s
ok  github.com/JasperXzy/harbormaster/internal/cli        1.168s [no tests to run]
```

后一个 CLI 过滤没有匹配用例，不作为 CLI 行为验收；完整检查另列。测试覆盖 38→39 升级登记、边界保留、server/agent 原始结果、重复 ACK、篡改/错误 owner 拒绝、请求键不重放、任务状态保留、活动任务与线路追踪隔离、跳过历史、告警序号、失败回滚、分批追赶、取消退出、配置优先级、HTTP 空历史/过期标记以及 GET 不清理。

全量检查实际通过，输出节选：

```text
go build ./...       # exit 0，无输出
go vet ./...         # exit 0，无输出
go test -race ./...  # exit 0，全部包通过
ok  github.com/JasperXzy/harbormaster/internal/api       117.462s
ok  github.com/JasperXzy/harbormaster/internal/backup     45.354s
ok  github.com/JasperXzy/harbormaster/internal/cli        20.281s
ok  github.com/JasperXzy/harbormaster/internal/config      1.410s
ok  github.com/JasperXzy/harbormaster/internal/retention   2.482s
ok  github.com/JasperXzy/harbormaster/internal/store     160.989s
npx --yes @redocly/cli lint docs/api/openapi.yaml
Your API description is valid. # exit 0，6 条原有警告
gofmt -l cmd internal          # exit 0，无输出
git diff --check -- internal deploy docs/m0 docs/api/openapi.yaml # exit 0，无输出
```

没有公网测量、真实云调用、agent 安装或真实通知发送，未修改 web/ 或 docs/design/。

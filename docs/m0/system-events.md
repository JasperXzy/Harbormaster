# ECS 系统事件来源

2026-11-12，PRD F11.3。当前完成：固定只读来源、分页与字段解析，三地域实际只读验证。持久化、周期调度、面板事件接口及通知尚未接入，不能标为预警功能已验收。

## 来源与真实结果

[DescribeInstanceHistoryEvents](https://www.alibabacloud.com/help/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstancehistoryevents) 默认只查询已结束事件。调用时必须显式包含 Scheduled、Executing、Inquiring 才覆盖未结束维护事件。本实现一次指定全部七种已声明状态，ResourceType 固定为 instance；生产读取器使用 NextToken / MaxResults，不用该分页模式下无意义的 TotalCount 判断完成。

官方当前说明：已完成事件可查询最近 30 天，未完成事件无此时间限制。没有添加发布时间过滤，避免把很早发布、尚未执行的维护事件排除。不是承诺本地已经保存 30 天历史。

[系统事件概览](https://help.aliyun.com/en/ecs/user-guide/summary)将 Instance:PreemptibleInstanceInterruption 列为状态变更通知，说明其在抢占回收前约 5 分钟发出。真实 CMS 元数据确认 Product=ECS、EventType=StatusNotification、Level=WARN、Status=Normal。读取器额外使用 [DescribeSystemEventAttribute](https://help.aliyun.com/en/cms/cloudmonitor-1-0/developer-reference/api-cms-2019-01-01-describesystemeventattribute)，固定 Product / Name，只查最近 24 小时；不创建订阅或报警规则。

已用 jasperxzy 的只读凭据查询上海、新加坡、香港两个来源，6 次读取全部 passed / complete=true。上海与新加坡均为空；香港 ECS 历史接口返回 1 条，CMS 24 小时窗口均为空。目录与查询摘录见 [system-event-evidence.json](system-event-evidence.json)。

实际香港记录为 EventType.Name=Instance:PreemptibleInstanceInterruption、Code=257，EventCycleStatus.Name=Executed、Code=0，EventPublishTime 与 EventFinishTime 均为 2026-11-03T07:42:20Z，NotBefore 缺失。这证明 ECS 历史接口也能返回该通知，不能从文档分类推断它绝不包含抢占中断；同时也不能由 Executed 推断实例已在该时刻释放。没有复现实时抢占回收，没有测得通知延迟。

## 读取边界

- 分开保留 ecs_history 与 cms_spot 的来源，源事件 ID、实例 ID、事件类型/代码、提供方状态/代码、发布时间、计划时间和结束时间。未返回时间保持 null，事件处理状态不替代实例状态。
- ECS 事件 ID 分页重复、游标循环、非法资源类型、缺少身份或错误时间均导致该来源失败，不发布部分页。CMS 独立分页；一个来源失败不阻断另一个来源。
- CMS 每个配置地域查询后，只接收 RegionId 匹配的数据；即便端点返回其他地域数据也不越过已配置地域边界。资源 ID 可为裸 ECS ID 或 `acs:ecs:region:account:instance/id`，ARN 的地域与资源类型必须匹配。
- 每来源总查询预算 10 秒、最多 50 页；错误只使用稳定代码。未知事件类型仍保留为 other，不丢掉提供方类型。未采集自由文本 Reason 或 Content。
- `Inventory.SystemEvents` 当前尚未由生产 worker 调用。后续须使用独立于默认 5 分钟资源扫描的事件轮询，才能避免把短期回收通知仅当作事后记录；实际提前送达仍需单独验收。
- 两个来源可能记录同一现实事件，当前不按不可靠时间/名称强行合并，也尚未生成通知；通知去重策略须考虑这个边界。

## RAM 与 PRD 建议

官方元数据确认 `ecs:DescribeInstanceHistoryEvents` 与 `cms:DescribeSystemEventAttribute` 都是 get，Resource 为 `*`。三档 RAM 草稿补入这两个读取动作，当前累计 14 / 20 / 28 个动作；没有改变真实 RAM 权限。DescribeSystemEventMetaList 元数据未声明 RAM Action，不因此猜测权限字符串。

F11.3 建议明确维护事件生命周期和抢占中断信号是不同语义；记录其原始状态和时间，不把 Executed 当作“已回收”，不从发布时间机械推导确切回收时刻。真实回收前的提前量、数据延迟及 Telegram 到达时间尚待验证；本轮未主动制造事件或调用调试事件写接口。

## 运行命令

```text
go test ./internal/m0
ok github.com/JasperXzy/harbormaster/internal/m0 0.505s

go run ./cmd/hm-server m0 query --account jasperxzy \
  --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc \
  --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key \
  --region cn-shanghai --check event-schema \
  --output /Users/jasperxzy/.local/share/harbormaster-m0/cpu-schema-_z5_lw7p/event-schema.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}

go run ./cmd/hm-server m0 query --account jasperxzy \
  --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc \
  --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key \
  --check system-events \
  --output /Users/jasperxzy/.local/share/harbormaster-m0/cpu-schema-_z5_lw7p/system-events.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}
```

命令均退出 0，报告仍在仓库外私有目录；仓库摘录不含真实账号数字、云实例 ID、事件 ID 或凭据。

最终工程验证（均退出 0）：

```text
go test ./internal/cloud/aliyun ./internal/model ./internal/m0
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 3.672s
ok github.com/JasperXzy/harbormaster/internal/model 0.608s
ok github.com/JasperXzy/harbormaster/internal/m0 (cached)

go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 48.118s
ok github.com/JasperXzy/harbormaster/internal/backup 37.547s
ok github.com/JasperXzy/harbormaster/internal/cli 9.041s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.460s
ok github.com/JasperXzy/harbormaster/internal/collect 4.597s
ok github.com/JasperXzy/harbormaster/internal/devmode 6.442s
ok github.com/JasperXzy/harbormaster/internal/m0 1.479s
ok github.com/JasperXzy/harbormaster/internal/model 1.333s
ok github.com/JasperXzy/harbormaster/internal/power 8.739s
ok github.com/JasperXzy/harbormaster/internal/rampolicy 1.356s
ok github.com/JasperXzy/harbormaster/internal/store 29.776s
其余有测试的包均 ok (cached)，命令包等为 [no test files]。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 84ms
Woohoo! Your API description is valid.

git diff --check
（无输出）
```

测试覆盖显式未完成状态、ECS token 分页不依赖 TotalCount、CMS 页码分页、来源独立失败、部分失败丢弃、重复事件/游标、时间与资源类型校验、地域隔离、ARN 与裸实例 ID、Executed/Normal 原样保留及缺失回收时间不推断。

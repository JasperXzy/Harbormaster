# ECS 系统事件来源

2026-11-12，PRD F11.3。当前完成：固定只读来源、分页与字段解析、持久记录、独立轮询及面板读取接口。通知与提前送达尚未验收，不能标为完整预警功能已验收。

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
- 来源层最初未接入生产 worker；后续独立轮询见下节。实际提前送达仍需单独验收。
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


## 持久化与独立轮询

迁移 021 增加 system_event 与 system_event_source。事件按账号、地域、来源、云事件 ID 唯一；本地 ID 与首次发现序号稳定，更新状态不移动分页位置。源事件同 ID 改绑另一实例视为错误，事务回滚。ECS/CMS 两个来源的记录不擅自合并。

成功查询写入/更新返回的事件，并将本次未返回的历史记录标为 seen_in_latest_scan=false。它们仍保留原 provider_status 和最后 observed_at，不合成 Canceled/Executed，也不删除。失败查询不使用部分结果、不推进事件时间，只更新来源 checked_at / last_error，保留 last_success_at。账号停用和移除地域后仍可查历史。

来源状态分为 ok / stale / error / paused。已停用账号或被移除的地域为 paused；配置修订变化、时钟异常或超过两个轮询周期未检查为 stale；近期读取失败为 error。首次查询前 sources=[]，不能据此认定没有事件。

新增 system_event_poll_interval_seconds / HM_SYSTEM_EVENT_POLL_INTERVAL_SECONDS，默认 60 秒，允许 15–3600 秒。独立持久 refresh 任务使用 system-events:<account_id> scope，来源为 schedule；不会重复执行资源/费用扫描。轮询仍共享既有 worker 与账号租约，并非专用实时通道；积压、网络和提供方延迟可能推迟观测。修改或停用账号会取消旧事件任务，恢复备份会取消旧工作并重置轮询时间。权限检查也读取两类来源，将缺失的事件权限归入 ecs_read / cms_read。

保存操作在事务内检查任务租约、账号租约和配置修订，旧 worker 不能发布新结果。不存在“读接口请求触发云查询”的捷径。

## 前端接口

`GET /api/v1/system-events` 使用管理员会话，返回 items、sources、next_cursor。支持 account_id、instance_id、region_id、source、category 和 limit/cursor；事件按首次发现序号倒序，最多 200 条。类别为 maintenance / spot_interruption / other，提供方原始类型与状态仍保留。

instance_id 为可空的 Harbormaster ECS ID：尚未被资源扫描发现或未保留该实例时为 null，cloud_instance_id 仍可用于识别。资源扫描后通过只读关联补出本地 ID，不重新创建事件。sources 与事件页面分开表达采集健康度，source 过滤可用于区分两套提供方语义。

`GET /settings` 同时返回事件轮询周期。开发模式有两套来源的合成示例，可联调筛选和分页。未增加新的通知发送入口，未执行事件处置、改期、取消云事件或开关机。

## 隔离库实测与最终检查

从此前只读验证库生成离线副本：先确认无打开句柄、无 WAL，复制前后确认主库大小/修改时间不变；副本目录 0700、数据库 0600，继续使用数据目录外的原验证主密钥。最初的原生 SQLite 备份尝试未成功，空库试跑不计入这里的结果。

```text
HM_MASTER_KEY= \
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/production-discovery-q8fh4ddr.key \
go run ./cmd/hm-server run-once \
  --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/system-events-offline-0ekppgxu
{"time":"2026-11-12T11:10:38.396Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

退出 0。进程结束后以离线只读方式检查副本：2 个账号、21 项迁移、8 个来源检查记录且 error_code 均为 null；香港那条历史抢占中断事件落库，provider_status=Executed，首次/最近观察为 2026-11-12T11:10:35.839353Z，seen_in_latest_scan=true。power job 数量为 0，foreign_key_check 为空。原验证库没有迁移或更新；没有开启真实电源执行。

最终命令均退出 0：

```text
go test ./internal/store ./internal/collect ./internal/api ./internal/config
ok github.com/JasperXzy/harbormaster/internal/store 1.943s
ok github.com/JasperXzy/harbormaster/internal/collect 0.808s
ok github.com/JasperXzy/harbormaster/internal/api 13.726s
ok github.com/JasperXzy/harbormaster/internal/config 0.360s

go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 50.554s
ok github.com/JasperXzy/harbormaster/internal/backup 37.772s
ok github.com/JasperXzy/harbormaster/internal/cli 9.279s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.523s
ok github.com/JasperXzy/harbormaster/internal/collect 5.517s
ok github.com/JasperXzy/harbormaster/internal/config 1.413s
ok github.com/JasperXzy/harbormaster/internal/devmode 6.893s
ok github.com/JasperXzy/harbormaster/internal/m0 1.546s
ok github.com/JasperXzy/harbormaster/internal/notify 2.175s
ok github.com/JasperXzy/harbormaster/internal/power 9.343s
ok github.com/JasperXzy/harbormaster/internal/store 31.445s
其余有测试的包均 ok (cached)，命令包等为 [no test files]。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 48ms
Woohoo! Your API description is valid.

git diff --check
（无输出）
```

新增测试覆盖失败/空扫描/状态变化的区别、跨重启历史、稳定分页、跨来源身份、事件改绑回滚、租约与修订隔离、暂停与过期状态、独立调度不重跑库存、权限结果合并、管理员认证、GET 不入队以及开发模式示例。实时抢占事件的提前到达与 Telegram 送达仍未证明。

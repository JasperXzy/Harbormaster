# 每日线路快照与变化事件

2026-11-13；PRD F10.9。基于[线路面板](trace-panel-api.md)和[持久归属](trace-enrichment.md)，不增加 agent 动作或云权限。契约先更新，前端仍由 Claude 通过 OpenAPI 对接。

## 已确认规则与接口

用户确认：每个已启用的测速组合独立开启每日快照，按业务时区配置时间，默认 03:00；停服不补历史，恢复后仅一次当前快照。两次结果必须目标 IP 相同、均到达目标、每个 TTL 都有唯一完整的 ASN 证据，才合并相邻相同 ASN 并比较。缺测、未知或多 ASN 不比较。ASN 数据库版本不同先不告警，以当前结果作为下一次比较起点。

- `GET /api/v1/network/plans/{id}/trace-schedule`：未配置时 revision=0、disabled。active 同时要求设置和父测速组合启用，inactive 时 next_run_at=null。
- `PUT` 同一路径：管理员 Session + CSRF，revision 乐观锁；enabled、notify_changes 必填，time_of_day 默认 03:00，max_hops/count/timeout_ms 默认 30/3/1000。与手工 trace 相同的发包总预算。不能给停用的测速组合新开一个启用的线路设置；错误为 plan_disabled。
- `GET /api/v1/network/plans/{id}/trace-snapshots`：实际调度时间 from/to，最多 31 天，游标与组合/范围绑定，按序号倒序。包含跳过、失败和等待归属；已删除组合保留历史。GET 不执行调度、比较、DNS、探测或云查询。

源、目标、地址族和 ICMP/TCP 方法继承测速组合；TCP 端口取目标当前配置。线路本身的次数/跳数/单次超时独立配置，避免将普通测速的包间隔或较大次数直接套到线路预算上。启用/编辑设置、编辑/重新启用父组合从下一个每日时间开始。目标编辑、组合编辑/删除、设置变更取消仍在排队的旧任务和待发线路事件；已经开始测量的结果保留，旧配置不能产生新的推送。

## 调度与存储

迁移 035 增加 trace_schedule、trace_snapshot_run 和 network_route_change 全局事件规则。后者非紧急、参数为空；每个组合还需显式 notify_changes=true。默认没有启用任何每日快照。

既有调度循环每五秒检查一次。每次最多处理 100 个到期组合，复用相同的提交校验和固定网络队列；线路、面板历史、快照行和 next_at 在同一 SQLite 事务提交。停服数天只产生一个 created_at=恢复当前时间的机会，due_at 保留原到期点，不伪造历史测量。源离线、没有线路协议能力、目标发现不完整/过旧/多地址、前次仍排队、缺 TCP 端口或队列满都记为 skip_reason，然后推进到下个每日时间。

每日时间按当地日历计算，不用固定 24 小时累加。重复小时取第一次；不存在的分钟取该日之后首个有效分钟；整个日期被时区调整跳过时跳到下个有效日。更改业务时区时只重新安排未来时间，不把旧 UTC 到期点解释成新时区的补测。以上时间边界、每次 100 项以及稍后提到的通知新鲜度是本次记录的工程约定。

每个快照冻结组合名称/版本、设置版本、目标版本、是否请求变化事件、原到期和实际调度时间，并关联原始 trace/job。测量结果和数据库版本仍由 trace 详情/导出读取。历史列表只读小型比较投影，不加载原始逐跳或完整归属 JSON。

## 比较与通知

后台处理尚未完成的比较，一次最多 100 项，在提交比较结果的同一事务内写通知记录。与紧邻的上一次调度机会比较，手工追踪不混入；跳过或失败会形成比较断点，不跳过断点去寻找更早的“好结果”。恢复后的首次完整路径可保留为新起点，下一次再正常比较。

比较状态为 waiting_result、waiting_enrichment、baseline、unchanged、changed、not_comparable；reason 解释缺测、归属缺口、多 ASN、目标地址变化、配置变化、数据库换版等。完整路径的候选序列保留在 asns；相邻相同 ASN 合并，不能把它称为测量到的 BGP AS path。私网跳没有公网 ASN，因此包含它的路径也会按用户选择保持不可比较。PTR/地理信息单独失败不破坏有效的 ASN 证据。

只比较相同组合/目标/设置版本，避免配置变化被记成网络变化。ASN 版本比较使用名称、edition、构建时间和文件大小；相同数据从内置 memory 切换为下载 mmap 不构成版本变化。数据库变更的当前快照仍保存有效候选序列，供之后同版本比较。

changed 才可能产生一次 network_route_change 事件，event key 为快照唯一 ID；重复调度或重启不会重复插入。推送还要求当前组合、账号、agent/目标仍适用、配置版本一致、组合明确开启且全局规则启用。超过十分钟的已完成观察只补历史比较，不产生新推送；此上限覆盖正常队列/测量/归属预算，不把长时间停服前的旧测量重新通知。静默/维护使用已有非紧急规则，只记录、不补发。变化是事件，不制造“恢复线路”通知。Title/Body 仍为占位内容，notification_id 仅说明记录已创建，不证明 Telegram 发送成功。

恢复备份会按用户既有决定取消旧任务、待发通知和未完成的比较，原因 backup_restored；保留已完成比较和历史。日常计划仍可按其下次时间安排新的当前测量。

## 验证方法与实际结果

临时 SQLite、注入的测量/离线 ASN、不可投递的加密测试渠道，无真实 DNS、探测、云写或消息发送。定向测试覆盖并发单次调度、未来激活、四天停服只一次恢复、事务回滚、编辑/停用/重启边界、时区改动、DST 重复/缺失分钟和跳过日期、完整 ASN 合并、未知/多路径、目标改变、版本变更后新起点、跨缺测不比较、事件幂等、全局关闭、静默、待发取消、恢复和删除后历史，以及 API 鉴权/CSRF/严格字段/分页/只读。

```text
go test -race ./internal/model ./internal/store ./internal/api ./internal/devmode -run 'Test(NextTraceTime|TraceASPath|TraceSnapshot|TraceSchedule|DemoTraces)' -count=1
ok internal/model 1.422s
ok internal/store 9.971s
ok internal/api 4.693s
ok internal/devmode 3.972s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0，均为已有警告

go test -race ./...
exit 0，全部包通过。最终重跑 internal/api 109.056s，其余 cached 或 no test files。
```

首轮全量回归中，其余包均通过（包括 store 123.331s、backup 48.302s、collect 13.138s、cli 21.342s）；API 一处旧断言仍认为只有十条可用规则，新增 network_route_change 后实际十一条。更新该断言后按上述命令全量重跑通过，没有删掉未核实的计量规则校验。

实际使用 `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o <临时路径> ./cmd/hm-server` 交叉构建六个目标，全部 exit 0：Linux amd64/arm64 为 37,021,310/35,301,878 字节，macOS 为 37,210,784/35,736,290，Windows 为 37,153,280/35,075,584。产物在临时目录 hm-trace-snapshot-build-8qut0tum，未部署。agent 本阶段没有变更，上一阶段两种 Linux 架构已实际构建通过。

开发模式增加两种组合的只读设置和各一条显式合成快照，链接已有 trace 示例；未知 ASN/未到达保持不可比较，不为演示伪造线路变化。PUT 返回 501，不启用实际调度。

结论：本地实现与上述边界已验证；真实公网每日执行、长期运行与真实通知送达尚未验收。双向追踪另属 F10.10，后续实现见[双向追踪](duplex-traces.md)。建议将用户已确认的日历/恢复和“可比路径”规则补入 PRD F10.9；PRD 原文未修改。

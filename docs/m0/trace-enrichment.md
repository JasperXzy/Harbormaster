# 线路归属、PTR 与证据标签

2026-11-13；对应 PRD F10.7、F10.8、F10.11。离线库的实际来源、许可、下载和资源测量见[离线库验证](offline-ip-database.md)。面板接口先更新 OpenAPI 顶部变更记录，再实现代码；web/ 与 docs/design/ 由 Claude 负责。

## 行为与持久化

线路结果接受的事务同时保存原始回报、列表 outcome 和 `trace_enrichment` 待处理项。归属 worker 与原始发包 worker 分开：处理归属不会重新领取测量任务、重发探测包或修改 agent 回报。迁移 034 为已有 outcome 的面板历史补一次待处理项；私有队列中没有面板记录的任务不补归属。

同一数据库最多一个归属任务运行，租约 45 秒；任务本身最多 30 秒。中断后可以在租约到期重新处理这些只读查询，旧 owner 无法覆盖新结果。成功完成后冻结结果；每月数据库更新不会回写历史。PTR 查询失败可产生 partial，缺少数据库覆盖或没有 PTR 则是正常的未知值。若希望再测，用新 request_key 提交新线路。损坏的存储回报标记 trace_enrichment_invalid，避免持续占用队列。恢复备份时按用户已确认的规则取消排队/运行的旧归属任务，保留已完成结果与原始测量。

一次处理只查询实际响应过的 IP，最多 150 个，按 IP 去重；不因为解析过目标域名就给未响应的目标补一条线路证据。原始字符串保留在 result，归属索引中的 IP 使用标准格式。离线批量读取持有同一代数据库的读锁，ASN 和地理库版本与覆盖缺口一起冻结，更新不能导致一条线路混用多代数据。默认国家级，不补城市或坐标；启用且成功加载 City 后才返回相应精度。

PTR 仅查询公网响应地址，私网、回环、文档网段、元数据等跳为 not_public，不发 DNS。使用 Go resolver，通过本机配置的 DNS 解析，不调用命令、不做正向验证或在线 ASN/地理查询。最多四个并发、单 IP 两秒、整批二十秒；到期未查询的地址保留 timeout。只保存最多八个去重、排序、规范化的合法主机名，每个最多 253 字节，异常内容和 resolver 原始错误不进入日志/面板。PTR 名称本身不证明对端身份。

上述并发和时间值是此次明确记录的工程上限，不是 PRD 已规定的业务默认。

## 字段与未知语义

历史 `TraceRun` 新增 enrichment_status：waiting_result、not_available、queued、running、complete、partial、failed。详情新增 enrichment 和 enrichment_error；updated_at 包括后台归属状态更新。GET 列表/详情/导出均只读，不能触发处理。列表不读取原始逐跳 JSON 或归属大 JSON。

enrichment 保存 enriched_at、两种实际数据库元数据、各 IP 的 ASN/地理/PTR、每 TTL 统计、标签及来源署名。每 TTL 的 attempts 是实际记录次数，responses 为响应次数，RTT 只统计有效响应；没有尝试时 no_response_percent=null，没有响应时 RTT=null。no_response_percent 不是该路由器的转发丢包率。

ASN 和国家按每个 TTL 的候选集合保存；未响应、未收录和多地址都保留。不会把多个可能路径压成一条确定的 BGP AS path。标签仅依赖这次离线库真实返回的 ASN，不靠熟悉的 IP 前缀猜补：

| ASN | code | 证据边界 |
|---|---|---|
| 4809 | cn2 | service_tier=unconfirmed，不能区分 GIA/GT |
| 4134 | chinanet_163 | 仅观察到的 IP 归属 |
| 9929 | cu_9929 | 同上 |
| 4837 | cu_4837 | 同上 |
| 58453 | cmi | 同上 |
| 45102 | alibaba_network | cross_border=unconfirmed |

标签附实际响应 IP、TTL 和 APNIC ASN 注册信息链接。注册归属与某条线路的等级、实际跨境状态是不同证据；detour_status 始终 unconfirmed。数据库估计的国家/城市可展示为位置序列，不直接判定绕行。这落实用户确认的 F10.8 修订方向；PRD 原文未改。

## 前端对接与导出

详情页按 enrichment_status 轮询，不能将尚未处理与“没有归属”混为一谈。需要使用的字段和枚举均在 `TraceEnrichment`、`TraceAddressInfo`、`TraceHopInfo`、`TraceNetworkLabel` 契约。国家级时 city/region/坐标为 null；PTR/ASN 分别显示各自状态。前端文案由 Claude 决定。

凡展示这些数据库结果的页面，需有可见的 [IP Geolocation by DB-IP](https://db-ip.com) 链接。导出在原始文本后追加带类型前缀的 JSON 行，保留可空值、数据库版本、匹配前缀、标签证据以及 CC BY 4.0 署名。开发模式提供显式 synthetic demo 归属与 PTR，仅构造固定数据，不访问 DNS 或真实账号。

## 验证方法与实际结果

使用临时 SQLite、注入测量/PTR 和真实内置离线库，验证事务回滚、并发领取、租约重新领取/旧 owner 拒绝、原始回报保持不变及 agent 重复 ACK、迁移补录幂等、恢复取消、只读 GET/导出、历史小投影、PTR 限额/取消/错误脱敏、候选多路径与空值、标签和署名。

实际内置 2026-11 库在此链路将 1.1.1.1 识别为 AS13335，返回国家级数据；59.43.1.1 的 ASN 仍为 not_found，没有猜成 CN2；192.0.2.1 不查 PTR。标签的六类覆盖使用明确的合成 ASN 数据，不冒充真实公网路径。

```text
go test -race ./internal/traceinfo ./internal/store ./internal/api ./internal/devmode ./internal/ipinfo -run 'Test(Trace|EvidenceLabels|PTR|BundledLookupEnrichment|Runner|DemoTraces|BatchPins)' -count=1
ok internal/traceinfo 6.077s
ok internal/store 15.346s
ok internal/api 8.720s
ok internal/devmode 4.269s
ok internal/ipinfo 4.062s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0，均为已有警告

go test -race ./...
exit 0，全部通过。本次重跑的主要包：
internal/agentclient 9.025s
internal/api 108.013s
internal/backup 45.491s
internal/cli 20.122s
internal/cloud/aliyun 4.444s
internal/collect 12.511s
internal/devmode 15.069s
internal/heartbeat 3.062s
internal/ipinfo 14.262s
internal/m0 1.465s
internal/model 1.338s
internal/notify 2.225s
internal/power 19.638s
internal/probes 12.587s
internal/store 116.948s
internal/traceinfo 7.697s
其余 cached 或 no test files。
```

实际交叉构建命令为 `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o <临时路径> ./cmd/hm-server`，六个目标全部 exit 0：Linux amd64/arm64 分别 36,921,750/35,153,262 字节；macOS amd64/arm64 为 37,116,576/35,666,130；Windows amd64/arm64 为 37,053,440/34,992,640。agent 同法构建 Linux 两架构，为 12,012,329/11,107,967 字节，未带入离线库。临时产物目录 hm-trace-info-build-wbpto8sw，未部署。

本阶段没有真实 agent 部署、原始套接字公网发包、实际路由器 PTR 或真实多跳线路的端到端验收。前述离线库验证不替代完整 hm-server 的 100MB 常驻内存验收；本阶段也没有云端写操作。

## 后续已确认的需求

用户已确认下一阶段按每个启用的测速组合单独开启快照，配置业务时区每日时间、默认 03:00；停服不补历史，恢复仅一次当前快照。线路变化仅比较目标 IP 相同、均到达目标、每 TTL 的 ASN 唯一且完整的结果，合并连续相同 ASN 后比较；缺测、未知或多 ASN 为不可比较。此文最初阶段尚未实现定时快照/变化检测；后续实现见[每日快照](trace-snapshots.md)，双向追踪仍独立记录。

归属依据：[DB-IP Lite](https://db-ip.com/db/lite.php)，以及 [AS4809](https://rdap.apnic.net/autnum/4809)、[AS4134](https://rdap.apnic.net/autnum/4134)、[AS9929](https://rdap.apnic.net/autnum/9929)、[AS4837](https://rdap.apnic.net/autnum/4837)、[AS58453](https://rdap.apnic.net/autnum/58453)、[AS45102](https://rdap.apnic.net/autnum/45102) 的 APNIC 只读注册记录；实际核对过程已记录在离线库文档。

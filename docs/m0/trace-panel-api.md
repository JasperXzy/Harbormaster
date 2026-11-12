# 线路提交、历史与导出接口

2026-11-13；PRD F10.7 / F10.11 的面板接入。执行基础见[内置线路追踪](network-trace-executor.md)、[agent 协议与恢复](agent-trace-tasks.md)及[server 线路执行](server-trace-tasks.md)。契约已先更新 `docs/api/openapi.yaml` 顶部记录。前端由 Claude 负责，本次未修改 web/ 或 docs/design/。

## 已实现

- `POST /api/v1/network/traces`：管理员 Session + CSRF，校验本地快照并原子入队，返回 `202 {job_id, trace_id}`。HTTP 请求内不做 DNS、云查询或探测。
- `GET /api/v1/network/traces?from=...&to=...`：按提交序号倒序分页；支持 point_id、target_id；列表返回请求和小型结果摘要，不载入原始逐跳结果。
- `GET /api/v1/network/traces/{id}`：读取状态、冻结的源/目标配置及已接受的逐跳结果。排队、租约到期或取消后没有测量结果时 result/outcome 为 null。
- `GET /api/v1/network/traces/{id}/export`：从数据库导出 UTF-8 文本，带状态、请求和每次原始响应；不触发网络或在线归属查询。
- `GET /api/v1/network/points`：增加独立的 trace 能力和不可用原因，供前端判断线路功能。

历史查询 from 包含、to 不包含，to 不超过当前时间，一次范围最多 31 天；limit 默认 50、最大 200，游标绑定范围及两个筛选项。范围限制不是数据保留期；自动保留/清理仍待后续阶段。修改目标或停用账号后旧历史仍可读取。

## 提交格式

直接目标示例，所有可空字段也显式发送：

```json
{
  "request_key": "trace_example_0001",
  "point_kind": "server",
  "instance_id": null,
  "target_id": null,
  "target_revision": null,
  "host": "1.1.1.1",
  "family": "ipv4",
  "method": "icmp",
  "port": 0
}
```

agent 源使用 point_kind=agent 和实例本地 ID。保存的目标使用 target_id、当前 target_revision，host=null；直接 host 与保存目标二选一。TCP 端口由本次请求显式指定（1–65535），覆盖目标预置端口；ICMP port=0。max_hops/count/timeout_ms 省略时分别为 30/3/1000；显式 0 不表示默认值。固定工程限制与底层执行器一致，包含发包间隔的预算不超过 120 秒，包含 DNS 的总时限为 125 秒。

request_key 为 16–128 位字母、数字、下划线或连字符。相同 key、相同解码后字段及默认值返回原 job/trace，即使任务已经完成、目标后来变更或源已离线；不会创建第二次测量。修改输入但复用 key 返回 request_conflict。客户端应为每次新的用户操作生成新 key；仅重试同一次请求时复用。

首次提交会校验源在线/协议证据、方法支持、目标版本及地址发现。源或目标不可用返回 409，不创建一个注定无法执行的任务。目标版本不匹配返回 network_configuration_changed，未知对象返回 404，字段非法返回 422，单源任务队列满返回 probe_queue_full。以上是本接口的工程约定，不更改已有周期测速对缺测/跳过的记录方式。

## 地址与能力证据

迁移 033 增加 `agent_trace_contact`，保存当前 token 哈希对应的最近认证线路轮询。普通测速轮询只更新 agent_probe_contact，线路轮询只更新 agent_trace_contact；不会从旧版 agent 的测速轮询推定它具备线路协议。

返回的 `point.trace` 包含 available、unavailable_reason、methods、last_poll_at。agent 需有当前凭据版本的发现证据、有效注册、启用且仍在发现中的实例、按既有掉线规则判断的新鲜流量上报，以及五分钟内的线路轮询。更换 token 后旧轮询记录不再生效。缺少线路轮询时为 agent_trace_unavailable。

server 的 methods 反映当前实现平台：ICMP 接口可用，TCP 原始线路目前仅 Linux。available 和轮询不证明实际原始套接字权限或公网可达；执行失败会成为明确的任务错误。本次没有真实 raw socket、部署或公网验收。

已纳管目标继续使用所选地址族当前唯一公网地址，要求发现完整、新鲜且与当前凭据版本一致。排队任务领取前重新检查目标版本和地址证据；发现变成不完整、过旧、无公网/多公网或 IP 改变时取消旧请求，保留原请求用于解释历史。目标配置修改/删除取消其排队线路，正在执行的任务可以完成。直接域名在后台只解析一次，并检查全部答案和固定实际探测地址。

## 数据与性能

`network_trace_run` 以 job_id 唯一关联既有网络队列，保存提交原字段、源 ID/名称、目标版本/配置快照和创建时间。源或目标之后改名不会改写旧记录。原始结果仍在 network_probe_job；结果接受时在同一 SQLite 事务内计算并保存小型 outcome，供列表使用。回报 ACK 重试不会产生第二条历史。

列表 outcome 包含 address、started_at/finished_at、reached、stop_reason、hop_count、sample_count；逐跳数组只在 detail/result 中返回。lease owner、token 哈希和私有上报包装不出现在面板或导出结果。状态 succeeded 只表示完成执行；TCP reset 表示到达目标但端口拒绝，max_hops 表示走到跳数上限。中间跳无响应保留 null，不补 RTT=0、不推定转发丢包。

## 前端联调

前端按契约生成新的 TraceSubmission、TraceRun、TraceDetail 和 TraceCapability 类型。提交前查看 point.trace，提交后用 trace_id 读取详情；通用 job.result 仍为 null。历史列表可直接展示 outcome，展开时再取逐跳详情。导出入口返回 text/plain 附件。

开发模式提供两个只读线路示例，ID 为 1/2：分别覆盖 TCP reset 到达目标、执行完成但未到达目标，包含缺测和私网中间节点。GET 列表/详情/导出都无需真实阿里云账号；POST 保持 demo 只读，返回 501，不会创建真实测量。示例方法不代表开发机器实际 raw socket 能力。

PTR、离线 ASN/地理库、线路类型识别、路径变化、定时快照及双向追踪仍未完成；本次没有填充未经验证的主机名、ASN、国家或线路标签。PRD 的“逐跳丢包”解释建议仍见执行器文档，PRD 原文未修改。

## 本地验证

定向测试已覆盖并发幂等入队、历史与摘要原子写入、旧配置快照保留、目标编辑/删除、托管 IP 变化/缺失/过旧导致取消、独立线路能力和 token 绑定、完整/未完成结果、范围/游标绑定、鉴权/CSRF、严格输入、文本导出及 demo 无真实任务。列表测试还破坏测试库原始结果 JSON，确认读取仍只依赖已保存摘要。全部测试使用临时库和注入结果，没有向公网发包。

```text
go test ./internal/store -run 'TestTrace(History|Source)'
ok github.com/JasperXzy/harbormaster/internal/store 0.564s

go test ./internal/api -run 'TestTracePanel'
ok github.com/JasperXzy/harbormaster/internal/api 0.903s

go test ./internal/devmode -run TestDemoTraces
ok github.com/JasperXzy/harbormaster/internal/devmode 0.576s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0，原有 license、两个公共操作 4xx 声明和三个未引用 schema 警告。

go test -race ./...
exit 0，全部包通过。此次重新运行的包：
internal/agentcli     1.408s
internal/agentclient  8.769s
internal/agentstate   1.758s
internal/api          98.957s
internal/backup       44.787s
internal/cli          16.545s
internal/cloud/aliyun 4.808s
internal/collect      12.155s
internal/devmode      11.127s
internal/heartbeat    3.361s
internal/m0           1.441s
internal/model        1.300s
internal/netprobe     1.348s
internal/notify       2.148s
internal/power        19.307s
internal/probes       12.020s
internal/store        111.159s
其余 cached 或 no test files。
```

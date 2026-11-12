# 测速目标、组合调度与历史接口

2026-11-12；对应 F10.1–F10.5 的后端配置、调度和读取部分。面板与设计由 Claude 负责，本阶段没有修改 web/ 或 docs/design/；不代表前端网络页、延迟告警或 M3 全部验收完成。

## 接口与开发模式

先修改 OpenAPI 顶部变更记录和完整数据结构，再实现下列会话认证接口；写入沿用 Origin + CSRF 校验：

- `GET /api/v1/network/points`：hm-server 与已纳管实例的测速点、可用状态、原因和时间。读取缓存，不查云、不发探测。
- `GET/POST /api/v1/network/targets`，`GET/PUT/DELETE /api/v1/network/targets/{id}`：自定义公网 IP / IDNA 域名，或跟随已纳管实例的目标；可选 TCP 端口。
- `GET/POST /api/v1/network/plans`，`GET/PUT/DELETE /api/v1/network/plans/{id}`：测速点 × 目标组合，方式、地址族、包数、超时、间隔、启停与周期。
- `GET /api/v1/network/plans/{id}/runs?from=...&to=...`：单组合最多七天范围内的运行记录，包含跳过原因、任务状态、实际请求、地址和逐次 RTT / 汇总。支持 24h / 7d 查询范围，不声称已经积累相应长度的真实数据。

列表默认 50、最大 200，ID 升序游标；运行历史按记录序号倒序，游标绑定组合与 UTC 时间范围。历史 from 包含、to 不包含且不能在未来；读取不会刷新、排队或填补缺失数据。组合 updated_at 表示配置更新时间，last_run.updated_at 表示本次任务状态/结果时间。任务 succeeded 仅表示测量完成，全部超时也可以完成测量，必须结合 loss_percent 判断可达性。计划列表/详情的 last_run 可用于前端矩阵的每个组合格；状态颜色阈值与网络告警尚未接入。

创建 revision=0；PUT 为完整配置替换并带当前 revision，不是 PATCH。过期修订返回 network_configuration_changed。组合省略数值选项时使用默认周期 300 秒、5 次、单次超时 2000ms、完成一次后间隔 1000ms；显式 0 不当作默认。范围沿用执行器的 1–20 次、100–5000ms、总预算不超过 60 秒，周期 60–86400 秒。

默认放入四个可编辑目标：1.1.1.1、8.8.8.8、cloudflare.com、google.com，TCP 端口 443。它们只是目标配置，**没有默认启用的组合，不产生自动测速**。TCP 是连接测试，不是 TLS / HTTP 服务健康检查。自定义目标未填端口时，ICMP 正常使用 port=0；TCP 组合会跳过并记录 target_tcp_port_required。

目标仍被未删除组合引用时不能删除（target_in_use）。删除目标/组合采用退役并保留历史；组合删除后运行历史仍可读取。修改、停用或删除配置会取消关联的 queued 任务；已 running 的测量可以完成，历史仍展示当时的请求和地址。

开发模式有四个目标、server/实例测速点、两组组合及最近一小时的合成运行记录，包含与生产相同的游标和时间范围检查；原有只读边界保持，写请求不会启动测速。Claude 可直接依据本契约生成类型并接入现有独立前端。

## 实例目标与测速点的可用性

用户已确认：只允许公网目标；实例目标跟随所选地址族当前唯一公网 IP，缺失、不完整或多候选地址时跳过，不擅自选一个地址。

新增 instance_probe_address 缓存每次发现的地址集合和 credential_version。它与面板保留旧地址的逻辑分开：失败/不完整发现使本次测速地址未知，不把保留给界面查看的旧值继续当成当前目标。保存仍在原资源扫描的账号修订/任务租约事务内；旧 worker 不能发布地址。

实例目标必须仍在发现快照中、账号启用、地址缓存属于当前凭据版本；当前实现对地址快照采用十分钟有效期。所选地址族只有一个去重后的普通公网地址才生成任务；零个、多于一个、缺失完整性或过期分别记录 target_no_public_address / target_ambiguous_address / target_discovery_incomplete / target_discovery_stale。IPv6 完整分配信息不等于已证实公网路由，真正可达性仍由测速结果体现。

排队期间地址变化或完整性失效，server/agent 领取前会再次校验跟随实例的目标；旧地址任务变为 probe_target_changed，不被执行。已经发出的探测不能撤回，其实际地址继续留在历史里。

agent 测速点还需要未撤销 token、当前凭据版本的实例发现、满足现有 agent_offline 规则时限的流量心跳，以及五分钟内使用当前 token 成功调用过测速领取协议。只有流量心跳的旧版 agent 不被假定支持测速，显示 agent_probe_unavailable。能力联系记录仅保存 token 哈希，不包含明文 token。available 不保证 ICMP socket 权限或目标可达性。

## 调度与持久化

用户确认“停服不补历史，恢复后安排一次当前测速”。实际调度对每个到期组合只处理一次，记录当前时间，再设 next_at=now+frequency；不会循环构造过去各周期的测量。新建、启用或更新后的启用组合立即到期，由后台下一轮处理。

每轮最多处理 100 个到期组合。读取配置、计算跳过原因、创建持久任务、记录 run 与推进 next_at 在同一个 SQLite 事务中。并发调度不能产生重复任务；请求键绑定组合 ID、修订和该次到期点。清理旧租约与校验排队地址每批只进行一次，避免按组合重复扫描整个队列。

同一组合已有 queued/running 任务时记录 previous_run_pending，避免积压。已到期的旧任务先终止，再处理当前周期。目标禁用、地址未知、agent 离线或无测速能力等都保存一条 skipped 记录，request/result 保持 null，不伪造零延迟或 100% 丢包。底层每个测速点的队列上限 100 沿用前阶段，超限记录 probe_queue_full。

迁移 030 增加目标、组合、运行关联、地址完整性缓存和 agent 领取联系缓存；已有 job/result 数据不改写。运行记录保存当时的组合/目标修订、测速点和实际请求；结果从已有专用任务表读取。服务端始终使用此前校验并重算过的统计，不能用客户端汇总替代原始样本。

## 实测与限制

在仓库外复制已离线、无 WAL/句柄的旧验证库，确认复制前后大小/mtime 不变，目录 0700、DB 0600；只将副本 next_scan_at 置零并运行只读 run-once。没有启动 serve，本机测速 worker 没有运行。

```text
HM_MASTER_KEY_FILE=<既有仓库外验证密钥路径> go run ./cmd/hm-server run-once --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/probe-address-offline-ue06xux0
{"time":"2026-11-12T15:01:14.019Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
exit 0
```

30 项迁移、外键检查无错误、四个预设目标、零组合、零 network_probe 任务。[脱敏地址证据](probe-address-evidence.json)保留实际来源状态：上海 ECS、香港 ECS 和 sha-pro 得到新地址缓存，每台一个 IPv4 候选；上海有一个已分配 IPv6、香港 IPv6 完整空列表、轻量 IPv6 仍未知。**新加坡没有本次新快照**，jasperxzy 的来源错误为 aliyun_unreachable；没有从该错误推断实例已释放或具体网络原因，也没有把保留的旧快照当成新地址数据。run-once 退出成功不等于每个云查询都成功。

真实地址来源核对不等于真实 ICMP / TCP 测速验收。本阶段没有公网探测、agent 安装、云资源写操作、Telegram 发送或真实部署。前端矩阵/曲线、网络告警、线路追踪/识别、离线库、长期覆盖和自动数据保留仍未完成。

## 本地测试范围

测试覆盖并发调度仅一次、停服一小时只安排一次当前测量、历史分页与退役保留、目标修订冲突/引用保护、排队旧地址取消、完整/不完整/空/多地址/过期地址、agent 离线及仅流量心跳不能宣称测速能力、凭据更换、session/CSRF、未知/缺失字段与数值限额、HTTP 配置/GET 无同步探测或云调用，以及开发模式契约数据。


## 实际检查命令与输出

```text
go test ./internal/store -run 'ProbeConfiguration|ManagedProbeTarget|ProbePointAvailability'
ok github.com/JasperXzy/harbormaster/internal/store 0.929s

go test ./internal/api ./internal/devmode -run 'NetworkManagement|DevelopmentContract'
ok github.com/JasperXzy/harbormaster/internal/api 0.946s
ok github.com/JasperXzy/harbormaster/internal/devmode 1.084s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 90.544s
ok github.com/JasperXzy/harbormaster/internal/backup 43.088s
ok github.com/JasperXzy/harbormaster/internal/cli 15.217s
ok github.com/JasperXzy/harbormaster/internal/collect 11.028s
ok github.com/JasperXzy/harbormaster/internal/devmode 7.311s
ok github.com/JasperXzy/harbormaster/internal/probes 4.277s
ok github.com/JasperXzy/harbormaster/internal/store 86.768s
其他测试包同样通过或 cached/no test files；exit 0。

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0（均为原有警告）
```

之后补充“仅流量心跳不能宣称测速能力”的断言，定向 store race 2.045s 通过。将同批调度的重复队列清理移到批次开始后，再执行：

```text
go test -race ./internal/store ./internal/probes ./internal/agentclient ./internal/collect
ok github.com/JasperXzy/harbormaster/internal/store 83.437s
ok github.com/JasperXzy/harbormaster/internal/probes 4.476s
ok github.com/JasperXzy/harbormaster/internal/agentclient 4.070s
ok github.com/JasperXzy/harbormaster/internal/collect 9.359s

go build ./... && go vet ./... && git diff --check
exit 0，无输出
```

没有 push，因此不声称远程 CI 已运行。真实网络页联调、实际 socket 权限和长期数据验证仍需后续验收。

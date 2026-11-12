# 公网监控回退来源验证

2026-11-12；PRD F5.1。此阶段验证数据来源，尚未接入生产资源快照或 24h/7d 曲线接口。hm-agent 的流量职责、CDT 对齐限制和保护开关均未改变。

## 方法与官方依据

先检查已真实获取的 ECS 指标目录，再对现有 Shanghai ECS 和 sha-pro 进行固定的只读查询。新增 M0 `--metric-source ecs-network` 和 `--check swas-network`，不接受任意 metric/action/endpoint。测试约束固定指标集合、Period=60、单地域和拒绝任意名称。

- [ECS 监控指标](https://www.alibabacloud.com/help/en/ecs/user-guide/view-the-monitoring-information-of-an-ecs-instance)：区分经典网络和 VPC 公网指标；基本指标不要求安装云监控 agent。
- [云监控基本指标](https://www.alibabacloud.com/help/en/cms/cloudmonitor-1-0/user-guide/overview-of-basic-and-operating-system-monitoring)：基本指标一分钟采样，速率为 bit/s；一分钟粒度 Average/Minimum/Maximum 相同。
- [轻量 DescribeMonitorData 官方 API 元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/DescribeMonitorData/api.json)：VPC_PUBLICIP_INTERNETIN_RATE / VPC_PUBLICIP_INTERNETOUT_RATE 为 bits/s；FLOW_USED 仅注明 bytes，没有说明本轮需要的采样周期累计口径。

2026-11-12 真实 ECS 目录确认：InternetInRate / InternetOutRate 为经典网络，维度 userId,instanceId；VPC_PublicIP_InternetInRate / VPC_PublicIP_InternetOutRate 维度为 userId,instanceId,ip。全部为 bit/s，支持 60/300/3600 秒。InternetIn / InternetOut 为经典网络 Byte 流量，不能替代 VPC 公网流量。

## 实际结果

2026-11-12T11:34:43Z–11:34:45Z 查询，均 passed、cloud_writes=0，无 NextToken。完整报告仅在私有目录；仓库[摘录](network-metric-evidence.json)只保留指标、时间、统计值和维度字段名，不保留账号 userId、实例 ID、IP 或请求 ID。

| 实例与来源 | 请求窗口 | 样本数 | 最新 Average（11:34:00Z） | 结论 |
|---|---|---:|---:|---|
| Ali_ShangHai / InternetInRate、InternetOutRate | 1 小时 | 各 60 | 均 0 bit/s | 该 VPC 实例的经典指标全窗口均为零，不能用来表示其公网流量 |
| Ali_ShangHai / InternetIn、InternetOut | 1 小时 | 各 60 | 均 0 Byte | 同样不能作为 VPC 公网流量来源 |
| Ali_ShangHai / VPC_PublicIP_InternetInRate | 1 小时 | 60 | 740 bit/s | 返回一组 IP 维度序列，已确认可读 |
| Ali_ShangHai / VPC_PublicIP_InternetOutRate | 1 小时 | 60 | 1924 bit/s | 返回一组 IP 维度序列，已确认可读 |
| sha-pro / VPC_PUBLICIP_INTERNETIN_RATE | 15 分钟 | 15 | 283712 bit/s | 轻量公网入速率已确认可读 |
| sha-pro / VPC_PUBLICIP_INTERNETOUT_RATE | 15 分钟 | 15 | 300400 bit/s | 轻量公网出速率已确认可读 |
| sha-pro / FLOW_USED | 15 分钟 | 0 | 无数据 | API 成功但 Datapoints 为 **空字符串**；不能把它解释为用量零，也不能推断周期累计口径 |

以上是存量实例当时的平均速率，不是带宽容量或吞吐压测。只传 instanceId 的 ECS 查询已返回 ip 字段；多公网 IP、多网卡、EIP 迁移或解绑历史、IPv6 和真实经典网络实例尚未验证。

## 对实现与 PRD 的建议

F5.1 可以提供云监控回退，但应保留 source、metric_name、unit、period、观测时间与查询时间；一分钟均值不能标为 agent 的十秒速率。ECS 应按网络类型及实际来源选择，不能在有有效 VPC 数据时优先取经典指标的 0。VPC 公网 IP 序列需要独立存储/辨别，不能与 EIP 指标重复相加，也不能把不同时点、缺失 IP 的残缺集合当作实例总值。

当前证据足够支持轻量速率和单公网 IP 的 ECS VPC 速率来源，但尚不足以证明准确的 24h/7d 流量总量。速率积分若用于流量展示必须标估算并处理缺失窗口；不能把结果当 CDT 实时计费或自动保护依据。原始样本跨小时如何分配的问题此前已提出，仍等用户答复。

建议在 F5.1 增加“云监控回退保留原始粒度与来源，缺失数据不得补零；VPC/EIP 多出口避免重复统计”。PRD 本文未修改。没有为验证安装云监控 agent、云助手或 hm-agent，也没有发送任何云写请求。

## 可复现命令

```text
go test ./internal/m0
ok github.com/JasperXzy/harbormaster/internal/m0 0.712s

go run ./cmd/hm-server m0 query --account jasperxzy \
  --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc \
  --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key \
  --region cn-shanghai --check metrics --metric-source ecs-network \
  --resource-id <已有 Shanghai ECS ID> --output <仓库外的新私有文件>
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}

go run ./cmd/hm-server m0 query --account winnemjw \
  --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc \
  --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key \
  --region cn-shanghai --check swas-network \
  --resource-id <已有 sha-pro ID> --output <仓库外的新私有文件>
{"account":"winnemjw","cloud_writes":0,"status":"report_written"}
```

实际执行时使用已有实例 ID 与新建私有输出文件，两个 query 均退出 0。输出目录为 `/Users/jasperxzy/.local/share/harbormaster-m0/network-metrics-89xupvx3`，目录权限 0700，报告由现有 M0 工具以 0600 创建；没有将凭据或原始响应提交到仓库。

最终工程检查：

```text
go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/cli 7.791s
ok github.com/JasperXzy/harbormaster/internal/m0 1.523s
其他测试包均 cached，命令包等为 [no test files]；exit 0。
```

本阶段未修改面板接口契约，前端无需据此生成新类型。

## 生产后台采集与缓存接入

`Instance.cloud_network` 已加入契约，前端可以在未安装 agent 的实例上展示云监控速率；保持与 `realtime` 分离，不能把一分钟平均速率当成十秒实测速率。当前采集所有已发现实例的云源作为独立数据，页面请求不会触发云查询。

ECS 由 DescribeInstances 的 `InstanceNetworkType` 选择 classic/vpc；未知类型不猜测。classic 查询实例维度 InternetInRate/InternetOutRate；vpc 仅在 IPv4 拓扑完整时，按当前公网 IP 构造 instanceId+ip 维度并读取 VPC_PublicIP_InternetInRate/VPC_PublicIP_InternetOutRate。不同 IP 保留独立序列，不与 EIP 指标叠加，不输出没有覆盖证明的总量。SWAS 使用自己的实例 ID 查询两项 VPC_PUBLICIP 速率。

后台每批最多 50 个 ECS 维度、轻量每次 1 台、入出各一次查询，窗口 15 分钟、Period=60、每页 Length=1440、最多 4 页，每个指标批次最多 5 秒；分页期间继续检查任务/账号租约与修订。范围外时间、非法或负数、错误实例/IP、同一序列同一时间冲突、分页失败和游标循环均不发布部分成功数据。明确的 0 是有效样本；空序列/轻量空字符串是 unknown。

每个方向保留 value、unit=bit/s、period_seconds=60、observed_at、checked_at、status 和 last_error。resource_snapshot 保存最新观测；同一来源和 IP 查询失败/空结果时保留旧值并改 stale，旧时间样本不能覆盖新值。拓扑不完整时保留旧序列并令 topology_complete=false，不能声称旧地址仍是当前地址。完整拓扑确认 IP 更换后，旧 IP 样本不会迁移给新 IP。接口将超过十分钟或未来时间的样本标 stale，不改写数据库。

开发模式有各自来源的合成速率并沿用示例实例 IP。实现尚不含 24h/7d 曲线、历史聚合、字节积分、IPv6 公网速率、CDT 对齐或保护动作；F5.1 仍是部分完成。真实经典网络、多公网 IP 和解绑/重绑历史目前仅有模拟测试，不能把单 IP 验收扩大为这些场景都已真实通过。

新增测试覆盖：按 IP 分开的样本/分页、零值、跨 IP 或跨实例响应拒绝、异常时间和值、冲突样本、第二页失败丢弃部分结果、空字符串保留未知、网络类型选择、未知拓扑不查询、旧样本保留/回退防护/IP 更换、以及 GET 仅老化缓存不发云请求。

### 生产路径实测与发现的轻量参数差异

从上次只读验证库再建离线副本，先确认无打开句柄、无 WAL，复制前后文件大小/修改时间一致；目录 0700、数据库 0600。只对副本执行 run-once，不启动电源 worker。

第一次于 11:44:39Z 完成：上海、新加坡、香港三台 ECS 的按当前 IP 查询成功，轻量的生产 CPU/速率均为 aliyun_unreachable。该错误分类不能证明具体网络或提供方参数错误，因此没有从它猜测原始原因。

核对轻量 [StartTime/EndTime 官方定义](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/DescribeMonitorData/api.json)：文档声明支持毫秒时间戳和 YYYY-MM-DDThh:mm:ssZ。生产原用毫秒字符串，先前成功的 M0 探测用 ISO 格式。此次将轻量 CPU/速率统一到已实测成功的 ISO 格式并增加请求测试；ECS CMS 参数不变。

一次 run-once 因尚未到下次资源调度时间没有产生新的资源观测，不计作复测通过。随后仅在离线验证副本将 next_scan_at 设为 0，重新运行只读采集，于 **2026-11-12T11:48:21.886Z** 完成：四台实例的云公网速率均成功落库；sha-pro 的 11:47:00Z 入/出样本分别为 223288 / 243816 bit/s，CPU 最新样本为 0.125%。这是选用 ISO 格式后的实测成功，尚不能单凭两次采样断言所有环境均不支持毫秒格式。

[生产路径证据](network-rate-runtime-evidence.json)保留首次和最后一次的脱敏摘录。最终 22 项迁移、0 个电源任务、foreign_key_check 无错误；未将 IP、账号 userId、实例 ID 或凭据写入证据文件。

```text
HM_MASTER_KEY= \
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/production-discovery-q8fh4ddr.key \
go run ./cmd/hm-server run-once \
  --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/network-rates-offline-aqnc6ndj
{"time":"2026-11-12T11:48:21.886Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

本阶段工程检查均通过：

```text
go test ./internal/cloud/aliyun ./internal/store ./internal/api ./internal/devmode
ok internal/cloud/aliyun 3.443s
ok internal/store 2.026s
ok internal/api 14.482s
ok internal/devmode 0.816s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok internal/api 59.045s
ok internal/backup 38.232s
ok internal/cli 9.380s
ok internal/cloud/aliyun 4.453s
ok internal/collect 5.576s
ok internal/devmode 6.508s
ok internal/m0 1.508s
ok internal/model 1.403s
ok internal/notify 1.795s
ok internal/power 9.460s
ok internal/store 35.208s
其余包 cached 或 [no test files]；exit 0。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 37ms
Your API description is valid.

# 轻量时间参数修正后的针对性复验
go test -race ./internal/cloud/aliyun ./internal/collect
ok internal/cloud/aliyun 4.472s
ok internal/collect (cached)
go build ./... && go vet ./...
exit 0，无输出
```

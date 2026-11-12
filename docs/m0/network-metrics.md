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

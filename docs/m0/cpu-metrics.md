# CPU 使用率与积分来源验证

2026-11-12，PRD F5.2 / F11.2。状态：ECS CMS 指标名称、单位和统计周期已真实只读确认；生产采集、实例展示及积分告警尚未实现。

## 方法与实际结果

先核对 CMS 2019-01-01 [DescribeMetricMetaList 官方元数据](https://api.aliyun.com/meta/v1/products/Cms/versions/2019-01-01/apis/DescribeMetricMetaList/api.json)，确认 Namespace / MetricName 都是可选查询条件，结果路径为 Resources.Resource，支持 PageNumber / PageSize。

在现有固定只读 M0 工具加入 `--check ecs-metric-schema`，查询 acs_ecs_dashboard 的完整目录，每页 30 项，沿用最大 50 页限制。未先猜测 CPU 积分的 MetricName；没有请求 Cloud Assistant 或安装 CMS agent。

使用已授权的 jasperxzy 只读凭据，在 cn-shanghai 发出目录查询。2026-11-12T10:18:00Z 完成 11 页，307 条记录，status=passed、complete=true，cloud_writes=0。凭据仍从仓库外加密 vault 读取；原始报告保存在仓库外 0700 目录、0600 文件。仓库只保存下表所需的字段摘录：[cpu-metric-evidence.json](cpu-metric-evidence.json)，不含密钥或账号身份数字。

| MetricName | 含义 | Unit | 周期（秒） | 统计量 |
|---|---|---|---|---|
| CPUUtilization | CPU 使用率 | % | 60 / 300 / 3600 | Average / Minimum / Maximum |
| TotalCredit | 累积 CPU 积分 | count | 60 / 300 / 3600 | Average / Maximum / Minimum |
| BurstCredit | 已消耗 CPU 积分 | count | 60 / 300 / 3600 | Maximum / Minimum / Average |
| AdvanceCredit | 预支 CPU 积分 | count | 60 / 300 / 3600 | Average / Minimum / Maximum |
| NotpaidSurplusCredit | 超额 CPU 积分 | count | 60 / 300 / 3600 | Minimum / Average / Maximum |

五项的 Dimensions 都为 userId,instanceId。本轮只有目录查询，没有查询任何实例的时间序列，不能据此证明指定实例有有效 CPU 数据或积分余额。

## 实现依据与 PRD 修订建议

1. F5.2 的 CMS 采集使用实际名称 CPUUtilization 与 TotalCredit，保留样本时间和查询时间。空序列应为未知，不能表示 CPU 使用率或余额为零。时间序列能否仅传 instanceId、是否还需 userId，需在实际数据查询时确认。
2. 第 9 节“CPU 积分低 <20%”没有给出分母。CMS TotalCredit 返回绝对积分，不是百分比。F11.2 启用前须明确按哪种余额上限归一化，或改成绝对积分阈值；本轮不启用此告警。
3. [DescribeInstanceTypes 官方元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstanceTypes/api.json) 包含 InitialCredit、BaselineCredit，但前者是初始积分，后者描述为所有 vCPU 的基准计算性能；不能直接将任一字段当作余额上限。
4. 云监控数据不改变已确认的 CDT 对齐限制，也不作为流量保护依据。仅查询 CMS 目录不会为 hm-agent 增加 CPU 或应用采集职责。

辅助官方说明：[突发实例监控](https://help.aliyun.com/zh/ecs/user-guide/monitor-burstable-instances)区分累积、消耗、预支、超额积分；[CMS 查询示例](https://www.alibabacloud.com/help/en/cms/cloudmonitor-1-0/developer-reference/api-cms-2019-01-01-describemetriclast)使用 acs_ecs_dashboard / CPUUtilization。本轮实际代码仍使用既有 CMS 2019-01-01 只读入口，没有切换到 CMS 2.0。

## 未完成与后续条件

- F5.2 生产采集、持久化、新鲜度处理和前端契约尚未接入。
- 真实时间序列与 t5/t6 积分余额尚未验证；可先读已有 ECS 的 CPU 数据。若没有真实突发实例，只能模拟测试积分流程，不能为了验证而创建收费资源。
- F2.4 的“本地备注、一个分组、多个文本标签”模型仍待用户确认；本轮未提前修改其接口或数据库。

## 实际命令

```text
go test ./internal/m0
ok github.com/JasperXzy/harbormaster/internal/m0 1.002s

go run ./cmd/hm-server m0 query --account jasperxzy \
  --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc \
  --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key \
  --region cn-shanghai --check ecs-metric-schema \
  --output /Users/jasperxzy/.local/share/harbormaster-m0/cpu-schema-_z5_lw7p/report.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}
```

两条命令均退出 0。新增测试证明计划只查询固定 ECS 指标目录，并正确翻到第二页，而非将第一页当作完整目录。

最终工程检查（均退出 0）：

```text
go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/cli 7.742s
ok github.com/JasperXzy/harbormaster/internal/m0 1.498s
其余有测试的包均 ok (cached)，命令包等为 [no test files]。

git diff --check
（无输出）
```

未修改面板契约，本阶段不需要前端重新生成类型。

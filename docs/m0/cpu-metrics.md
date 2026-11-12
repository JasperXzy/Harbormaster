# CPU 使用率与积分来源验证

2026-11-12，PRD F5.2 / F11.2。状态：ECS CMS 指标与 CPU 时间序列已真实只读确认；ECS 后台采集、缓存与只读字段已实现。轻量 CPU 来源和积分百分比告警尚未实现。

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

五项的 Dimensions 都为 userId,instanceId。目录阶段没有查询实例时间序列；后续真实查询及实现见下节。

## 实现依据与 PRD 修订建议

1. F5.2 的 CMS 采集使用实际名称 CPUUtilization 与 TotalCredit，保留样本时间和查询时间。空序列应为未知，不能表示 CPU 使用率或余额为零。后续已确认仅传 instanceId 能读取上海 ECS CPU 序列，无需读取或推断账号 userId。
2. 第 9 节“CPU 积分低 <20%”没有给出分母。CMS TotalCredit 返回绝对积分，不是百分比。F11.2 启用前须明确按哪种余额上限归一化，或改成绝对积分阈值；本轮不启用此告警。
3. [DescribeInstanceTypes 官方元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstanceTypes/api.json) 包含 InitialCredit、BaselineCredit，但前者是初始积分，后者描述为所有 vCPU 的基准计算性能；不能直接将任一字段当作余额上限。
4. 云监控数据不改变已确认的 CDT 对齐限制，也不作为流量保护依据。仅查询 CMS 目录不会为 hm-agent 增加 CPU 或应用采集职责。

辅助官方说明：[突发实例监控](https://help.aliyun.com/zh/ecs/user-guide/monitor-burstable-instances)区分累积、消耗、预支、超额积分；[CMS 查询示例](https://www.alibabacloud.com/help/en/cms/cloudmonitor-1-0/developer-reference/api-cms-2019-01-01-describemetriclast)使用 acs_ecs_dashboard / CPUUtilization。本轮实际代码仍使用既有 CMS 2019-01-01 只读入口，没有切换到 CMS 2.0。

## 未完成与后续条件

- F5.2 的 ECS 采集、持久化、新鲜度处理和契约现已接入；轻量仍待来源验证。
- 上海 ECS 的 CPU 序列已验证；t5/t6 真实积分余额尚未验证。若没有真实突发实例，只能模拟测试积分流程，不能为了验证而创建收费资源。
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


## 实际时间序列与生产接入

在 M0 固定只读探测增加 `--metric-source ecs`，仅允许已确认的 CPUUtilization 和 TotalCredit。对原有上海 ECS（Ali_ShangHai，ecs.e-c1m1.large）查询最近一小时，Dimensions 仅传 instanceId；2026-11-12T10:22:15Z 两个请求均 passed / complete=true。CPU 返回 60 个一分钟样本，最新点时间为 2026-11-12T10:22:00Z，Average=1.187%；TotalCredit 返回空序列。本轮不能由此验证真实突发实例积分余额，也没有把空序列归零。

实际查询命令：

```text
go run ./cmd/hm-server m0 query --account jasperxzy   --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc   --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key   --region cn-shanghai --check metrics --metric-source ecs   --resource-id i-uf65eqxuucx20ck92au3   --output /Users/jasperxzy/.local/share/harbormaster-m0/cpu-schema-_z5_lw7p/shanghai-cpu-samples.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}
```

退出 0；摘录只记录指标样本和实例别名，未将响应中的 userId 写入仓库。

生产后台资源扫描现在为已发现的 ECS 按地域、每批最多 50 台查询两项指标。取最近 15 分钟、Period=60，每页最多 1440 点、最多 4 页，每个指标批次总超时 5 秒；每页遵守原有账号/任务修订检查和固定只读调用限制。以上是实现的工作量和超时边界，不是云服务性能承诺。查询参数与响应字段依据 [DescribeMetricList 文档](https://help.aliyun.com/en/cms/cloudmonitor-1-0/developer-reference/api-cms-2019-01-01-describemetriclist)。

`Instance.cpu` 提供 usage / credit_balance，分别对应 CPUUtilization（percent）和 TotalCredit（count）。value 是最新时间点的 Average，保留 observed_at（提供方样本）与 checked_at（查询完成/尝试）两种时间。分页失败、未知实例、冲突重复点、未来/范围外时间、非法数值和 CPU 超过 100 都不会发布为有效样本。空序列保留未知；显式 0 是有效值。

最新数据随已有 resource_snapshot 持久化，无需额外历史表。失败/空结果保留旧值与旧样本时间，状态改为 stale；返回更旧的样本也不会倒退覆盖。API 只读取缓存，距离样本超过 10 分钟时即时将响应状态标为 stale，不改写数据库或请求云接口。10 分钟是当前新鲜度工程边界，采集频率仍跟随已配置的资源扫描周期（默认 5 分钟）。

开发模式提供明确的合成 CPU 使用率和未知积分；SWAS 的 cpu=null。hm-agent 没有增加 CPU 采集，未启用百分比积分告警，未改变 CDT 对齐、估算或停机条件。前端需按 source/status/unit/两个时间字段展示，不能把 TotalCredit 的 count 当百分比。

ECS 生产采集阶段最终检查（均退出 0）：

```text
go test ./internal/cloud/aliyun ./internal/store ./internal/api
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.028s
ok github.com/JasperXzy/harbormaster/internal/store 1.989s
ok github.com/JasperXzy/harbormaster/internal/api 13.833s

go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 48.718s
ok github.com/JasperXzy/harbormaster/internal/backup 37.871s
ok github.com/JasperXzy/harbormaster/internal/cli 9.285s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.497s
ok github.com/JasperXzy/harbormaster/internal/collect 4.794s
ok github.com/JasperXzy/harbormaster/internal/devmode 6.684s
ok github.com/JasperXzy/harbormaster/internal/m0 1.560s
ok github.com/JasperXzy/harbormaster/internal/model 1.391s
ok github.com/JasperXzy/harbormaster/internal/notify 2.161s
ok github.com/JasperXzy/harbormaster/internal/power 9.135s
ok github.com/JasperXzy/harbormaster/internal/store 29.349s
其余有测试的包均 ok (cached)，命令包等为 [no test files]。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 44ms
Woohoo! Your API description is valid.

git diff --check
（无输出）
```

新增验证覆盖 50 台分批、跨页、部分失败不发布、显式零值、绝对积分、空/错实例/非法时间与数值、重启后保留旧样本、拒绝更旧样本覆盖，以及 HTTP 读缓存过期不改时间、不写数据库、不入队。没有真实云写操作或前端源码变更。

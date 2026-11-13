# CPU 积分上限验证

2026-11-13。补充 PRD F5.2 / F11.2 与 [既有 CPU 指标验证](cpu-metrics.md)。状态：官方上限概念、四个规格的字段关系已确认；百分比告警定义待用户决定，真实突发实例的积分时间序列仍未验证。

## 方法与结果

核对 [突发性能实例说明及 t5/t6 表](https://help.aliyun.com/zh/ecs/user-guide/burst-performance-instance-overview)、[积分变化示例](https://help.aliyun.com/zh/ecs/user-guide/cpu-credit-change-examples)、[DescribeInstanceTypes 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstanceTypes/api.json)及[官方生成的 Go SDK 类型](https://github.com/aliyun/alibaba-cloud-sdk-go/blob/master/services/ecs/struct_instance_type.go)。API/SDK 的 `InitialCredit`、`BaselineCredit` 为整数；描述没有直接提供“最大余额”字段。

新增 M0 固定检查 `ecs-credit-specs`：只允许单个已授权地域，用官方签名 SDK 查询下表四个固定规格，最多 100 条/页，按顶层 `NextToken` 分页；不新增任意 RPC 或端点入口。它读的是规格目录，不创建突发实例，也不查询、变配或启动其他资源。

使用已导入的 jasperxzy 只读账号、上海地域，2026-11-13T01:07:29Z 返回 1 页、4/4 规格，`passed` / `complete=true` / `cloud_writes=0`。原报告保存在仓库外 0700 目录、0600 文件；仓库仅保存无账号身份或凭据的[字段摘录](cpu-credit-spec-evidence.json)。

| 规格 | vCPU | API BaselineCredit 原值 | API 初始积分 | 官方每 vCPU 基准 | 官方积分/小时 | 官方最大余额 |
|---|---:|---:|---:|---:|---:|---:|
| ecs.t5-c1m1.large | 2 | 50 | 60 | 25% | 30 | 720 |
| ecs.t5-lc2m1.nano | 1 | 20 | 30 | 20% | 12 | 288 |
| ecs.t6-c2m1.large | 2 | 20 | 60 | 10% | 12 | 288 |
| ecs.t6-c1m1.large | 2 | 40 | 60 | 20% | 24 | 576 |

## 结论及推断边界

官方最大累积余额对应 24 小时可获得的积分；一次性初始积分单列。官方示例中，t6-c2m1.large 可用积分曾为 348，即 60 初始积分加 288 最大累积余额。因此不能把“可用积分的最大值”和“常规累积余额上限”当作同一个数，也不能未经选择就截断为 100%。

**由四行实际返回值与官方表交叉推断**：`BaselineCredit` 对应所有 vCPU 的基准百分数之和，例如 2×10%=20；它不是每小时积分，更不是最大余额。对这些已核对的规格：

```text
每小时积分 = BaselineCredit × 60 / 100
24 小时累积余额上限 = BaselineCredit × 60 × 24 / 100
```

计算时不能再次乘 vCPU 数，也不能将 `InitialCredit` 当作累积上限。四个规格的计算值与文档表一致。这是有范围的交叉推断，API 没有直接返回该换算单位；不将它声称为已验证所有规格/未来产品或真实 CMS 序列。官方生成 SDK 的整数类型也不能单独证明单位。

当前已纳管 ECS 不是突发规格，既有 `TotalCredit` 查询为空；本次目录查询不会改变这个事实，也没有证明 CMS TotalCredit 如何包含初始积分、在各种停机/变配状态下如何变化。要验收这些行为，需要现有 t5/t6 实例的只读时间序列；不会为此创建收费资源或执行停开机实验。

## PRD 修订建议与待答

建议明确第 10 节及 F11.2 的 20% 分母：若采用 `TotalCredit / 该规格 24 小时累积余额上限`，分母不另加一次性初始积分；只有可信且新鲜的余额与规格信息均具备时才判断，缺失/过期保留未知，比例大于 100% 保留原值。也可改为绝对积分阈值，但须另定默认值。

该选择已提给用户，尚未同意；当前没有启用积分百分比告警，没有修改 PRD、面板契约或生产采集字段。原 CPU 文档中“InitialCredit / BaselineCredit 均不能直接当上限”的结论继续成立，新增的是可交叉核对的换算证据。

## 工程验证

新增分页测试发现旧 M0 执行器只识别 BSS 的 `Data.NextToken`。现为规格目录识别顶层 `NextToken`，保留原账单路径，并测试多页、重复/非法游标、签名只读 HTTPS、参数及写动作拒绝。第一次修复前的目录请求不作为完整性证据，以上结果来自修复并通过测试后的重新查询。

实际命令：

```text
go test ./internal/m0 -count=1
ok  github.com/JasperXzy/harbormaster/internal/m0  0.531s

go run ./cmd/hm-server m0 query --account jasperxzy \
  --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc \
  --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key \
  --region cn-shanghai --check ecs-credit-specs \
  --output /Users/jasperxzy/.local/share/harbormaster-m0/credit-specs-verified-c0cvefim/report.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}
```

两条命令均 exit 0。受影响包与检查实际结果：

```text
go test -race ./internal/m0 ./internal/cli
ok  github.com/JasperXzy/harbormaster/internal/m0   1.593s
ok  github.com/JasperXzy/harbormaster/internal/cli  25.563s
go build ./...        # exit 0，无输出
go vet ./...          # exit 0，无输出
gofmt -l cmd internal # exit 0，无输出
git diff --check -- internal/m0 docs/m0  # exit 0，无输出
```

此前审计阶段的全量 race 已通过，见[审计验证记录](audit-log.md)；本批增量只影响 M0 查询器/CLI，未再次运行无关存储或网络执行器的完整测试。没有修改 web/、docs/design/ 或面板接口契约。

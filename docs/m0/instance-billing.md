# 按实例费用来源验证

2026-11-12；PRD F9.2。以 DescribeInstanceBill 查询当前业务月的 MONTHLY / IsBillingItem=false 实例账单，不用已被替代的 QueryInstanceBill，也不按实例逐个调用。

## 官方依据与方法

[官方 API](https://www.alibabacloud.com/help/en/user-center/developer-reference/api-bssopenapi-2017-12-14-describeinstancebill) 与 [API 元数据](https://api.aliyun.com/meta/v1/products/BssOpenApi/versions/2017-12-14/apis/DescribeInstanceBill/api.json) 确认：Data.Items 为数组，Data.NextToken 为空表示结束，MaxResults 最大 300；RAM action 为 `bssapi:DescribeInstanceBill`、资源 `*`，不是既有余额和汇总账单的旧 bss action。

账单约延迟 24 小时、实例附加信息约延迟 48 小时；当月缺少尚未出账的数据，不能作为最终对账结果。共享带宽等产品不会自动给出每个下属资源的拆分费用。保持账单原类型（订购、按量、退款、调整）及币种，不据此计算未经确认的退款净额或实时费用。

M0 新增固定 `instance-billing` 查询；最多 50 页、每页 300 行、调用间隔 250ms，拒绝循环游标、异常结构、分页失败及总数不一致的终页。缺失 NextToken 在实际终页已观察到，作为结束处理。BSS 响应使用 json.Number 保留金额十进制精度，不经过 float64；兼容原有页码型查询。

## 实际结果

两账号于 2026-11-12T13:37:23Z 成功，分别 15 行、2 行，均一页完整，无云写请求。[脱敏证据](instance-billing-evidence.json)保留精确金额和匹配结论。

- jasperxzy：两台已纳管 ECS 按实例 ID 精确匹配；上海 ECS 无匹配行，不能解释为零费用。其他账单不强行分配到当前实例。
- winnemjw：sha-pro 按轻量 API 实例 ID 精确匹配两条订购账单；ProductCode=ace_eweb、PipCode=swas、CommodityCode=swas。不能仅凭 ProductCode=swas 过滤。
- 新接口未返回 PaymentAmount / OutstandingAmount；不要复用旧汇总账单的必填字段或补零。PretaxAmount 是应付金额，AfterDiscountAmount 是优惠后金额，两者不能混称实付。
- 两个账号都只验证了 CNY。国际站、多页真实数据、退款与调整真实样例尚未验证；分页与金额精度有本地模拟测试。

## PRD 修订建议

F9.2 明确“当前月已出账、能精确匹配实例的账单条目”，保留账单类型、币种、查询时间。无匹配行应显示暂无可归属账单，不能推断零费用；不将共享资源成本分摊到 ECS，也不将月内订购支出当成月度摊销成本。PRD 原文未改。

## 执行与检查

```text
go test ./internal/m0
ok github.com/JasperXzy/harbormaster/internal/m0 0.783s

go run ./cmd/hm-server m0 query --account jasperxzy --check instance-billing --month 2026-11 --output <私有目录>/jasperxzy.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}

go run ./cmd/hm-server m0 query --account winnemjw --check instance-billing --month 2026-11 --output <私有目录>/winnemjw.json
{"account":"winnemjw","cloud_writes":0,"status":"report_written"}
```

两次 query 均退出 0。私有目录 `/Users/jasperxzy/.local/share/harbormaster-m0/instance-billing-abwk2wvq` 为 0700，报告 0600；凭据沿用仓库外加密 vault 和外置主密钥。此来源验证阶段未修改面板契约，生产缓存与只读 API 在下一阶段接入。

```text
go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/cli 10.402s
ok github.com/JasperXzy/harbormaster/internal/m0 1.741s
其余测试包 cached 或 no test files，exit 0。
```

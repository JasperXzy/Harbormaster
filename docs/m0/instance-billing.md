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

## 生产采集、缓存与面板接口

已增加 `GET /api/v1/instances/{instance_id}/finance`，先更新 OpenAPI 顶部变更记录及字段，再实现后端。由正常资源采集按账号读取当前业务月，单次最多 50 页 / 15000 行 / 30 秒，每页前检查账号与任务租约；任何分页失败、游标循环、账期不符、金额异常或总数变化均不发布部分数据。与其他费用来源分别记录结果，GET 不访问阿里云、不排队。

迁移 027 扩展 account_finance 的 source，原样保留已有余额和汇总账单。实例账单同样按账号、credential_version、账期隔离，SaveScan 原子校验租约和配置修订后保存。失败保留旧成功数据及 fetched_at，同时更新 last_attempt_at / last_error；停用保留历史，换凭据不回退到旧凭据数据，跨月不拿上月账单顶替。

接口按 instance.cloud_id 与账单 InstanceID 精确比较，并要求 PipCode 与 ecs/swas 类型一致；不使用名称、IP、前缀匹配或账单附加地域名称猜归属。响应保留多行、币种、bill_type、product_code、pip_code、pretax_amount、after_discount_amount；不输出该来源没有的实付/欠付字段。成功无匹配行为 items=[]；来源未成功为 data=null。开发模式提供三台示例实例的合成账单，Claude 可独立接入这个接口。

RAM 三档草稿增加只读 `bssapi:DescribeInstanceBill`，未向真实身份授予权限。原有 bss 权限不据此推定包含新 action。工程边界是每次完整扫描最多 15000 行与 30 秒；超限显示源失败并保留旧值，后续可依据实际规模调整，不静默截断。

### 真实生产路径结果

从已离线、无句柄和 WAL 的旧验证库复制到新 0700 目录，DB 为 0600，复制前后大小和修改时间一致。仅在副本将 next_scan_at 置零，以触发本次只读采集；不启动 serve 或电源 worker。

```text
HM_MASTER_KEY_FILE=<仓库外既有验证密钥路径> go run ./cmd/hm-server run-once --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/instance-finance-offline-tmknxxan
{"time":"2026-11-12T13:47:04.189Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
exit 0
```

两账号分别在 13:46:53Z / 13:47:01Z 完成实例账单采集，缓存 2 / 15 行；两台 ECS 各匹配一行，sha-pro 匹配两行，上海 ECS 空匹配。实际应付值按十进制字符串保存；没有把“优惠后 270、应付 0”的轻量行显示成实付 270。27 项迁移、foreign_key_check 无错误、电源任务为 0。[生产路径脱敏证据](instance-finance-runtime-evidence.json)。

测试覆盖精确小数、两页完整成功、中途权限失败/租约失效/取消、循环和超限、结构错误、总数变化、空结果、旧缓存保留、停用/凭据轮换、旧 lease 拒绝、业务时区月边界、精确 ID/产品过滤、同实例多行保留、会话保护、GET 不查云/不建任务，以及迁移保留旧数据和约束。国际站/退款/调整仅模拟；真实长分页与最终账单对账仍待对应账单条件。

### 接入阶段最终检查

```text
go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 81.303s
ok github.com/JasperXzy/harbormaster/internal/backup 41.052s
ok github.com/JasperXzy/harbormaster/internal/cli 13.260s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.455s
ok github.com/JasperXzy/harbormaster/internal/collect 8.636s
ok github.com/JasperXzy/harbormaster/internal/devmode 6.863s
ok github.com/JasperXzy/harbormaster/internal/heartbeat 2.590s
ok github.com/JasperXzy/harbormaster/internal/model 1.333s
ok github.com/JasperXzy/harbormaster/internal/notify 1.984s
ok github.com/JasperXzy/harbormaster/internal/power 14.371s
ok github.com/JasperXzy/harbormaster/internal/rampolicy 1.324s
ok github.com/JasperXzy/harbormaster/internal/store 61.919s
其余包 cached 或 no test files，exit 0。

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
Your API description is valid. You have 6 warnings.
exit 0
```

六条契约警告为原有 license、两个公开读取缺 4xx 声明及三个未引用组件，本阶段新接口无新增警告。仅读取前端现有工具执行校验，未生成或修改 web/ 文件。

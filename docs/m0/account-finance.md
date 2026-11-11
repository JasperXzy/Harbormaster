# 账号余额与本月账单缓存

2026-11-12，PRD F9.1 后端实现。新增 `GET /api/v1/accounts/{account_id}/finance`，仍使用管理员会话，只读取 SQLite。前端无需直接调用阿里云；现有后台资源扫描、手动刷新及权限检查会更新费用快照，默认跟随 300 秒资源扫描周期。

## 字段来源

- [QueryAccountBalance](https://www.alibabacloud.com/help/en/user-center/developer-reference/api-bssopenapi-2017-12-14-queryaccountbalance)：分别保留 AvailableAmount、AvailableCashAmount 和可选 CreditAmount。可用额度与现金余额不混为一谈，币种采用返回的 Currency。
- [QueryBillOverview](https://help.aliyun.com/zh/user-center/developer-reference/api-bssopenapi-2017-12-14-querybilloverview)：明确传入 BillingCycle=YYYY-MM，逐条保存账单类型、产品代码/名称、币种、PretaxAmount、PaymentAmount、OutstandingAmount。保留 Refund / Adjustment 等类型及返回的金额符号，不推断退款应该再取负，也不把不同币种或不同金额口径混成总额。成功空列表仍为空列表。
- 国内站端点 business.aliyuncs.com、国际站 business.ap-southeast-1.aliyuncs.com，按账号 site 选择，与实例地域无关。权限分别为 bss:DescribeAcccount 和 bss:DescribeBillList；权限检查的 bss_read 现在覆盖这两项实际请求。

账期由持久化 business_timezone 的当前月份决定，未初始化时使用服务端配置默认值；账期字符串明确返回给页面，并核对提供方返回的 BillingCycle。fetched_at 是查询成功时间，不是提供方的计费截止时间，也不宣称本月金额已经最终结算。

账单概览是账号维度的提供方账单，可包含返回的其他阿里云产品类别。本阶段未实现实例维度费用、欠费阈值告警、汇率转换或净费用总额；前端可直接展示契约中的明确金额字段和账单条目，文案由 Claude 定稿。`web/`、`docs/design/` 未修改。

## 精度与未知数据

SDK 的普通 JSON 解析会先转换为浮点数。对 BSS 响应改为由官方 SDK 读取字符串响应，再用 Go JSON decoder 的 UseNumber 解析；签名、端点白名单、HTTP 错误处理和请求限速仍使用既有实现。

金额只接受原始字符串或 json.Number，使用精确有理数检查和十进制规范化，SQLite 与面板均保存/返回字符串。支持合法千分位、负值和科学计数法；不使用 float64 参与金额计算。当前工程边界为最多 12 位有效小数、有限输入长度及数值大小，超出范围直接报来源数据无法识别，不舍入成另一个金额。金额字段缺失、格式无效、币种无效或账期不符时失败；不补零。

## 持久化与刷新

迁移 009 增加 account_finance，主键为 account_id、credential_version、source、billing_cycle。余额不区分月份；账单按月保存当前快照，轮询更新同一行，不逐次累积完整账单。只保存白名单字段，不保存账单账号邮箱、原始响应或凭据。

费用写入位于 SaveScan 的同一事务中，检查账号 revision、任务租约和启停状态。凭据轮换后，读取只匹配新 credential_version，避免新密钥实际属于另一个云账号时展示旧费用。停用保留缓存且停止采集；换月后查询新账期，未采集时不会借用上月数据。

余额与账单可以独立成功/失败。失败更新 last_attempt_at 和 last_error，保留上次成功 data 及 fetched_at；从未成功则 data 为 null。status 为 never_synced / ok / failing，表示最近一次查询结果，不表示数据永远新鲜，页面须同时看时间和账号 enabled。updated_at 是两个来源最近一次尝试的较晚时间。

开发模式包含 CNY 和 USD 合成示例，返回同一结构，无真实账号依赖。生产接口没有新增同步云查询；现有实例刷新任务即可触发费用刷新。

## 真实只读验收

使用已有仓库外验收数据库和主密钥，执行：

```text
go build -o deploy/bin/hm-server-readonly-check ./cmd/hm-server
HM_MASTER_KEY_FILE=<已有仓库外主密钥文件> ./deploy/bin/hm-server-readonly-check run-once --data-dir <已有隔离验收目录>
{"time":"2026-11-11T22:16:11.088Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

数据库关闭且无 WAL 残留后，以只读模式提取状态和条目数，未输出真实金额：

| 账号 | 余额来源 | 2026-11 账单来源 | 币种 | 账单条数 |
|---|---|---|---|---|
| jasperxzy | 成功，error=null | 成功，error=null | CNY | 10 |
| winnemjw | 成功，error=null | 成功，error=null | CNY | 3 |

成功抓取时间为 2026-11-11 22:16:07–10 UTC。真实余额和账单金额仅在仓库外缓存，未写入文档、日志或提交。国际站端点与 USD 金额已在模拟 HTTP 签名测试中验证，尚无真实国际站账号验收；没有执行任何云写操作或 Telegram 发送。

## 测试结果

```text
go build ./...                       exit 0（无输出）
go vet ./...                         exit 0（无输出）
go test -race ./...                   exit 0
  internal/api                       26.531s
  internal/cli                       2.806s
  internal/cloud/aliyun               1.422s
  internal/collect                    2.537s
  internal/devmode                    5.483s
  internal/notify                     1.579s
  internal/store                      8.214s
  其他有测试的包                       cached / passed
npm --prefix web run lint:api         Your API description is valid.
git diff --check                     exit 0
```

针对性测试覆盖：超过 float64 精确范围的金额、12 位小数、负数、科学计数法、合法/非法千分位、拒绝浮点输入、签名及站点端点、错误账期与空账单、来源独立失败、凭据隔离、停用保留历史与停止采集、采集中停用导致租约失效且结果不发布、失败保留原金额和时间、认证与只读 GET、开发示例。

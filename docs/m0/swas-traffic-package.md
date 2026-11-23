# 轻量流量包用量缓存与展示

2026-11-13，对应 F4.1/F4.3 的只读展示。已接入官方总量、已用、剩余、超额字段的采集、持久缓存和 Instance.swas_traffic_package；sha-pro 的真实空包响应已完成新代码端到端落库验证。带固定额度及超额的正例仅用官方字段说明和模拟响应验证，未购买套餐或制造超额。阈值告警、额度保护和精确重置时刻仍未完成。

## 官方来源与数据边界

[ListInstancesTrafficPackages](https://help.aliyun.com/zh/simple-application-server/developer-reference/api-swas-open-2020-06-01-listinstancestrafficpackages)及[官方元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/ListInstancesTrafficPackages/api.json)确认四个字段是 Byte 整数，查询当月周期，方向为公网出网。入网不计费。[计费项说明](https://www.alibabacloud.com/help/en/simple-application-server/product-overview/billable-items)说明固定配额在自然月第一天重置；部分套餐没有固定流量配额。

接口没有返回账期起止、统计截止时间、更新时间或下次重置时间戳。此次查阅没有确认准确重置时区，所以响应只给 reset_day=1；next_reset_at 与 official_as_of 保持 null。业务显示时区不会成为官方计费时区，fetched_at 是本机收到查询结果的时间。

四个数字按提供方字段原样保存和返回：TrafficPackageTotal → total_bytes，TrafficUsed → used_bytes，TrafficPackageRemaining → remaining_bytes，TrafficOverflow → overflow_bytes。不重算剩余，不把超额再加进已用，不推算百分比、费用或月末预测。

元数据没有声明剩余值的非负下限，因此保留有符号 remaining_bytes，不将负值钳为零；真实超额后的字段关系仍缺正例。其他三个计数要求非负。所有值限于可精确表示的 JSON 整数范围 ±(2^53−1)，越界/小数/缺字段会使该批次失败，不转换成零或“无包”。这项数值上限和十分钟新鲜度是实现边界，未修改 PRD。

## 采集与事务

复用已有发现任务和固定读取操作，不新增云请求种类或 RAM 权限。继续每批最多 50 个实例；每次调用前检查账号租约/配置，验证返回 ID 属于当前批次、没有重复，四个数字完整。非法响应使整批未知；其他批次的成功结果独立保留。原有账号失败/权限汇总继续接收固定错误码。

成功空列表或省略某个 ID，以 entry_present=false、usage=null 记录实际列表成员情况；该字段不单独证明套餐版本。sha-pro 继续沿用已验证的 swas_no_traffic_package 展示。明确返回四个零的行则为 entry_present=true，保留真实零值。API 失败没有成功成员结论。

迁移 041 增加 swas_traffic_cache，键为实例、凭据版本和本次发现记录的套餐 ID。实例快照与缓存在 SaveScan 的同一个任务/账号租约事务内写入；目标账号、实例类型与套餐 ID 必须匹配，失败整体回滚。旧凭据和旧套餐的记录保留，不用于当前身份。

同一已知套餐查询失败时，只更新尝试时间/错误码，保留原成功值和抓取时间；成功空结果会清除旧 usage。套餐 ID 未知时，失败不沿用无法绑定的旧用量。乱序的旧尝试不会覆盖较新的缓存。上述身份是同轮发现的本地证据，不表示云侧多接口事务或精确对账水位。

## 只读接口与前端对接

先修改 OpenAPI 并记录顶部变更，再实现代码。现有 GET /api/v1/instances 每页最多一次缓存查询，为 SWAS 叠加新对象，ECS 为 null。查询目标取本页的套餐 ID，绑定当前凭据版本；没有同步云调用或新任务。

status 为 never_fetched / ok / unavailable / stale / disabled。成功值超过十分钟或来自未来时标 stale；账号停用或移除该地域为 disabled，保留同一身份的已知历史。unavailable 可以附带旧值，界面需同时显示 fetched_at 与 last_error。updated_at/last_attempt_at 是最近尝试时间，不替换成功时间。

父级 billing_subject_kind 仍供既有套餐分类使用；新对象中的 entry_present 是查询证据，usage=null 不代表四项都为零。没有用此对象启用告警、停机、CDT 估算或实时保护。开发模式为 sha-pro 示例提供空包状态，仍不需要真实账号。

数据在加密数据库备份中保留；跨主密钥恢复保持原用量和抓取时间，恢复后的陈旧数据不会被标成新鲜。缓存使用凭据版本隔离，但这些用量本身不是密钥。web/ 与 docs/design/ 未修改。

## 真实只读运行

创建仓库外新生产测试库，导入既有 M0 加密凭据（只在内存解密后重新加密），独立主密钥位于数据目录外。仅在这个临时库停用非目标账号，让 run-once 只采集 winnemjw 已授权的上海地域。没有修改原验证库、阿里云配置或真实资源。

实际启动参数：

```text
go build -o /Users/jasperxzy/.local/share/harbormaster-m0/swas-traffic-live-c44w81tc/hm-server ./cmd/hm-server
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/swas-traffic-live-c44w81tc.key HM_DEV=false HM_INSTANCE_POWER_ENABLED=false <上述二进制> import-accounts --config /Users/jasperxzy/.local/share/harbormaster-m0/swas-traffic-live-c44w81tc/config.json
account_import_complete: imported=2 skipped_existing_names=0 cloud_queries=0 cloud_writes=0
HM_MASTER_KEY_FILE=<同一外部密钥路径> HM_DEV=false HM_INSTANCE_POWER_ENABLED=false <上述二进制> run-once --config /Users/jasperxzy/.local/share/harbormaster-m0/swas-traffic-live-c44w81tc/config.json
run_once_complete: cloud_writes=0
```

构建和两条 CLI 命令均 exit 0。包装器仅输出固定日志字段，主密钥不进命令值或日志。已确认执行环境没有 HM_DATA_DIR/HM_CONFIG 覆盖临时目录。测试库和配置目录 0700、配置与密钥文件 0600，二进制已移除，保留加密测试库和脱敏 result.json。

实际缓存：1 行，entry_present=false、usage=null、last_error=null，fetched_at=last_attempt_at=2026-11-13T08:23:26.876685000Z，实例分类 swas_no_traffic_package；foreign_key_check 为 0 行。仅将[无资源身份或凭据的结果摘录](swas-traffic-runtime-evidence.json)纳入仓库。这个空包验证不替代固定额度/超额/重置边界的真实验收。

## 工程验证

实际专项检查，均 exit 0：

```text
go test ./internal/cloud/aliyun ./internal/store ./internal/api ./internal/devmode ./internal/backup -run 'TestSWASTraffic|TestDevelopmentContractAndSessionBoundary|TestRestoreWithNewMasterCancelsOldWorkAndPreservesHistory' -count=1
ok  github.com/JasperXzy/harbormaster/internal/cloud/aliyun  1.564s
ok  github.com/JasperXzy/harbormaster/internal/store         1.655s
ok  github.com/JasperXzy/harbormaster/internal/api           1.692s
ok  github.com/JasperXzy/harbormaster/internal/devmode       2.063s
ok  github.com/JasperXzy/harbormaster/internal/backup        1.719s
```

覆盖字节精度、有符号剩余、异常/重复/跨批 ID、失败隔离、租约前置、空包与零值、缓存失败保留/确认清除/乱序拒绝、套餐和凭据隔离、停用历史、事务回滚、GET 不查询/不改时间、开发数据，以及加密备份换主密钥恢复。原 V6 的轻量元数据预绑定结论不变；套餐保护和官方重置时区仍需继续验证。

最终全量检查，均 exit 0：

```text
go build ./...       # 无输出
go vet ./...         # 无输出
go test -race ./...  # 全部包通过
ok  github.com/JasperXzy/harbormaster/internal/api           118.842s
ok  github.com/JasperXzy/harbormaster/internal/backup         44.602s
ok  github.com/JasperXzy/harbormaster/internal/cli            19.677s
ok  github.com/JasperXzy/harbormaster/internal/cloud/aliyun    4.439s
ok  github.com/JasperXzy/harbormaster/internal/collect        13.642s
ok  github.com/JasperXzy/harbormaster/internal/devmode        14.130s
ok  github.com/JasperXzy/harbormaster/internal/store         160.867s
npx --yes @redocly/cli lint docs/api/openapi.yaml
Your API description is valid. # 84ms，6 条原有警告
gofmt -l cmd internal          # 无输出
git diff --check -- internal docs/m0 docs/api/openapi.yaml # 无输出
```

PRD 修订建议继续沿用 V6：第 6.1 节明确出网方向，F4.3 将自然月重置规则与实际可验证的时间戳分开。要完成固定配额正例，需要已有此类轻量实例的 ID、已授权地域及只读访问，以及超额前后/跨月的脱敏响应或用量明细。当前账号凭据无需重新导入；不会为验收新建收费资源或制造超额。

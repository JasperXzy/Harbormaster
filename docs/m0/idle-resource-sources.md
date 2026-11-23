# 闲置计费资源的只读来源验证

2026-11-13，补充 PRD F9.4。状态：实例、磁盘及 EIP 的只读字段/权限通路已确认；当前样本没有闲置正例，生产发现、告警与月度费用估算未实现。估算口径已提问，尚未收到选择。本批没有改变面板契约或生产扫描器。

后续更新（2026-11-14）：[释放影响预览](ecs-release-preview.md)已把固定 DescribeDisks 及所需影响字段接入后台采集/RAM 草稿。本页的“生产发现未实现”指闲置候选检测；释放缓存没有闲置计费判断、报价或浪费估算，不能据此标记 F9.4 已交付。

再后续：[闲置候选与缓存](idle-resource-candidates.md)已复用这些读取实现后台状态候选和只读 API，unknown 行保留旧证据；报价、月估算及告警尚未实现。本页的原始 M0 方法/命令与历史结果保留，不把它们解释为后续全部功能验收。

## 方法与官方依据

新增固定 M0 检查 `--check idle-resources`，对已导入账号的地域依次读取 DescribeInstances、DescribeDisks、DescribeEipAddresses。可用现有 `--region` 缩小范围，不能绕过账号地域限制。磁盘只允许 RegionId、MaxResults、NextToken；不开放任意 RPC、端点、过滤条件或 DryRun，更不允许解绑、停机或释放。

- [DescribeDisks](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describedisks)及[官方元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeDisks/api.json)：RegionId 必填，RAM Action 是 `ecs:DescribeDisks`。使用顶层 NextToken / MaxResults，默认取 100 条、最多 50 页；异常/重复游标及页数耗尽均不算完整。**该分页方式下 TotalCount 无效**，不得按它为 0 推断没有磁盘。Size 的单位是 GiB。
- 磁盘不能仅凭 InstanceId 为空就判闲置：多重挂载云盘的该字段始终为空，应结合 Status、MultiAttach 与 Attachments；旧共享盘还有 MountInstances。Category 中含本地盘/临时盘，不能全部按独立云盘收费。DiskChargeType、锁定状态和关联实例状态需要分别判断。
- [DescribeInstances 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstances/api.json)提供 StoppedMode、InstanceChargeType、SpotStrategy、Status。[按量付费说明](https://help.aliyun.com/zh/ecs/pay-as-you-go-1)区分普通停机与节省停机的计费项；节省停机不是所有资源免费，磁盘等费用仍可能存在。运行中的实例返回 StoppedMode 不等于观察到了停机后的账单行为。
- [DescribeEipAddresses 元数据](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/DescribeEipAddresses/api.json)提供绑定状态/对象、ChargeType、共享带宽、IP 地址池与托管状态。[EIP 按量计费说明](https://help.aliyun.com/zh/eip/pay-as-you-go/)说明未绑定也可能有配置费，但 IP 地址池、自带 IP、线路及计费方式存在差异；不采用“每个未绑定 EIP 统一单价”的推断。
- [DescribePrice](https://help.aliyun.com/en/ecs/api-describeprice)可查询按量云盘小时报价，查询配置有条件约束；它不是历史实付账单，也不能单独给出全部 EIP/实例的净浪费。本次没有新增或实际调用报价接口。

## 实际结果

使用已导入的两个只读账号；jasperxzy 为上海、新加坡、香港，winnemjw 为上海。最终检查时间 2026-11-13T06:20:12Z–06:20:16Z，12/12 项 `passed`、`complete=true`，全部各 1 页，cloud_writes=0。

| 账号 / 地域 | ECS | 磁盘 | EIP |
|---|---|---|---|
| jasperxzy / cn-shanghai | 1，Running / PrePaid | 1，In_use / system / PrePaid / cloud_essd_entry | 0 |
| jasperxzy / ap-southeast-1 | 1，Running / PostPaid / SpotAsPriceGo | 1，In_use / system / PostPaid / cloud_essd | 1，InUse / PostPaid / PayByTraffic |
| jasperxzy / cn-hongkong | 1，Running / PostPaid / SpotAsPriceGo | 1，In_use / system / PostPaid / cloud_essd | 1，InUse / PostPaid / PayByTraffic |
| winnemjw / cn-shanghai | 0 | 0 | 0 |

三块盘 MultiAttach 均为 Disabled，两条 EIP 为非托管、Normal。这里只列这三种 ECS/VPC 资源；winnemjw 的三项为空不表示它没有轻量服务器。字段频数与逐项时间见[无身份字段的摘录](idle-resource-evidence.json)，不含资源 ID、名称、IP、账号数字 ID、AccessKey 或游标。

首次尝试中，三块磁盘有返回行而 TotalCount=0，旧通用完整性检查将它们标为 pagination_incomplete。核对官方关于无效 TotalCount 的说明后，仅对 DescribeDisks 的游标分页取消此总数比较，保留 BSS 与规格目录原有校验，并在测试通过后重新查询。首次失败不作为最终完整性证据。

本次没有停机实例、Available EIP、多重挂载、独立按量数据盘或真实多页样本。不能为验收制造这些资源或改变状态；当前空闲置样本不能证明月估算、状态变化告警或全部计费例外已验证。

## 结论、修订建议及待确认

建议 PRD 第 7 节补充 DescribeDisks 及真实 RAM Action；F9.4 区分“当前资源状态的闲置候选”“按当前配置推算的参考费用”和“历史已产生的闲置费用”。本次没有修改 PRD 或自动扩大生产 RAM 模板；实际接入生产磁盘采集时应同步更新最小只读模板及权限状态。

已向用户询问月估算是否按“当前状态与配置保持整月”计算：已验证的官方按量小时报价 × 业务时区当月小时数，显示报价时间；价格或资格不可靠时为未知。也可以选择历史日均费用推算，但账单归属、折扣、退款与停机前后分段需要另一套证据，不能把当前月账单简单当作浪费。等待答案期间只验证资源字段，不实现依赖该选择的估算。

要完成真实正例验收，需要已有的未绑定 EIP、停机且仍计费的实例/磁盘及对应只读账单；若现有账号无此资源，应保留未验证状态。无需重新导入当前凭据，本批没有申请额外权限或执行任何真实写操作。

## 实际命令与输出

由本地 Python 包装器创建仓库外 0700 临时目录、编译二进制，再并行执行以下两个账号查询。命令实际参数为：

```text
go build -o /Users/jasperxzy/.local/share/harbormaster-m0/idle-resources-verified-_lay4jbg/hm-server ./cmd/hm-server
/Users/jasperxzy/.local/share/harbormaster-m0/idle-resources-verified-_lay4jbg/hm-server m0 query --account jasperxzy --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key --check idle-resources --output /Users/jasperxzy/.local/share/harbormaster-m0/idle-resources-verified-_lay4jbg/jasperxzy.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}
/Users/jasperxzy/.local/share/harbormaster-m0/idle-resources-verified-_lay4jbg/hm-server m0 query --account winnemjw --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key --check idle-resources --output /Users/jasperxzy/.local/share/harbormaster-m0/idle-resources-verified-_lay4jbg/winnemjw.json
{"account":"winnemjw","cloud_writes":0,"status":"report_written"}
```

三条命令均 exit 0，报告文件 0600；已清除本批两个临时二进制，保留私有报告。工程检查：

```text
go test ./internal/m0 -count=1
ok  github.com/JasperXzy/harbormaster/internal/m0  0.495s
go test -race ./internal/m0 ./internal/cli
ok  github.com/JasperXzy/harbormaster/internal/m0   1.466s
ok  github.com/JasperXzy/harbormaster/internal/cli 15.139s
go build ./...                             # exit 0，无输出
go vet ./...                               # exit 0，无输出
gofmt -l internal/m0                       # exit 0，无输出
git diff --check -- internal/m0 docs/m0     # exit 0，无输出
```

新增测试覆盖两地域签名读取、磁盘多页、无效 TotalCount、非法/重复/无限游标、报告脱敏和写动作/多余参数在联网前拒绝。全量 race 已在前一[延迟清理阶段](latency-retention.md)通过，本批仅影响 M0/CLI，未重复无关生产包的测试。面板契约未改，无需重新运行契约 lint；web/ 与 docs/design/ 未修改。

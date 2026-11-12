# V1：接口、参数与 RAM 权限

**状态：需要真实环境验证（部分已完成）。** 具名接口公开定义已核对；主要资源发现、两账号 BSS/CDT 和 jasperxzy CMS 读取已实测通过。其余接口、国际站账号及写权限未验证。

## 首轮实测更新

见[2026-11-11 实测](live-verification-2026-11-11.md)。ListCdtInternetTraffic 已通过官方通用 SDK 的固定只读探测取得真实响应；它仍未出现在所查公开产品 SDK/元数据中，不因此承诺接口稳定性或水位。DescribeCommonBandwidthPackages 的 PageSize 上限是 50，最初 100 导致 InvalidParameter，修正并重查后通过。四个 CMS 指标元数据已确认单位与统计字段。

下文方法与 SDK 缺口描述保留为初始文档核对记录；“未调用”仅指当时步骤，最新可用性以本节为准。账号凭据已提供，无需再次导入。

2026-11-12 补充：重新读取 14 个写接口的官方 RAM Action/资源元数据，生成三档[策略草稿与复制接口](ram-policy.md)。这确认了模板所列的 Action 名称与实例 ARN 模式，不证明真实身份拥有写权限，也不证明 M4 创建链路的全部关联授权已经齐全。

## 验证方法

2026-11-11 下载阿里云官方 Go SDK，逐项查找 Client 方法；读取官方 OpenAPI 元数据的 parameters、operationType、ramActions。没有向真实账号发送请求。31 项结果、官方链接和固定 SDK commit 见 [接口矩阵](api-table.md)；全部顶层参数名、类型、必选标志及 RAM 资源范围见 [机器可读记录](api-evidence.json)。required=true 不是场景全部必选条件。

## 实际结果

- ECS：PRD 具名方法均存在。实例列表按 RegionId 分页，可用 NextToken/MaxResults；InstanceIds 是 JSON 数组字符串。实例响应提供 PublicIpAddress、EipAddress、计费、到期信息；网卡查询补 IPv6；规格提供 InstanceBandwidthRx/Tx，不能将规格网络上限理解为公网保证带宽。
- ECS 操作：InstanceId；停止时显式设置 StoppedMode 和 ForceStop。StartInstance、StopInstance 有 DryRun 参数，本轮没有调用。RunInstances 的镜像、规格、网络、安全组、磁盘有组合/条件约束，不能因元数据仅标 RegionId 必选就认为其他参数不需要。
- VPC：列表按 RegionId 分页；EIP 用 AllocationId、InstanceId/InstanceType 关联。共享带宽返回 Bandwidth、InternetChargeType、CreationTime、PublicIpAddresses。CreateVSwitch 的 ZoneId/VpcId 以外还要按 IPv4/IPv6 场景核对地址段。创建链路所需关联权限需在 M4 按实际方案补齐。
- SWAS：使用实例所在 RegionId；ListInstancesTrafficPackages 的 InstanceIds 是 JSON 数组字符串。响应含总量/余量/已用/超额（Byte），没有统计方向与截止时间字段。空列表和调用失败必须区分。
- CMS：DescribeMetricList 使用 Namespace、MetricName、Dimensions、StartTime、EndTime、Period，Datapoints 是 JSON 字符串。实际 RAM Action 是 **cms:QueryMetricList**。PRD 的 EIP/共享带宽指标应另核对统计量、单位和采样周期，rate 不能直接当累计字节。
- BSS：BillingCycle=YYYY-MM，QueryInstanceBill 有 Granularity、BillingDate、PageNum/PageSize。余额权限实际为 **bss:DescribeAcccount**（三个 c），账单为 **bss:DescribeBillList**。汇总账单不等同实时流量水位。[端点表](https://www.alibabacloud.com/help/en/user-center/developer-reference/api-bssopenapi-2017-12-14-endpoint)与 SDK 确认 business.aliyuncs.com / business.ap-southeast-1.aliyuncs.com；按账号站点选择，不能按被查询实例地域选择。
- 元数据：ECS 的加固模式通过本机固定地址获取短期元数据令牌，再查询 instance-id；不需要账号 AccessKey。SWAS 的支持性仍待 V6。

### CDT 缺口

[官方操作审计目录](https://help.aliyun.com/zh/actiontrail/product-overview/audit-events-of-cdt)列出 ListCdtInternetTraffic，但核对的公开 CDT 2021-08-13 SDK 不含此方法；产品级元数据返回 `Product is not public`，单 API 元数据返回 `api not found`。这不能证明接口不存在，也不足以确认其参数、RAM 权限、返回粒度或稳定性。不能自行猜 `cdt:ListCdtInternetTraffic` 权限后宣布已验证。

### 可选云拨测

已确认 CMS 2019-01-01 的 DescribeSiteMonitorList、DescribeSiteMonitorData、CreateSiteMonitor；前两项读取已有任务，最后一项会创建任务，本轮不调用。[站点监控说明](https://help.aliyun.com/zh/cms/cloudmonitor-1-0/user-guide/site-monitoring/)确认支持 PING/TCP，使用可能收费。CMS 1.0、2.0 和 ARMS 云拨测应区分，M3 再按账号可用能力选定。

## 结论与 PRD 修订建议

1. 第 7 节补充 API 版本、真实 RAM Action、参数附录，不能按 API 名机械生成策略。
2. F1.2：只有服务明确支持且成功的 DryRun 才能证明相应预检通过；否则写权限应为“未验证”。只读查询成功不能证明写权限。M0 不执行写 API，包括未明确允许的 DryRun。
3. CDT 查询通路与统计水位是 M1 前置条件；在确认前不能承诺 F3.1/F3.3 的精确对齐。
4. 自动建网络的安全组等关联权限尚不是完整模板，留待 M4 方案确定。权限集合不含云助手。

## 需要提供

两个账号的站点、扫描地域、只读 RAM 凭据的安全本地来源；已有 CDT 查询响应或官方支持的查询说明。凭据放仓库外受限文件或环境变量，只告知位置，不在聊天粘贴值。只读 RAM 范围以矩阵为依据，CDT 未确认部分不以通配写权限替代。实际调用还需记录通过/缺权/未验证/检查失败及限流情况。

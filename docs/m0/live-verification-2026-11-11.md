# M0 首轮真实环境验证

2026-11-11。用户完成凭据导入后，使用本机加密凭据执行只读查询。用户另行同意将 jasperxzy 的地域列表更新为上海、新加坡、香港；winnemjw 仍只扫描上海。两账号配置的站点均为国内站，本轮 BSS 均返回 CNY；没有验证国际站账号。

完整脱敏响应存放在仓库外：

`~/.local/share/harbormaster-m0/verification-zuyr77j4/`

仓库只保存结论和复现方法，不复制 AccessKey、Token、账户余额、完整账单或资源标识。没有执行云资源写操作、DryRun、云助手、安装 agent 或修改 nftables 规则。SSH 只使用本机已有的对应主机配置和已信任主机密钥；未读取私钥内容。

## 查询覆盖与结果

| 账号 / 范围 | 结果 |
|---|---|
| jasperxzy：上海、新加坡、香港 | ECS 实例、网卡、EIP、共享带宽、轻量实例查询通过，结果完整翻页 |
| winnemjw：上海 | 同上；发现 sha-pro，后续流量包查询通过 |
| 两账号 BSS：2026-11 | 余额、账单概览、实例月度计费项查询均通过 |
| 两账号 CDT | ListCdtInternetTraffic 固定只读探测通过，取得 TrafficDetails |
| jasperxzy CMS | sg-share、hk-share 双向带宽，以及 sin-pro EIP 双向流量读取通过 |
| CMS 指标元数据 | 四个指标的 DescribeMetricMetaList 查询通过，确认单位与统计字段 |

这里只证明**本轮所调用的读取接口可用**，不证明这些凭据没有其他权限，也不证明任何写权限。未检查 RAM 策略内容；写权限状态仍为未验证。规格、系统事件、可选云拨测等尚未做真实调用。

最初共享带宽请求因 PageSize=100 返回 InvalidParameter。官方 [API 元数据](https://api.aliyun.com/meta/v1/products/Vpc/versions/2016-04-28/apis/DescribeCommonBandwidthPackages/api.json)确认上限 50，工具已修正为 50，同时修复“未返回 TotalCount 时的翻页终止条件”，回归测试通过后重查成功。不能将这次参数错误归因为凭据无效。

## 实例与计费对象

| 用户别名 | 云端名称 | 已确认的公网出口 | 计量含义 |
|---|---|---|---|
| sin-pro | sg-server-nano | EIP → sg-share；BGP，2000Mbps，PayByDominantTraffic | 按共享带宽小时内合并入出再取 max |
| hkg-pro | hk-server-nano | EIP → hk-share；BGP，2000Mbps，PayByDominantTraffic | 同样按主流量，不能当普通 EIP 只计出方向 |
| sha-pro | sha-pro | 轻量应用服务器，上海，Running；流量包响应成功且数组为空 | 与 PRD 的“无流量包”背景一致；不参与 CDT |
| 上海 ECS | Ali_ShangHai | 实例公网 IP，PayByBandwidth，3Mbps，包年包月 | 固定带宽，不参与 CDT 实时增量 |

SSH 别名 sin-pro / hkg-pro / sha-pro 的 HostName 分别与对应云端资源公网地址一致。hkg-pro 的映射并非仅凭云端名称推断。

sg-share 当前 1 个成员 EIP，创建时间 `2026-11-11T03:18:47Z`；hk-share 当前 1 个成员 EIP，创建时间 `2026-09-26T11:15:03Z`。另发现上海 sh-share：同为 2000Mbps、按主流量，当前成员列表为空。空成员列表不等于当月累计流量为零，CDT 响应里仍存在上海共享带宽的历史累计值；归因不能只用当前拓扑回算历史。

本轮仅有单成员真实数据，多个成员合并后取 max 的规则依据仍是官方文档，没有额外添加成员做实验。

## CDT 来源与水位：V1 / V3 的新证据

公开 SDK 缺少该方法的结论仍成立，但“接口可访问性待定”已取得进展：在 PRD 指定的 [CDT-Monitor 原始实现](https://github.com/wang4386/CDT-Monitor/blob/main/internal/aliyun/client.go)中定位到实际请求方式；[官方操作审计目录](https://help.aliyun.com/zh/actiontrail/product-overview/audit-events-of-cdt)把该 Action 定义为查询累计公网用量。

据此仅增加固定 `ListCdtInternetTraffic` 探测：CDT 2021-08-13、cdt.aliyuncs.com、服务 RegionId=cn-hongkong、RPC POST/HMAC-SHA1，由官方通用 Go SDK 签名。POST 是这个读取操作的传输方法，不是开通或改配。没有照搬参考项目的其他操作，也未修改 RAM 权限。国际站端点未确认，工具暂拒绝国际站 CDT 探测。

两账号于 `2026-11-11T15:32:00Z` 读取成功。实际响应只有 RequestId 与 TrafficDetails，后者按 BusinessRegionId、ISPType 划分，含 ProductTrafficDetails 和 TrafficTierDetails。jasperxzy 返回上海、新加坡、香港；winnemjw 返回香港累计 0 的记录。数据可按地域和产品类别分解，但没有实例级流量列表。

**响应没有账期、采集截止时间或更新时间字段。** 抓取时间不是数据水位，不能自动填进 official_as_of。单次查询也不能测出官方延迟或补账规律。因此 V3 对“可按官方 cutoff 精确拼接 agent 增量”的假设需要修订，不能据此宣布 F3.3 或 24 小时 5% 验收成立。

TrafficTierDetails 的第一档上限为 `10995116277760`，数值等于 `10 × 1024^4`，与公开“10TB 第一阶梯”和参考项目按字节处理相符；这是支持 1024 换算的证据，响应本身没有单位、免费量是否包含等说明，仍需控制台/计量明细对照。不能把参考项目的除法当作官方计费承诺。

BSS 也不能直接补上水位：jasperxzy 当前 CDT 商品为 `cdt_DataTransfer_public_cn`，取得的 8 条 CDT 月度计费项 Usage 均为字符串 `0`，而 CDT 查询有非零 Traffic。winnemjw 概览中仍出现旧商品 `cdtinternet`。此外，新商品下返回的 InstanceID 仍有复合格式，不能只按“CDT 2.0”标签假设 ID 必为纯资源 ID。月账单字段应按实际 schema 处理，不能将 Usage=0 误认为账号没有流量。

## CMS 指标口径

由真实 DescribeMetricMetaList 响应确认：

| namespace / metric | Unit | Statistics | Periods |
|---|---|---|---|
| acs_vpc_eip / net.rx、net.tx | B | Average, Minimum, Maximum, Sum | 60, 300 |
| acs_bandwidth_package / net_rx.rate、net_tx.rate | bit/s | Value | 60, 300, 600 |

共享带宽速率转字节必须包含除以 8 和时间积分；不能把 bit/s 直接加到 Byte 用量上。EIP 指标与共享带宽指标形态不同，不能共用一套盲目取 Average 的解析器。

本轮一小时查询返回每方向 58 个共享带宽点、59 个 EIP 点。最后样本标签分别为 15:29Z / 15:30Z，抓取在 15:32Z 左右，相差约 180 / 120 秒。这里只记录返回样本的时间标签差，不将一次观察写成延迟 SLA，也不把这些辅助指标用于额度保护。

## 机器与元数据：V6 / V7

- sin-pro：Linux `6.12.73+deb13-cloud-amd64`，nftables 1.1.3，网卡 lo/eth0；加固模式元数据读取成功，实例 ID 和地域与云 API 一致。
- sha-pro：Linux `6.12.107+deb13-cloud-amd64`，nftables 1.1.3；没有 python3，因此切换已有 curl 做只读检查，未安装软件。加固模式 token 请求成功（未输出 token），普通模式 instance-id 查询成功。
- sha-pro 的元数据返回底层 `i-...` ECS ID，与 SWAS ListInstances 的 32 位实例 ID **不相同**。PRD F12.2 的直接匹配不适用于该机器。

用户已明确同意：**轻量注册码预绑定已纳管 SWAS 实例 ID，元数据 ID 仅作辅助信息**。本轮只记录方案，未实现或安装注册流程；注册码的保密和可信安装仍是信任边界，元数据文本不能作为远程身份的防伪证明。

V7 还没有启动 24 小时计数。此次没有添加计数表、改动防火墙、制造业务流量、断网或重启；5% 偏差、内存与 CPU 指标均未验证。

## PRD 修订建议与后续边界

1. 第 6.1 节补充 hkg-pro → EIP → hk-share，按主流量；前端示例不能误标为普通出向计费。
2. F12.2 对 ECS 与 SWAS 分支说明，按上述已同意方案修订轻量绑定。
3. 第 6.2 节明确目前官方来源没有可信 cutoff；需要另行设计保守对齐/数据质量策略并确认，不能未经验证启用自动保护。
4. CMS 指标注明 B 与 bit/s、取值字段与时间积分方式；辅助数据不提升为保护依据。
5. V7 的真实计数安装与 24 小时窗口留待下一步；V9 继续等待具体停开机授权，本轮不执行。

PRD 原文件未修改。代码仅增加本地 set-regions、修正分页、补充两个固定只读探测（CDT 和 CMS 指标元数据）；无面板契约字段变更，无前端目录修改，无提交或推送。

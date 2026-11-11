# V8：hkg-pro 公网 IP 与共享带宽归属

**状态：已确认。**

## 首轮实测结果

2026-11-11 已用 jasperxzy 的真实凭据查询香港实例、EIP、共享带宽，三者关联一致；本机 hkg-pro SSH 别名的公网地址也与该实例匹配。

- 云端名称 hk-server-nano，Running、PostPaid，实例本身无公网带宽且 PublicIpAddress 为空。
- 公网出口是 EIP，已加入 hk-share；共享带宽为 2000Mbps、BGP、PayByDominantTraffic，当前成员为该 EIP。
- 应按共享带宽每小时总入/总出取大计量，不能使用普通 ECS/EIP 的 outbound_only 规则。
- CDT 查询存在香港 cbwp 产品累计值；BSS 有该共享带宽对应的 CDT 商品计费项，支持其由 CDT 结算的结论。

第 6.1 节的待核对描述可补齐上述拓扑。完整响应存仓库外，记录见[首轮实测](live-verification-2026-11-11.md)。下文保留原验证方法与条件说明，这些条件现已满足。

## 方法与实际结果

已通过 V1 确认 DescribeInstances、DescribeEipAddresses、DescribeCommonBandwidthPackages 的 SDK 方法和关联字段。未获得真实账号凭据，没有查询到 hkg-pro 的实际响应。PRD 的“jasperxzy、香港、按流量、计入 CDT”作为用户提供的背景保留，不作为本轮独立验证结果。

## 待执行步骤

在 cn-hongkong 只读查询指定实例 ID（不能只凭名称，重名时由用户确认）。比对 PublicIpAddress 与 EipAddress.AllocationId，再按 EIP 的实例/网卡绑定和 BandwidthPackageId 追踪共享带宽，完整分页；必要时补 DescribeNetworkInterfaces。保留字段级证据与更新时间，检查是否同时有多个公网出口。

## 结论与 PRD 修订建议

现阶段无结论可替换第 6.1 节的待核对项。按流量 ECS/EIP 的出向规则与共享带宽主流量规则必须由真实拓扑选择，查询失败不能默认为未加入共享带宽。

## 所需条件

jasperxzy 的只读 ECS/VPC 权限、hkg-pro 实例 ID 或能唯一匹配的账号/地域/名称。无需登录实例或执行写操作。

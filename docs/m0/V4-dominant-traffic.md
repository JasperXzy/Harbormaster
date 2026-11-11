# V4：共享带宽按主流量

**状态：与 PRD 不符（小时合并规则已确认；CDT 资格判断需修订）。**

## 方法与实际结果

查阅[按主流量计费](https://help.aliyun.com/zh/internet-shared-bandwidth/product-overview/pay-by-dominant-data-transfer)与[计费概述](https://help.aliyun.com/zh/internet-shared-bandwidth/product-overview/billing-overview/)。计量对象是整个共享带宽实例，每小时比较总入、总出字节，较大者为该小时用量。文档针对“共享带宽实例”的规则支持先合并成员再比较；未对真实多成员账单做复算。

正确的本地计算：`sum_over_hours(max(sum_members(rx), sum_members(tx)))`，不能对每台成员先取 max，也不能每 10 秒取 max 后相加。成员加入/离开需按有效时间归属。

## 结论与 PRD 修订建议

第 6.2 节小时周期可去掉待验证标记。第 6.1 节不能仅按“此实例 2024-12-17 后创建”判断 CDT：官方写的是**账号首次创建**条件。[切换公告](https://help.aliyun.com/zh/internet-shared-bandwidth/product-overview/announcement-on-switching-shared-bandwidth-to-cdt-billing-mode-by-traffic)还记载存量账号升级计划，和主流量说明的存量表述有差异，实际采用账号已升级状态和账单，不按日期推断。

CDT 通用页面“仅出方向”的表述与共享带宽主流量专门说明不完全一致：按计费对象使用专门规则，并在 V7 以真实账单确认 sin-pro，不将普通 ECS/EIP 规则覆盖共享带宽。

## 所需条件

通用计量规则已确认；需只读 DescribeCommonBandwidthPackages、DescribeEipAddresses 与 CDT/BSS 账单确认 sg-share 的真实成员、升级状态及小时对齐。多成员情况下需各成员同窗口数据；本轮不添加成员或制造流量。

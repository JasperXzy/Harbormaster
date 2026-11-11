# V5：增强型 95 的方向与峰值

**状态：已确认（官方规则）。**

## 方法与实际结果

核对[增强型 95 计费说明](https://help.aliyun.com/zh/internet-shared-bandwidth/product-overview/pay-by-enhanced-95th-percentile)和[Describe95Traffic](https://help.aliyun.com/zh/internet-shared-bandwidth/developer-reference/api-vpc-2016-04-28-describe95traffic-brandwidths)。每 5 分钟计算入、出方向平均带宽并取较大值。计费说明的日峰值取每日第 5 高采样点，月峰值为最高 5 个日峰值的均值。API 的 BillBandwidth 对应同一采样区间较大的方向，另有 InBandwidth/OutBandwidth。

## 结论与 PRD 修订建议

第 6.1 节的方向问题可确认，写为“5 分钟平均带宽入出取大，再按增强型 95 规则计算峰值”；不要把它实现成字节 max 或普通全月 95 分位。它不参与免费公网流量额度估算。

## 所需条件

回答本项方向问题无需真实账号。若要验证某个实际 95 实例的账单，需已有实例 ID、上一完整月份的只读统计；API 对当月数据及已接入 CDT 的实例存在限制，不为验证而新建实例。本轮未做实例账单复算。

# V6：轻量流量包与元数据

**状态：与 PRD 不符（轻量元数据 ID 不能直接匹配 SWAS ID）。**

## 首轮实测更新与用户决定

2026-11-11，winnemjw 的 SWAS API 成功发现运行中的 sha-pro，ListInstancesTrafficPackages 成功返回空数组。通过已有 SSH 配置只读检查：元数据令牌接口返回 200，instance-id 可读，但得到底层 `i-...` ECS ID，与轻量 API 的 32 位实例 ID 不同。因此 F12.2 的“直接匹配实例 ID”不能照搬。

用户已明确同意：一次性注册码预绑定已纳管 SWAS 实例 ID，元数据 ID 仅作辅助信息。本轮不安装 agent、不实现注册。见[实测记录](live-verification-2026-11-11.md)。下文保留初始资料核对；机器登录与 API 权限已确认，无需再次提供。

2026-11-13 补充：已接入流量包四项原始字节字段的持久缓存和只读展示，在新的隔离生产库中用真实 sha-pro 空响应完成落库验证。固定配额/超额正例仍缺真实环境；没有据字段说明推测重置时区或启用保护。[缓存、边界与命令输出](swas-traffic-package.md)。

## 方法与实际结果

[ListInstancesTrafficPackages 文档](https://help.aliyun.com/zh/simple-application-server/developer-reference/api-swas-open-2020-06-01-listinstancestrafficpackages)明确只计算公网出方向；入方向不计费。SDK 响应有 Byte 单位的累计值，没有“方向”字段。[计费项说明](https://www.alibabacloud.com/help/zh/simple-application-server/product-overview/billable-items)说明自然月 1 日重置，且有无固定流量配额套餐。

查到的[元数据协议](https://help.aliyun.com/zh/ecs/user-guide/view-instance-metadata/)明确针对 ECS；没有找到足以确认 SWAS 支持相同 instance-id 且与 SWAS API ID 一致的官方保证。没有登录 sha-pro，不把 ECS 的底层实现当作 SWAS 的承诺。

## 结论与 PRD 修订建议

第 6.1 节方向改为出方向，删除“以接口返回方向为准”。F12.2 的 SWAS 自动匹配需实测；失败时不得伪造 instance-id 或绕过纳管校验。无流量包由成功查询和套餐信息共同判定；接口失败不能显示为“无流量包”。PRD 提供的 sha-pro 状态仍是用户背景，未独立实测。

## 所需条件与步骤

sha-pro 登录方式（SSH 主机别名、用户名、仓库外密钥路径）、winnemjw 只读 SWAS 访问与地域。先在机内以短超时访问固定元数据地址，按 ECS 加固模式获取临时令牌后读取 instance-id/region-id；不请求 RAM 凭证目录、不记录元数据令牌。和 ListInstances 的实际 ID 比对。若不支持，请用户决定是否采用管理员显式绑定的一次性安装凭据，之后再实现注册分支。

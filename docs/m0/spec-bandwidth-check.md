# 实例规格带宽检查

2026-11-12，PRD F11.1 的只读后端实现。实例刷新后，`Instance.network_capacity` 提供规格网络能力和已确认公网出口的出方向比较结果。面板 GET 只读 SQLite 快照；没有额外开关机、网络变配或 Telegram 发送。

## 来源和口径

- [DescribeInstanceTypes](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describeinstancetypes) 的 `InstanceBandwidthRx` / `InstanceBandwidthTx` 单位为 kbit/s，保存原值，不解释为测得吞吐。每批只查已发现的最多 10 个不同规格，按 NextToken 分页，不拉完整规格目录。
- [ECS 网络带宽说明](https://help.aliyun.com/zh/ecs/user-guide/network-bandwidth/) 说明单实例公网能力受规格网络带宽限制。当前比较取出方向字段；入方向规格值供展示，不能用购买的出方向带宽反推所有产品的公网入方向限额。
- [共享带宽说明](https://help.aliyun.com/zh/internet-shared-bandwidth/user-guide/create-an-internet-shared-bandwidth-instance) 说明成员 EIP 原峰值不再生效。因此复用已解析计费出口：实例公网、独立 EIP、共享带宽包取实际对应者；包峰值并非每台实例独享保证。

比较为 `public_outbound_mbps × 1000 > tx_kbps`，按接口声明的单位使用十进制换算；实现用整数除法比较，避免乘法溢出。相等不提示。零或缺失规格能力、未知拓扑、多计费出口、未返回对应规格时，比较结果为 null。查询失败带固定错误码，不返回原始 SDK 错误。SWAS 没有该规格来源，整个对象为 null。

`false` 只表示本次出方向比较没有发现配置超过规格，不代表所有网络检查正常、带宽有 SLA 或实际流量一定可达到该数值。当前没有推算突发积分、动态带宽权重或实际吞吐。建议 PRD F11.1 明确“提示配置能力不匹配”，并将按产品识别公网入方向限制列为独立判断条件。

## 采集和失败行为

生产资源扫描额外调用固定只读动作 `DescribeInstanceTypes`，复用账号地域限制、签名、限速、超时和账号版本检查。过滤参数仅开放固定的 InstanceTypes.1–10、MaxResults、NextToken。每次请求前检查任务租约及账号是否变化。

拒绝重复/非请求规格、分页无进展、循环 token 和无效响应，失败批次不作为完整目录发布。缺少规格权限会出现在 ecs_read 权限结果和账号采集错误中，但不使已成功读取的实例列表消失。快照不借用旧规格值伪造当前检查结果。每次后台资源扫描更新检查时间；不另引入全局缓存，持久快照即为前端查询缓存。

开发模式内置两个合成 ECS 不匹配示例和一个 SWAS 未知示例，仍标为 demo。前端只需按 OpenAPI 新增可选字段展示提示，中文文案由 Claude 决定。`web/`、`docs/design/` 未修改。

## 真实只读结果

使用先前已导入的加密账号及仓库外主密钥，对已有仓库外验收数据库运行：

```text
go build -o deploy/bin/hm-server-readonly-check ./cmd/hm-server
HM_MASTER_KEY_FILE=<已有仓库外主密钥文件> ./deploy/bin/hm-server-readonly-check run-once --data-dir <已有隔离验收目录>
{"time":"2026-11-11T21:46:50.421Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

随后从已关闭、无 WAL 残留的验收库只读提取下列非凭据字段。时间均为 UTC，规格检查完成于 2026-11-11 21:46:47–50，对应新加坡 11 月 12 日清晨。

| 实例 | 规格 | RX / TX（kbit/s） | 公网出方向配置（Mbit/s） | 超出规格 |
|---|---|---|---|---|
| sg-server-nano（sin-pro） | ecs.e-c4m1.large | 204800 / 204800 | 2000（共享带宽） | true |
| hk-server-nano（hkg-pro） | ecs.e-c4m1.large | 204800 / 204800 | 2000（共享带宽） | true |
| Ali_ShangHai | ecs.e-c1m1.large | 204800 / 204800 | 3（实例公网） | false |
| sha-pro | swas.s.c2m2s40b1.linux | 未知 | 不作 ECS 规格比较 | null |

三个 ECS 检查 `last_error` 均为 null。没有执行测速负载、修改带宽或实例操作。这次结果支持 F11.1 的实际必要性，也说明共享带宽的 2000 Mbit/s 不能直接当作单实例已保证的传输能力。

## 检查结果

```text
go build ./...                      exit 0（无输出）
go vet ./...                        exit 0（无输出）
go test -race ./...                  exit 0
  internal/cloud/aliyun             1.402s
  internal/api                      26.446s
  internal/cli                      2.681s
  internal/devmode                  5.324s
  其他有测试的包                    cached / passed
npm --prefix web run lint:api        Your API description is valid.
git diff --check                    exit 0
```

首次 OpenAPI 校验发现可空对象缺少 type，已补 `type: object` 后重新校验通过。针对性测试覆盖：规格去重与分批、分页 token、账号修改中断、未知/重复响应、SDK 签名及参数白名单、共享包覆盖 EIP 旧峰值、权限失败与拓扑未知、SWAS 不伪造数据、比较边界与缺失字段。

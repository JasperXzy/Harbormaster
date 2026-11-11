# ECS IPv6 发现与失败保留

2026-11-12，对应 F2.3。面板契约先新增可选 `Instance.ipv6`，然后实现采集和快照保留。前端仍由 Claude 负责，可单独按新契约展示。

## 来源与查询顺序

1. `DescribeInstances` 增加 `AdditionalAttributes.1=NETWORK_PRIMARY_ENI_IP`。读取 `NetworkInterfaces.NetworkInterface[].Ipv6Sets.Ipv6Set[].Ipv6Address`，合并主、副网卡，规范化、去重并排序。只有每个返回网卡的地址列表均完整有效，且存在地址时才使用这个结果。
2. 扩展字段缺失、空列表或不完整时，调用 `DescribeNetworkInterfaces`，限定当前 `InstanceId`，不设置 `Type`（覆盖主、副网卡）。使用 `MaxResults=100`、`NextToken` 翻页；整个回退查询共用 3 秒 deadline，覆盖限速等待与 HTTP，页间检查账号任务是否仍有效。
3. 拒绝重复网卡、其他实例的网卡、重复游标、不完整分页、缺失地址字段、错误地址或超时。失败不发布部分地址列表，也不将其当作“没有 IPv6”。显式空地址列表或完整的零网卡结果可确认地址集合为空。

官方依据：[DescribeInstances](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstances) 的实例扩展字段及 IPv6 返回结构；[DescribeNetworkInterfaces](https://help.aliyun.com/en/ecs/api-describenetworkinterfaces) 的 InstanceId、主副网卡默认范围与游标分页。没有新增 RAM Action；使用现有只读 ECS 权限。原有地域级网卡扫描仍用于 EIP 拓扑，和本功能按实例的 3 秒回退独立。

## 快照与 API

`ipv6` 字段含 `addresses`、`status`、`source`、`checked_at`、`observed_at`、`last_error`：

- `ok`：本次成功读取，地址集合可为空；观察时间更新。
- `stale`：本次失败，保留数据库中上次成功的整个地址集合、来源和观察时间，更新尝试时间与错误；即使连续失败或服务重启也不丢失。保留结果也可能是已确认的空集合。
- `unknown`：尚无成功结果；空数组不能解释成“没有地址”。

保留在 `SaveScan` 的账号/任务租约校验之后、同一 SQLite 事务内进行。账号停用或旧任务不能覆盖结果。沿用 JSON 资源快照，无需新增迁移；旧快照可以没有此可选字段。GET `/api/v1/instances` 只读快照。开发模式提供正常、过期两种合成 IPv6 示例。

这些是**网卡分配地址**，不是已验证可访问的公网地址；不将它们混入现有公网 IPv4 `public_ips`，不据此推断 IPv6 公网带宽、CDT 资格或保护范围。不展开委派前缀成主机地址。SWAS 当前没有经过核实的 IPv6 来源，返回 `null`。

## 验证

覆盖实例扩展优先、主副网卡合并、IPv6 规范化、按实例分页、3 秒真实 HTTP context 取消、查询错误、分页中途失败、地址错误、账号任务失效、数据库重启后保留旧值、确认无地址才清除，以及 API 返回快照不触发云调用。

实际运行的检查：

| 命令 | 结果 |
|---|---|
| `go build ./...` | 通过，无输出 |
| `go vet ./...` | 通过，无输出 |
| `go test -race ./...` | 全部通过；api 28.152s、cli 3.209s、cloud/aliyun 4.417s、collect 2.762s、devmode 5.523s、notify 1.536s、store 10.429s；其余包缓存通过或无测试文件 |
| `npm --prefix web run lint:api` | `Your API description is valid.` |
| `git diff --check` | 通过，无输出 |

真实环境只读命令（路径均为仓库外已有验证数据和密钥，不含密钥值）：

```sh
go build -o deploy/bin/hm-server-readonly-check ./cmd/hm-server
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/production-discovery-q8fh4ddr.key deploy/bin/hm-server-readonly-check run-once --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/production-discovery-4z0pnqrt
```

退出码 0，实际日志：

```json
{"time":"2026-11-11T23:30:19.773Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

进程退出、确认 WAL 文件已不存在后，以 SQLite `mode=ro&immutable=1` 查询持久化结果，仅输出实例名、状态、来源、地址数量和时间，不输出真实地址或凭据：

| 实例 | 结果 | 来源 | 地址数 | 观察时间 UTC |
|---|---|---|---|---|
| Ali_ShangHai | ok | ecs_instances | 1 | 2026-11-11T23:30:15.453852Z |
| sg-server-nano | ok | ecs_network_interfaces | 0 | 2026-11-11T23:30:17.111095Z |
| hk-server-nano | ok | ecs_network_interfaces | 0 | 2026-11-11T23:30:18.497762Z |
| sha-pro | null（未核实来源） | null | 未知 | 未知 |

实际验证覆盖了扩展字段与空集合回退两条路径。多页主副网卡、权限失败、重启保留及超时由自动测试验证，未改变真实网络来制造故障。分配地址的公网可达性、计费资格不在本次查询结论内。

## 前端对接与 PRD

前端可重新生成契约类型，单独展示 IPv6；对 `stale` 显示 `observed_at`，对 `unknown` 显示未知，不把整条实例的 `updated_at` 当作 IPv6 更新时间。没有修改 web/ 或 docs/design/。

无需修改 F2.3 的查询顺序及失败保留规则。建议在 PRD 补充“分配地址不代表已启用公网访问”，并将轻量 IPv6 数据源列为另行确认项；本轮不修改 PRD 原文。

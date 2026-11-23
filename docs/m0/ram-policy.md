# RAM 策略草稿

2026-11-13 增量：三档草稿的共用只读集合增加 ecs:DescribeDisks，用于后台释放影响预览；公开元数据声明该真实 RAM Action，支持磁盘资源类型。本次仍使用现有草稿的 Resource="*"，没有向实际身份附加权限，也没有新增写权限。[实现与边界](ecs-release-preview.md)。

2026-11-12。当前后端公开可核对的只读权限草稿位于 [deploy/ram/readonly.json](../../deploy/ram/readonly.json)。它没有开关机、续费、创建/释放资源、云助手或 RAM 管理权限。本轮只生成仓库文件，未向任何真实 RAM 身份授予权限。

原 m0-readonly.json 保留为初始 M0 范围的记录。新草稿在其基础上补充两个当前实际读取所需的动作：

| RAM Action | 用途 | 官方来源 |
|---|---|---|
| ecs:DescribeInstanceAutoRenewAttribute | 包年包月 ECS 续费策略 | [ECS 自动续费属性查询](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstanceautorenewattribute) |
| bss:DescribeInstances | 通过 QueryAvailableInstances 匹配轻量实例续费记录 | [BSS 可用实例查询](https://help.aliyun.com/en/user-center/developer-reference/api-bssopenapi-2017-12-14-queryavailableinstances) |

余额查询仍使用官方命名 bss:DescribeAcccount（三个 c），账单使用 bss:DescribeBillList，CMS 数据查询使用 cms:QueryMetricList；不能把 API 方法名机械地当作 RAM Action。其他既有来源见 [接口证据矩阵](api-table.md)。

该 JSON 是已知动作集合，不是“完整且已实测的最小权限方案”：

- ListCdtInternetTraffic 的 RAM 动作仍缺少公开确认，不添加猜测的 cdt 动作或 cdt:* 通配授权。
- DescribeRegions / DescribeInstanceTypes 的既有公开元数据没有列出 RAM Action；没有据此宣称它们一定不需要权限，也没有凭 API 名添加未确认动作。
- Resource 使用 `*` 以覆盖账号资源列表与不支持资源级约束的查询；这不是已按实例或地域 ARN 收紧并验收的策略。
- 现有真实查询使用此前导入的凭据，不证明仅拥有此模板的独立 RAM 身份能完成全部查询。完整验收需要用户准备仅绑定该策略的只读身份，并在已有安全导入流程中提供其仓库外来源，不能在聊天发送密钥。
- 已补充含开关机版和 EIP/创建主要动作版，范围与未覆盖项见下文；后者还不是完整创建工作流的最小权限方案。模板存在不等于对应生产操作已实现或真实云写操作已获授权。

此前只读 JSON 检查已通过：11 个动作均为显式只读动作，与旧草稿的差集恰为上面两项。

## 三档模板与接口

| ID / 文件 | 动作数 | 增加的能力 | 资源范围 |
|---|---:|---|---|
| [readonly](../../deploy/ram/readonly.json) | 11 | 当前已确认的主要只读查询 | `*`，包括列表查询 |
| [instance_power](../../deploy/ram/instance_power.json) | 17 | ECS 与轻量的开机、关机、重启 | 写动作分别限定 `acs:ecs:*:*:instance/*` 与 `acs:swas-open:*:*:instance/*` |
| [resource_management](../../deploy/ram/resource_management.json) | 25 | 上一档加 ECS RunInstances/DeleteInstance、EIP 分配/绑定/解绑/释放、创建 VPC/交换机 | DeleteInstance 限定 ECS 实例 ARN，其余新增动作暂为 `*` |

三档逐级包含前一档，全部使用明确 Action，不使用 `ecs:*` 等动作通配符。实例 ARN 模式与官方元数据一致，但还没有绑定用户账号、地域或实例集合。第三档的资源范围更宽，不能描述成已按具体工作流收紧的最小授权。

`GET /api/v1/ram-policies` 需要管理员会话，仅返回固定文本。每个条目包含 `id`、`reviewed_on`、`verification=draft`、`cloud_write_permissions`、`limitations` 与可复制的 `document`。没有任何授予权限、权限预检或云资源写入口；实际写权限测试继续显示 `not_verified`。开发模式提供同一目录，前端可复制 `document`，同时展示草稿状态及限制。

模板唯一来源是 `internal/rampolicy.Catalog()`，部署副本用下面的本地命令更新，构建不依赖网络或 Node：

```sh
go run ./internal/rampolicy/export ./deploy/ram
```

该命令只写 JSON 文件，不接入 RAM。测试逐字比对部署副本与 API 文档，并校验逐级动作集合、官方写权限证据、实例 ARN 模式及禁止的通配/云助手/RAM 管理权限。

## 写动作的官方证据

2026-11-12 从阿里云公开 OpenAPI 元数据重新读取了 14 个接口的 `ramActions`，记录在 [ram-write-evidence.json](ram-write-evidence.json)。每项含产品、版本、接口、官方 URL、Action、资源 ARN 与条件字段；它不包含账号凭据或真实调用结果。

主要分组：

- ECS `StartInstance`、`StopInstance`、`RebootInstance`、`DeleteInstance` 均提供实例资源 ARN；`RunInstances` 的本次接口元数据列出全部资源 `*`。
- SWAS 三个电源接口均提供 `acs:swas-open:{region}:{account}:instance/{instance}` 形式。
- VPC 的 EIP、VPC 与交换机动作支持的资源种类并不相同；例如 EIP 绑定/解绑涉及 EIP 和被绑定资源，不能机械地将所有语句换成某一种 ARN。

[ECS 自定义策略官方示例](https://help.aliyun.com/en/ecs/user-guide/custom-policies-for-ecs)说明创建实例通常还涉及镜像、网络、安全组等查询；订单支付路径也会改变所需 BSS 权限。[RunInstances API](https://help.aliyun.com/en/ecs/api-runinstances)列有条件参数与角色传递相关错误。不能由单个 `ecs:RunInstances` Action 推导全部配置组合都可执行。

因此第三档明确标记 `creation_dependencies_pending`：镜像/网络/安全组选择查询、共享带宽成员变更、创建失败后的网络清理、订单支付、KMS、角色传递等应按 M4 实际选定流程逐项评审。没有因为它们可能有用就预先加入未选定的管理权限。特别是 F7.2 的“只操作工具创建和管理的 EIP”需要运行时所有权台账，现有模板本身不能强制证明所有权。

## 验证与未完成项

本批只下载无需认证的官方元数据、生成本地策略文件并测试读取接口；未连接真实 RAM 身份修改授权，未执行写 API 或 DryRun。

实际运行结果：

| 命令 | 输出/结果 |
|---|---|
| `go run ./internal/rampolicy/export ./deploy/ram` | 退出码 0，生成三档 JSON；原 readonly.json 内容不变 |
| `go build ./...` | 退出码 0，无输出 |
| `go vet ./...` | 退出码 0，无输出 |
| `go test -race ./...` | 全部通过；api 29.657s、cli 2.802s、devmode 5.442s、rampolicy 1.443s；其余有测试包缓存通过 |
| `npm --prefix web run lint:api` | `Your API description is valid.`，退出码 0 |
| `git diff --check` | 退出码 0，无输出 |

JSON 检查输出 `readonly actions=11`、`instance_power actions=17`、`resource_management actions=25`。官方证据覆盖新增 14 个 Action；认证测试证明 GET 不创建任务或凭据，POST 不能应用策略。

F1.4 尚缺具体资源范围收紧、独立最小权限身份验收、M4 创建/换 EIP 依赖以及 Claude 的复制界面。CDT 与公开元数据未列 Action 的缺口仍保留，没有用通配授权掩盖。


## 轻量 CPU 读取补充（2026-11-12）

官方 DescribeMonitorData 元数据确认只读 RAM Action 为 swas-open:DescribeMonitorData，资源格式为 `acs:swas-open:{#regionId}:{#accountId}:instance/{#InstanceId}`；sha-pro 真实只读调用已通过。三档策略均加入此动作，当前累计动作数为 12、18、26，部署 JSON 已从 Catalog 重新生成。没有更改真实 RAM 授权；资源范围未按账号收窄、CDT Action 与创建依赖等既有草稿限制仍成立。

手动电源代码已经实现但默认关闭，接口限制标识改为 runtime_writes_require_explicit_enable；这只是准确描述运行时条件，不把 RAM 策略当作执行授权。详见 [CPU 来源](cpu-metrics.md)与[电源接口](instance-power.md)。

系统事件来源阶段再补入只读 ecs:DescribeInstanceHistoryEvents 与 cms:DescribeSystemEventAttribute，当前累计动作数为 14 / 20 / 28；三地域读取已实测通过，未修改真实授权。来源与语义边界见 [系统事件来源](system-events.md)。

2026-11-12 实例账单接入：三档草稿新增只读 `bssapi:DescribeInstanceBill`（官方 action，Resource=`*`），累计 action 数为 15/21/29。依据与真实读取结果见[实例账单验证](instance-billing.md)。未向真实 RAM 身份授予权限，既有 bss 权限不能替代此 action。

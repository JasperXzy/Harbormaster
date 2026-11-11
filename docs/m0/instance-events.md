# 实例变化事件

2026-11-12，对应 F2.5。覆盖已纳管 ECS 与轻量实例；先更新 OpenAPI，再实现采集比较、持久化和只读接口。

## 事件口径

| kind | 依据 |
|---|---|
| discovered | 首次扫描到此前未纳管的实例；不是云端创建时间 |
| disappeared | 该账号、地域、产品的完整扫描中不再出现；不证明云端一定释放，权限可见范围变化也可能造成这个结果 |
| reappeared | 已标记消失的实例重新被发现 |
| status_changed | 两次已知实例状态不同；未知状态不覆盖基准 |
| public_ipv4_changed | 完整的公网 IPv4 地址集合发生变化；忽略顺序和重复项 |
| ipv6_changed | 成功读取的分配 IPv6 地址集合发生变化；过期/未知结果不覆盖基准 |

`observed_at` 是扫描提交时的发现时间，不是操作发生时间。记录包含账号、内部/云端实例 ID、类型、地域、当时名称和相应字段的前后值，不保存整份云响应或账号凭据。

IPv4 比较需要全部相关来源完整：ECS 的实例公网字段、网卡关系、EIP 查询；轻量的实例公网字段及返回的网络属性。失败时页面仍可显示已有的部分地址，但不以此产生“地址已移除”的事件。IPv6 使用上一阶段的 `ipv6.status=ok` 结果，不能由 stale 地址触发变化。

## 持久化与查询

迁移 `013_instance_events.sql` 新增事件表及逐实例比较基准。基准区分未知地址与已确认空集合，并在采集失败、服务重启后保留。只有完整的对应产品/地域列表才推导消失；其他账号、地域、产品扫描完成不会导致该实例消失。

事件、比较基准、资源快照在同一个 `SaveScan` 事务中提交，受账号修订号、启用状态和任务租约约束。任一写入失败全部回滚；重试或重复扫描不会重复记录同一变化。账号停用保留历史。重新发现时先记录 reappeared，不猜测消失期间的中间状态变化。

已有数据库升级时，将现有资源视为已知资源，下一次有效读取建立字段基准，不补造历史新增或字段变化事件。没有扫描的地域仍保留现状；配置中移除地域不等于资源释放。

`GET /api/v1/instance-events` 需要管理员会话；支持 `account_id`、`instance_id`、`limit`（1–200，默认 50）及不透明 `cursor`。按持久序号倒序，新增事件不会插入旧游标后续页。未知筛选条件返回空列表，已停用账号和已消失实例的历史仍可查询。查询只读 SQLite，不调用云接口、不创建任务。

开发模式提供合成的发现和状态变化事件，可直接联调。前端需生成最新契约类型；本批不修改 web/ 或 docs/design/。

## 验证

自动测试覆盖：首次发现、已有资源升级建立基准、主副源不完整、状态未知、地址重排/重复、确认清空、连续扫描去重、消失与重现、地域/产品隔离、轻量状态/IP 变化、重启后保留基准、账号停用、事务回滚及重试、认证、筛选和新增事件期间的稳定分页。

实际运行 `go build ./...`、`go vet ./...`、`go test -race ./...`、`npm --prefix web run lint:api` 和 `git diff --check`。构建、vet 和差异检查通过时无输出；OpenAPI 输出 `Your API description is valid.`。最终 race 输出见本节末尾。

现有验证库的真实只读升级与查询：

```sh
go build -o deploy/bin/hm-server-readonly-check ./cmd/hm-server
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/production-discovery-q8fh4ddr.key deploy/bin/hm-server-readonly-check run-once --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/production-discovery-4z0pnqrt
```

退出码 0，实际日志：

```json
{"time":"2026-11-11T23:44:19.136Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

进程退出且 WAL 文件已消失后，通过 SQLite 只读方式检查：

| 实例 | present | 状态基准 | IPv4 基准 | IPv6 基准 |
|---|---|---|---|---|
| Ali_ShangHai | true | running | 已建立 | 已建立 |
| hk-server-nano | true | running | 已建立 | 已建立 |
| sg-server-nano | true | running | 已建立 | 已建立 |
| sha-pro | true | running | 已建立 | 未知（来源未核实） |

`instance_event_count: 0`，说明已有实例升级时未被误记为首次发现。实际云资源的创建、释放、换 IP 与关机事件没有通过制造真实操作验证；这些变化路径及失败边界使用自动测试覆盖。

全量 race 首轮全部通过：api 29.250s、cli 3.192s、cloud/aliyun 4.707s、collect 3.172s、devmode 5.927s、m0 1.772s、notify 1.857s、store 11.517s，其余缓存通过或无测试文件。补充嵌入 EIP 异常字段检查后再次运行构建、vet 与全量 race，最终 cli 2.623s、cloud/aliyun 4.418s，其余有测试包缓存通过，无失败。

## PRD 修订建议与边界

建议 F2.5 将“新增、释放”明确为“首次发现、不再发现”，或注明这是资源查询的观测事件；若要证明真实创建/释放原因及操作人，需要另行接入相应云端事件来源。当前字段已按可证明的来源命名，不将列表缺失伪装为已执行或确认的 DeleteInstance。

事件记录独立于 Telegram 通知与期望状态规则。本批不声称完成公网 IP 变化推送或外部关机确认策略；没有执行真实开关机、创建、释放或换 IP 来制造事件。事件历史保留期随 F16.5 继续实现。

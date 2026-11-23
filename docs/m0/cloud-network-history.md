# 云监控公网速率历史

2026-11-12；PRD F5.1 的云监控分支。在已有最新速率缓存之上保存原始分钟样本，并提供支持 24h/7d 时间范围的只读接口。agent 历史接口、小时分配、字节聚合和长期清理仍是后续工作，不将这些内容算作完成。

## 数据写入

迁移 023 新增 `cloud_network_sample`，按实例、来源、网络类型、IP、方向、样本时刻唯一；稳定 sequence 用于相同时间的分页排序。保存提供方 Average、60 秒周期、sampled_at 和 checked_at，单位 bit/s。prepared statement 在已有资源扫描事务内批量写入，沿用账号启用状态、修订和 worker 租约检查；无新的云请求。

采集器把完整分页响应内的所有有效分钟点交给存储；相同时间相同值的重叠点去重，冲突点使该查询失败。任何分页失败均丢弃该次原始点，不把前几页当作成功。最新快照回退到 stale 旧值不会制造历史点。内部原始点切片不包含在 `/instances` 响应中，避免最新快照膨胀。

重叠采集按唯一键 upsert，不重复新增；较新的 checked_at 可以修订同一提供方样本，较旧观察不能覆盖新观察。VPC 地址更换后旧地址的历史仍保留，不能把它搬给新地址。缺少某方向、某分钟或查询失败时不补零；明确的 0 值正常保存。保存与资源快照同事务，异常样本或失效租约使整个事务回滚。

## 接口

`GET /api/v1/instances/{instance_id}/cloud-network-history`，管理员 Cookie 会话；只读，不要求采集引擎在线，不调用阿里云、不入队任务。

- `from` / `to` 必填 RFC3339：包含 from、不包含 to；最大 7 天，to 不能晚于当前时间。
- 可选 `direction=ingress|egress` 和公网 IPv4 `ip`；省略则返回全部独立序列。
- `limit` 默认 500，范围 1–1000；最多多取 1 行判断下一页。
- 按 sampled_at 降序、同时间 sequence 降序做 keyset 分页；不使用 OFFSET。
- cursor 绑定实例、时间范围和过滤条件，后续页必须保持这些参数一致。cursor 不包含凭据。
- 返回 items、next_cursor、from、to。每条包含来源、网络类型、可空 IP、方向、指标名、单位、周期、值和两个时间。
- 未知实例 404；已有但没有历史的实例返回空数组。账号停用或实例消失不删除已保存的历史。

分页不是数据库冻结快照：后续采集可能插入较旧时间的样本，后续页可能看到它们；提供方修订可能改变已有值。前端应按 IP/方向分组、按时间绘图，不能跨 IP 或方向直接合成一个累计量。当前接口不做速率积分、不输出账单字节、不启用 CDT 保护。

开发模式提供六分钟合成样本及分页/筛选，使用示例实例的地址，维持明确的 development 标识；未修改前端。真实历史只从升级后的首次完整采集开始积累，首次查询可带回最近 15 分钟窗口；长时间停采后的更早数据尚不自动补采。

## 本地与真实验证

新增测试覆盖完整样本保留、无序分页排序与重复去除、失败分页不带部分历史、重叠 upsert/修订、缺口不补零、地址更换仍可查询旧历史、同一时间跨序列分页、from/to 边界、失效租约和中途异常回滚、会话鉴权、范围与参数校验、cursor 过滤绑定、GET 不创建云任务，以及开发模式分页。

接口限额测试最初把 `limit=1001` 追加到已有 `limit=1`，构造了重复参数；按已有服务约定 Query.Get 取首个值，所以该测试错误地期望 422。已改成单一 `limit=1001` 请求，没有为修正测试而改变其他接口的查询参数约定。

在既有隔离验证库 `/Users/jasperxzy/.local/share/harbormaster-m0/network-rates-offline-aqnc6ndj` 上执行只读 run-once，**2026-11-12T11:58:23.980Z** 完成，cloud_writes=0。进程退出、确认无 WAL 和打开句柄后离线只读检查：

- 23 项迁移，0 个电源任务，foreign_key_check 无错误。
- 三台 ECS 与 sha-pro 共 8 个入/出序列，各 14 个分钟样本，总计 112 条。
- 时间覆盖 11:44:00Z–11:57:00Z，样本来自各完整查询的实际返回，不是人为填满窗口。
- 提供[脱敏证据](cloud-network-history-evidence.json)，只保留实例别名和统计值，不包含实例 ID、IP、userId、请求 ID 或凭据。

```text
HM_MASTER_KEY= \
HM_MASTER_KEY_FILE=/Users/jasperxzy/.config/harbormaster/keys/production-discovery-q8fh4ddr.key \
go run ./cmd/hm-server run-once \
  --data-dir /Users/jasperxzy/.local/share/harbormaster-m0/network-rates-offline-aqnc6ndj
{"time":"2026-11-12T11:58:23.980Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

这证明当前采集链路与落库可用，尚未证明已经积累完整 24h/7d 数据、历史覆盖无缺口或真实多公网 IP 场景均已通过。数据清理、小时聚合和 PRD 的数据库容量目标还未验收。

最终检查命令和结果：

```text
go build ./... && go vet ./... && git diff --check
exit 0，无输出

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 40ms
Your API description is valid.

# 修正上述限额测试请求后，完整复跑
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 58.374s
其他有测试包均 ok (cached)，命令包等为 [no test files]；exit 0。
```

其余包本阶段第一次 race 检查已通过，例如 store 36.525s、cloud/aliyun 4.436s、collect 5.595s、devmode 6.477s、backup 38.090s；最终复跑使用对应缓存。没有把修正前的失败测试列作通过。

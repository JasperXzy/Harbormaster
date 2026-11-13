# 闲置资源候选、覆盖与缓存

2026-11-14，PRD F9.4 的只读候选阶段。识别与证据缓存、分页 API 和无账号开发示例已实现。月度费用估算口径仍待用户答复；本阶段 monthly_cost / currency 始终 null，没有报价、求和、闲置告警或资源操作。原始字段与真实权限通路见[来源验证](idle-resource-sources.md)。

## 来源与判断方法

复用同一次后台区域扫描已有的 DescribeInstances、ListInstances、DescribeDisks、DescribeEipAddresses，不增加云请求。来源列表必须通过原有分页/身份校验；失败或不完整不能被当成空列表。源内字段不足时按资源 ID 记录“无法判断”，与云请求失败分开。

| 候选 | 必须观测到的事实 |
|---|---|
| unbound_eip | Status=Available，且不存在与此矛盾的非空/畸形绑定 ID |
| stopped_subscription | ECS/SWAS 的 Status=Stopped、PrePaid；不把相互矛盾的抢占/包年包月字段解释成可靠证据 |
| stopped_keep_charging | ECS 的 Stopped / PostPaid / KeepCharging |
| stopped_not_charge_exempt | ECS 的 Stopped / PostPaid / Not-applicable，官方含义为不支持停机不收费 |
| unattached_cloud_disk | 已知云盘类别、已知按量/包年包月方式、Available 且没有挂载矛盾 |
| disk_on_stopped_instances | 云盘处于 In_use，归属完整；同一次完整 ECS 列表中全部挂载目标都明确 Stopped |

ECS StopCharging 不产生“计算实例停机仍收费”候选，它的云盘按独立证据判断。已知本地/临时盘不当作独立闲置云盘。多重挂载使用 Attachments、旧共享盘使用 MountInstances；空 InstanceId 不能证明盘未挂载。未知状态/类别/计费方式、缺失挂载目标或 ECS 列表失败会保留无法判断状态；完全未挂载的云盘不依赖 ECS 列表成功。

StopCharging、KeepCharging 与 Not-applicable 沿用 [DescribeInstances 官方元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DescribeInstances/api.json)的字段含义；磁盘来源见 [DescribeDisks](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-describedisks)，SWAS 状态及 ChargeType 见 [ListInstances 元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/ListInstances/api.json)。以上是状态候选依据，具体账单、抵扣、预留用途、托管/IP 地址池等例外仍需费用证据，不据此给出“实际浪费”金额。

磁盘大小保持官方 GiB 数值；缺失为 null。费用与币种完全未知，不按国内/国际站推测币种，不按当前账单日均补出月费用。候选不参与 CDT、流量包保护或电源操作。

## 持久化与未知语义

迁移 044 的 idle_resource_source 按账号/凭据版本/地域/资源类型保存成功时间、最近尝试、稳定错误及数量。idle_resource_candidate 保存最近有效候选与逐条保留标记，关联同一个版本的来源。SaveScan 在既有任务/账号租约及配置修订检查之后，同事务更新来源、候选和实例发现。

- 完整成功列表中，已明确不符合条件或已不再返回的资源移出候选。
- 字段不足的资源保留它原有的候选证据与原时间，evidence_status=retained；本次明确的新候选仍可发布。不能用未知字段消除旧问题。
- 请求失败保留原成功时间、数量和候选，仅更新尝试时间/稳定错误。首次失败的成功数量均为 null，不能显示为零。
- 较旧的尝试不会覆盖较新的来源状态。坏观察、重复来源和跨账号/地域数据会让整次事务回滚。
- 换凭据隐藏旧身份缓存；账号停用或移除地域保留明确 disabled 的历史。不会将旧数据标成新凭据下刚读取。

每个来源返回 scanned_count、unclassified_count、candidate_count。candidate_count 包含为未知行保留的证据，逐条状态和时间说明其新鲜度。字段不足使来源为 partial；云失败为 failing；成功数据超过十分钟或时间在未来为 stale；关闭范围为 disabled。只有从未抓取成功时数量才为 null，明确成功空读才为零。

observed_at / fetched_at 使用区域查询开始时刻，保守限定这批顺序读取的年龄；没有跨接口原子快照或官方计费 cutoff。磁盘的 ECS 状态索引每个来源建立一次，避免按每块磁盘重新遍历实例列表。

## 面板与示例

先更新契约顶部记录，再实现 `GET /api/v1/idle-resources`。管理员会话及现有 HTTPS/主机/CIDR 边界保护，只读 SQLite；GET 不采集、不清理、不估价、不发送消息。

支持 account_id、region_id、kind（ecs/swas/disk/eip）、limit（1–200）和游标。候选 ID 升序、实时键集分页；游标绑定筛选条件，改变条件须从第一页开始。来源覆盖不随候选页截断；空候选页须结合来源状态和无法判断数解释。

同一只读事务读取账号当前凭据版本、候选及来源，防止换密钥期间混用身份。每条候选包含当前 source_status、current/retained 证据状态和原观察时间；monthly_cost / currency 都为 null。

开发模式提供独立未绑定 EIP、未挂载云盘两条合成示例，支持相同分页/筛选，现有运行中实例示例不被伪造为停机。来源数量与示例资源对应，金额保持未知。前端仍由 Claude 接入，未改 web/ 或 docs/design/。

## 验证与实际结果

定向测试覆盖各类正例/明确非候选/未知字段、节省停机与不支持免收费模式、多重挂载、源失败独立性、错误地域、复用调用次数、未知证据保留/恢复明确后移除、成功空列表、失败时间保留、旧观察、凭据轮换/关闭范围、分页及游标筛选、事务回滚/外键、会话与无 GET 云调用、示例未知金额。

实际命令（exit 0）：

```text
go test ./internal/api ./internal/devmode ./internal/store ./internal/model ./internal/cloud/aliyun -run 'TestIdle|TestDemoIdle|TestDevelopmentContractAndSessionBoundary' -count=1
ok github.com/JasperXzy/harbormaster/internal/api 0.870s
ok github.com/JasperXzy/harbormaster/internal/devmode 1.320s
ok github.com/JasperXzy/harbormaster/internal/store 0.560s
ok github.com/JasperXzy/harbormaster/internal/model 0.435s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 0.459s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 48ms
Woohoo! Your API description is valid.
```

真实只读采集使用已有加密数据的独立副本，仅保持已授权的 winnemjw / cn-shanghai 启用，其他账号仍停用；不重新导入凭据，主密钥继续来自副本数据目录之外的既有文件。两个云写开关通过配置及子进程环境显式 false；未启动 serve。

实际命令：

```text
go build -o /Users/jasperxzy/.local/share/harbormaster-m0/idle-live-krhu4kxl/hm-server ./cmd/hm-server
（exit 0，无输出）
/Users/jasperxzy/.local/share/harbormaster-m0/idle-live-krhu4kxl/hm-server run-once --config /Users/jasperxzy/.local/share/harbormaster-m0/idle-live-krhu4kxl/config.json
{"time":"2026-11-13T21:18:03.250Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

2026-11-14 新加坡时间这次采集在 2026-11-13T21:17:57.494198000Z 开始，四个来源均成功落库：ECS/磁盘/EIP 各 scanned_count=0，SWAS=1；全部 unclassified_count=0、candidate_count=0、error_code=null。外键错误为 0，迁移 044 已应用。未发现真实闲置正例，正例判断仍仅有模拟验证；真实空结果不补出金额零。

采集后的摘要脚本第一次用普通只读连接打开 WAL 模式数据库失败，外层脚本 exit 1；子进程已经产生完成日志，未单独保留其退出码。确认它已终止且无 WAL 后，以 immutable 只读方式读取已落盘副本，取回上述证据，没有重复云采集。该读取失败没有被记为 Go 构建/测试失败或云接口失败。已删除临时二进制，私有数据库、日志和摘要留在仓库外；日志/摘要 0600、目录 0700。公开[白名单摘录](idle-candidate-runtime-evidence.json)不含账号/资源 ID、名称、IP、AccessKey 或主密钥。

最终检查（均 exit 0）：

```text
go build ./...
（无输出）
go vet ./...
（无输出）
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 18.829s
ok github.com/JasperXzy/harbormaster/internal/api 221.532s
ok github.com/JasperXzy/harbormaster/internal/backup 90.945s
ok github.com/JasperXzy/harbormaster/internal/cli 46.495s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.651s
ok github.com/JasperXzy/harbormaster/internal/collect 29.369s
ok github.com/JasperXzy/harbormaster/internal/devmode 30.175s
ok github.com/JasperXzy/harbormaster/internal/heartbeat 5.353s
ok github.com/JasperXzy/harbormaster/internal/m0 1.662s
ok github.com/JasperXzy/harbormaster/internal/model 1.449s
ok github.com/JasperXzy/harbormaster/internal/notify 3.501s
ok github.com/JasperXzy/harbormaster/internal/power 46.710s
ok github.com/JasperXzy/harbormaster/internal/probes 24.455s
ok github.com/JasperXzy/harbormaster/internal/release 34.336s
ok github.com/JasperXzy/harbormaster/internal/retention 2.611s
ok github.com/JasperXzy/harbormaster/internal/store 294.324s
ok github.com/JasperXzy/harbormaster/internal/traceinfo 15.360s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。
npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 78ms
Woohoo! Your API description is valid.
git diff --check
（无输出）
```

PRD 修订建议：F9.4 分开“基于状态的闲置候选”“可靠报价推算的月参考费用”“历史实际闲置费用”，并展示无法判断范围；F8.9 后续接入告警时，未知源/字段不能作为恢复依据。月估算口径未答复，仍不实现依赖该选择的计算。PRD 原文未改。

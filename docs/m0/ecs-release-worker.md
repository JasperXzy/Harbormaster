# ECS 释放执行与确认

2026-11-14：独立 worker、定向只读复核和受理后的存在性确认已实现并模拟测试。尚未接入生产启动、配置开关或面板/Telegram 提交入口；execution_enabled 仍为 false。没有真实释放、DryRun 或本批真实云查询。

## 固定读取与执行流程

ReleaseObserver.Inspect 先用 RegionId、单个 InstanceIds、PageNumber=1、PageSize=10 查询目标，要求返回 ID/地域完全一致且 TotalCount 与实际行数一致。目标存在才读取该地域完整磁盘列表。复核从查询开始计时，不把多次顺序查询说成原子快照；30 秒预算和存储的新鲜度检查共同阻止耗时过长的旧复核进入发送。

worker 仅在 Enabled 显式开启、依赖完整时领取持久释放任务，默认无操作。领取后定期续租，在任务/账号修订/凭据版本检查下读取加密凭据，AES-GCM 解密后校验，只在内存使用并清理原始字节。没有任意动作、端点或参数接口。

执行顺序：

1. 定向查询实例、完整读取磁盘，重新计算释放影响。
2. BeginReleaseDispatch 在事务内比较已确认影响、账号/目标及租约，提交一次发送标记。
3. 使用持久记录中的地域/云 ID 调用固定 DeleteInstance 客户端；最多一次 transport 尝试。
4. 用独立短期清理上下文保存递送结果，即使服务退出，也不把已发送动作恢复为排队。
5. 只有 accepted 才进入定向只读确认；其他结果失败且保留 not_sent/rejected/unknown。

不使用云助手、远程命令、自动停机或属性修改。名称、磁盘和锁等变化在步骤 2 之前拒绝；云端并发操作者在最后读取之后的变更仍无法用本接口原子排除，边界沿用[预览说明](ecs-release-preview.md)。

## 确认含义

ReleaseObserver.Observe 只查询同一云实例 ID。成功零行必须同时具备有效数组、TotalCount=0、没有后续页；缺少总数、其他 ID/地域、多行、剩余页、权限/网络错误或畸形响应均为错误，不能写入“实例不存在”。

存储只接受 accepted 之后、发送标记之后、最近 30 秒且单调前进的本地观察时间；再次核对账号启用、修订、凭据版本和地域。实例仍返回时任务继续 running，保存这次观察；完整零行时任务 succeeded，确认依据固定 not_found_after_accept，并同事务把目标资源快照标为 present=0，保留实例/历史行。

该成功只表示同一凭据的定向查询在受理之后不再返回目标。不能证明云端物理清理、云盘/快照最终删除或全部费用终止，也不能单独证明不存在权限可见性变化。结果保留 accepted 与实际确认依据供前端表达，不能只显示“所有资源已销毁”。后续正常发现可能反映不同的新观测，不修改已记录的历史依据。

确认默认每 5 秒读取，最多等待 5 分钟；这些是工程默认值，不是 PRD 的额度保护响应指标。读失败仅继续只读等待，绝不重发 DeleteInstance。超时保留 accepted 并失败为 release_confirmation_timeout；停服、账号变化等中断保留 accepted，失败为 release_confirmation_interrupted。发送结果未知不进入确认，也不因后续空列表伪装为本次成功。

可能进入写阶段的任务结束后安排普通只读资源刷新，以便后续发现状态/IP/消失变化，不推断操作因果。生产采集、M0 和 run-once 没有引用释放 worker，现阶段不会启动它。

## 验证

全程使用合成凭据、本地 SQLite 与模拟 writer/observer/HTTP transport。测试覆盖：

- 完整目标过滤、正确地域、行数/总数和游标完整性；零行不能由请求失败推导。
- 发送前已经提交 dispatching 标记，目标来自持久记录；复核改名、运行中、磁盘属性变化、查询失败或取消时零写请求。
- 受理后先读失败、仍存在、最后零行的完成路径；过旧/未来/倒序观察和未受理观察拒绝。
- unknown、rejected、非法 writer 结果/错误码均不重放；不把原始错误文本写入历史。
- 持续读失败/超时、受理后停服与账号修改保留 accepted，不虚构缺失；复核期间账号修改零写。
- worker 关闭保持队列不动，主密钥不匹配时云读取和 writer 都不会被调用。

实际定向命令（exit 0）：

```text
go test ./internal/release ./internal/store ./internal/cloud/aliyun -run 'TestRelease|TestRestoredRelease' -count=1
ok github.com/JasperXzy/harbormaster/internal/release 1.215s
ok github.com/JasperXzy/harbormaster/internal/store 1.324s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 0.588s

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 37ms
Woohoo! Your API description is valid.
```

最终检查（均 exit 0）：

```text
go build ./...
（无输出）
go vet ./...
（无输出）
go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 10.144s
ok github.com/JasperXzy/harbormaster/internal/api 125.917s
ok github.com/JasperXzy/harbormaster/internal/backup 47.074s
ok github.com/JasperXzy/harbormaster/internal/cli 21.539s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.463s
ok github.com/JasperXzy/harbormaster/internal/collect 14.599s
ok github.com/JasperXzy/harbormaster/internal/devmode 14.688s
ok github.com/JasperXzy/harbormaster/internal/heartbeat 3.359s
ok github.com/JasperXzy/harbormaster/internal/notify 2.370s
ok github.com/JasperXzy/harbormaster/internal/power 23.243s
ok github.com/JasperXzy/harbormaster/internal/probes 13.480s
ok github.com/JasperXzy/harbormaster/internal/release 17.172s
ok github.com/JasperXzy/harbormaster/internal/store 201.935s
ok github.com/JasperXzy/harbormaster/internal/traceinfo 7.893s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。
git diff --check
（无输出）
```

下一阶段才接生产默认关闭的配置与会话/CSRF 提交；Telegram、真实写权限与云端删除验收仍未完成。PRD 修订建议：F7.5 明确 not_found_after_accept 的证据范围、超时/退出保留受理、未知结果不重放。PRD 原文未改。

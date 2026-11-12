# Agent 原始流量历史与区间速率

2026-11-12；对应 PRD F5.1 的 agent 数据分支。沿用已保存的 traffic_sample / traffic_sample_detail，不增加云请求、agent 任务或迁移。前端的曲线绘制仍由 Claude 完成。

## 接口与计量语义

`GET /api/v1/instances/{instance_id}/agent-network-history`：管理员会话，只读本地样本。from/to 必填 RFC3339，from 包含、to 不包含，最多 7 天且 to 不晚于当前时间。limit 默认 500，最大 1000。按采样时间、counter_epoch、sequence 降序 keyset 分页；cursor 绑定实例和时间范围。

响应保留每条原始累计入/出字节、uptime_ms、采样/接收时刻、计数代际、sequence、规则版本、声明上报周期、丢样本数和接收时质量。质量不是当前在线状态；撤销 token、重注册、账号停用不抹去历史。

可比较的同代际样本返回 interval：前序 sequence、起止采样时刻、单调 uptime 差、入/出字节差、完整区间平均 bytes/s。复用既有大整数速率计算，整数除法向下取整，溢出不回绕。原始计数按 PRD 保持整数字节；云监控历史另用 bit/s，两个来源不能混加。

前序查找不受 from 限制：它可能在查询范围之前。返回完整跨界区间，不分摊到 from 之后，不生成小时/范围总量。小时按比例分配的问题仍待用户决定。用户可以查询 24h/7d 原始序列，但这不等于采样缺口已补齐、数据已经完整积累七天或计费对齐已成立。

- `continuous`：可比较、序号连续、单调间隔不超既有 RateWindow、无 gap 标记。
- `gap`：存在有效累计差值，但序号缺失、间隔超过 max(30s,3×声明周期)，或采样记录本身为 gap/声明丢样本；提供的是整个间隔平均值，不能插值为正常瞬时曲线。
- `unavailable`：interval=null，原因明确为 no_previous_sample、metadata_unknown、rules_changed、clock_skew、invalid_counter 或 rate_overflow。新代际不能连接旧代际；缺失基线不假设起点为零。

stale 补报不自动丢弃有效历史差值；接收时间与采样时间单独返回。时钟顺序检查同时检查当前→前序及前序→更前序，兼容旧记录丢失 clock_skew 标记的情况；不把墙钟时间差当作单调计量时长。正向墙钟漂移与缺乏完整历史的所有异常并未被证明都可诊断，因此保留原时间和质量供前端明确展示。

## 修复的既有质量问题

原接收流程在发现时钟倒退后，若该报告又属于补报，会把 clock_skew 覆盖成 stale。现在迟到只对非 clock_skew 记录设置 stale，已有时钟倒退判断保持优先。回归测试通过真实 AcceptReports 路径制造迟到且时间倒退的报告，并验证保存为 clock_skew。历史查询还独立检查前序顺序，不依赖修复后才有的标记。

## 存储与性能

按实例定位唯一 agent，保留撤销后的可查询性。使用原有按 agent/采样时间索引和 (agent,epoch,sequence) 主键查找页内记录及同代际前序，不从查询窗口边界开始重置基线。每页最多读取 limit+1 条主样本，前序查询按主键/序号范围；没有加载全库到内存、没有写入汇总表。

没有 agent 的已知实例返回空数组，未知实例返回 404。接口不要求采集引擎存在，不创建 job，不返回 agent token/hash 或注册凭据。开发模式提供独立的合成累计样本与区间，用于前端联调。

## 验证

本阶段仅本地模拟 agent 报告与 HTTP 请求，不把模拟结果称为真实 Linux 计量验收。没有远程安装 agent，没有云写操作或对外通知。

测试覆盖：迟到补报仍有准确区间、from 之前的基线、缺序号、跨代际重启、新基线、倒退时钟与下一采样、历史旧质量兼容、恢复正常、撤销后仍有历史、未安装 agent、同一时刻跨代际与同代际分页、缺元数据、规则变化、负增量/单调计数无效、整数速率溢出、会话鉴权、范围上限、cursor 绑定、只读不入队，以及开发模式分页。

后续仍需小时聚合策略、自动保留/清理、真实 agent 安装与 24 小时计量验收；未新增这些业务假设。

实际检查命令与结果：

```text
go test ./internal/store -run 'TestAgentHistory|TestAgentInterval' -count=1
ok github.com/JasperXzy/harbormaster/internal/store 0.467s

go test ./internal/store ./internal/api ./internal/devmode
ok internal/store 2.160s
ok internal/api 14.802s
ok internal/devmode 0.988s

go build ./... && go vet ./...
exit 0，无输出

go test -race ./...
ok internal/api 63.612s
ok internal/backup 39.445s
ok internal/cli 9.509s
ok internal/cloud/aliyun 4.504s
ok internal/collect 6.112s
ok internal/devmode 6.728s
ok internal/model 1.790s
ok internal/notify 1.816s
ok internal/power 9.825s
ok internal/store 38.604s
其余包 cached 或 [no test files]；exit 0。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 43ms
Your API description is valid.

git diff --check
exit 0，无输出
```

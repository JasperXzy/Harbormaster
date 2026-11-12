# 实例电源操作

2026-11-12：固定云调用层、持久化任务存储及模拟测试已完成；执行 worker、实际状态确认和面板提交 API 尚未接入。没有使用真实凭据发起电源操作或 DryRun。

## 已确认的业务语义

用户确认：手动开关机任务入队时，事务内保存期望状态；云调用失败仍保留用户意图，在任务记录中显示错误。停机排队期间，后续保活不得按旧意图开机。现已实现原子入队和保存意图；云调用结果只影响任务状态，不回滚意图。

用户另已确认备份恢复取消未完成任务和待发通知、清空租约；该逻辑已在 `60839f3` 实现，恢复不会重放旧操作。新增电源任务时仍须遵守此策略。

## 调用层

`internal/cloud/aliyun/power.go` 提供独立的 `PowerClient`。现有只读 `Client.Call` 仍拒绝所有电源动作；采集与 run-once 没有引用 PowerClient。

固定动作仅为 ECS / SWAS 的 start、stop、reboot。全部使用 SDK 签名、固定 HTTPS 端点及 POST；不接受自定义端点、任意动作或附加参数。SDK 与 transport 禁止重试及重定向，不使用环境代理；SDK DEBUG 开启时拒绝调用。错误只返回稳定分类，不包含原始云错误消息、AccessKey 或签名。

| 产品 | 请求约束 |
|---|---|
| ECS start | `InitLocalDisk=false`，不允许重置本地盘 |
| ECS stop | `ForceStop=false`；必须显式传 keep_charging 或 stop_charging，避免账号默认模式改变语义 |
| ECS reboot | `ForceStop=false` |
| SWAS 三个动作 | `RegionId`、`InstanceId` 和来自持久任务的 `ClientToken`；不接受 StopMode |

ECS 三个单实例电源 API 的官方参数均未声明 ClientToken，因此不发送该参数。SWAS token 本地限制为 1–64 位字母、数字、下划线、连字符，适配持久 job ID，是比官方 ASCII 范围更窄的内部约定。

返回结果区分：

- `not_sent`：本地校验、等待或 SDK 初始化阶段未发出请求。
- `rejected`：收到结构化 4xx 拒绝（408 除外），附稳定错误分类。
- `unknown`：请求可能已执行，包括连接失败、响应丢失、5xx、408、畸形或重定向响应。调用层不自动重放。
- `accepted`：收到 2xx 和 RequestId，仅表示云 API 接受异步请求。不能据此声明已开机、已重启或已进入节省停机。

下阶段 worker 必须将发送前状态持久化，并在崩溃/租约恢复时处理不确定结果；不能沿用只读采集任务的重试恢复策略。实际状态需要独立查询；尤其请求 StopCharging 后可能普通停机成功，必须观察 StoppedMode。重启后的 Running 单点观测也不能单独证明发生过重启。

## 官方依据

核对时间：2026-11-12。六个公开元数据 URL 的 product/version 分别为 `Ecs/2014-05-26` 与 `SWAS-OPEN/2020-06-01`，action 为 StartInstance、StopInstance、RebootInstance，均声明支持 POST/GET：

- [ECS StartInstance 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/StartInstance/api.json)
- [ECS StopInstance 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/StopInstance/api.json)
- [ECS RebootInstance 元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/RebootInstance/api.json)
- [SWAS StartInstance 元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/StartInstance/api.json)
- [SWAS StopInstance 元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/StopInstance/api.json)
- [SWAS RebootInstance 元数据](https://api.aliyun.com/meta/v1/products/SWAS-OPEN/versions/2020-06-01/apis/RebootInstance/api.json)
- [ECS 停机语义及不支持时回退](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-stopinstance)
- [SWAS 重启状态与幂等参数](https://help.aliyun.com/en/simple-application-server/developer-reference/api-swas-open-2020-06-01-rebootinstance)

PRD 修订建议沿用 [V9](V9-economical-stop.md)：区分请求受理和实际状态，不能把不报错解释为节省停机生效。V9 真实验证状态没有改变。

## 验证

模拟 transport 覆盖六类动作和两种 ECS 停机模式、签名/固定地址、普通关机/不初始化本地盘、非法参数零调用、取消/DEBUG、4xx/5xx/408、重定向、畸形响应、响应丢失、单次请求及错误脱敏。没有真实资源操作。

本阶段实际命令与输出（均退出 0）：

```text
go test ./internal/cloud/aliyun
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 3.455s

go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 39.357s
ok github.com/JasperXzy/harbormaster/internal/cli 7.043s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.476s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。

git diff --check
（无输出）
```

调用层阶段没有面板接口变更。后续任务存储阶段已先在 OpenAPI 增加 instance_power 任务类型与 PowerJobResult，尚无电源提交接口；前端可以识别该任务类型，不能据此启用电源按钮。

## 持久化任务阶段

迁移 017 扩展 job 类型约束。迁移在原事务内暂存并恢复 job_change、job_order、cdt_observation 和通知的 job_id 关联，全程保持 foreign_keys 开启。保留原任务字段、分页序号及 SSE 高水位，不制造通知发送事件。

`EnqueuePowerJob` 仅允许仍在已启用账号、已配置地域中被发现的 ECS/SWAS。每次新动作必须携带持久事件键（16–64 位字母、数字、下划线或连字符，可使用 UUID）。事件键与任务关联长期保留，完成或恢复后的相同请求仍返回原任务；同键不同目标/动作报 idempotency_key_conflict。活动任务去重时也保存新事件键的关联，避免完成后旧 HTTP 重试再次开机或重启。请求和期望状态在同一事务写入：start/reboot 为 running，stop 为 stopped。相同实例已有相同动作/模式/账号修订的活动任务时复用；不同请求返回 instance_power_busy，不覆盖已有意图。

独立 `ClaimPowerJob` 不与只读领取器混用。租约为 60 秒；发送前因进程中断可重新领取，最多 3 次。`BeginPowerDispatch` 重新检查账号启用状态、配置修订、地域及实例是否仍存在，并持久化发送标记后才允许调用云接口。发送标记只可写一次，过期 owner 不能写结果。

`RecordPowerDispatch` 仅接受固定结果及错误码。accepted 保持 running，等待后续实际状态确认；其他结果为 failed。发送后租约失效会将未知响应记为 power_outcome_unknown；已接受但确认过程丢失记为 power_confirmation_interrupted。两者都不会被重新领取发送。通用 FinishJob/RetryJob 拒绝电源任务，避免套用读请求的重试语义。

恢复时取消排队、执行中及等待确认的电源任务；dispatching 改为 unknown，已知 accepted 保留，完成的拒绝记录不改写。期望状态保留为备份时的用户意图，恢复不会运行 worker。

本阶段的内部工程选择：同一实例只允许一个活动电源任务；冲突请求明确报错。发送前最多领取三次沿用现有任务上限，云请求本身仍最多一次。未实现自动化、豁免、控制台停机询问、实际状态确认和写 worker；没有把这些标为完成。生产启动与 run-once 仍只有只读采集。

持久任务阶段最终检查（均退出 0）：

```text
go test ./internal/store -run Power -count=1
ok github.com/JasperXzy/harbormaster/internal/store 0.775s

go build ./... && go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/api 42.805s
ok github.com/JasperXzy/harbormaster/internal/backup 37.742s
ok github.com/JasperXzy/harbormaster/internal/cli 8.620s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.407s
ok github.com/JasperXzy/harbormaster/internal/collect 4.156s
ok github.com/JasperXzy/harbormaster/internal/devmode 6.972s
ok github.com/JasperXzy/harbormaster/internal/m0 1.595s
ok github.com/JasperXzy/harbormaster/internal/model 1.326s
ok github.com/JasperXzy/harbormaster/internal/notify 2.045s
ok github.com/JasperXzy/harbormaster/internal/store 21.825s
其余有测试的包均 ok (cached)，命令包等为 [no test files]。

npm --prefix web run lint:api
../docs/api/openapi.yaml: validated in 23ms
Woohoo! Your API description is valid.

git diff --check
（无输出）
```

覆盖原子失败回滚、并发重复请求、完成/恢复后的事件键重试、只读 worker 隔离、租约过期与旧 owner、发送后的不重放、账号修改/停用及实例消失、已接受结果不冒充已完成，以及迁移后的四类旧引用和两个序号高水位。没有对真实验证数据库运行本迁移，也没有执行真实云操作。

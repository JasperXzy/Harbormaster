# 实例电源操作

2026-11-12：本阶段完成固定云调用层及模拟测试；生产任务入队、执行 worker、实际状态确认和面板 API 尚未接入。没有使用真实凭据发起电源操作或 DryRun。

## 已确认的业务语义

用户确认：手动开关机任务入队时，事务内保存期望状态；云调用失败仍保留用户意图，在任务记录中显示错误。停机排队期间，后续保活不得按旧意图开机。该决定待下一阶段存储和任务实现，不能由本阶段调用层代替。

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

本阶段没有面板接口变更，无需生成前端类型或修改 OpenAPI。生产电源接口接入前会先更新契约和顶部变更记录。

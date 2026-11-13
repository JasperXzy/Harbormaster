# ECS 释放调用层

2026-11-13：本文记录独立固定调用层及其模拟验证。后续已实现后台只读影响采集和管理员[预览接口](ecs-release-preview.md)。尚未提供释放提交、任务、worker 或 Telegram 入口；F7.4 没有完成。没有使用真实账号发送 DeleteInstance，包括 DryRun。

## 官方接口核对

核对官方 [DeleteInstance 文档](https://help.aliyun.com/zh/ecs/developer-reference/api-ecs-2014-05-26-deleteinstance)及 [2014-05-26 公开元数据](https://api.aliyun.com/meta/v1/products/Ecs/versions/2014-05-26/apis/DeleteInstance/api.json)：

- 单次只能指定一个 InstanceId；支持 POST；权限为 `ecs:DeleteInstance`，支持实例资源级授权。
- Force 默认 false，只能正常释放已停止的实例。运行中释放另有 Force / ForceStop 语义。
- TerminateSubscription 默认 false；true 的公开含义是允许释放**已到期**的包年包月实例，不能据此宣称支持未到期退订。
- 请求没有声明 ClientToken。返回只有 RequestId，没有“已释放”的状态证明，也没有可重试的幂等承诺。
- 云盘与自动快照的去留取决于已有 DeleteWithInstance / DeleteAutoSnapshot 属性；手动快照、EIP 不随实例释放。安全锁定时，云盘保留属性存在例外，官方说明即使 DeleteWithInstance=false 也可能被释放。

这些结论来自公开接口说明，没有验证真实资源删除或写权限。既有 RAM 模板含此 action 也不等于对应凭据已获授权。

## 本阶段实现

`internal/cloud/aliyun/release.go` 的 ReleaseClient 与只读 Client 分离，输入只有地域和 ECS 实例 ID。固定调用 DeleteInstance，版本 2014-05-26，固定 HTTPS 端点、POST、Force=false、TerminateSubscription=false；不接受端点、任意动作、ClientToken、强制释放或关联资源修改参数。这是最小内部调用原语，尚不决定最终入口允许的计费类型。

调用前检查站点、地域格式、账号启用的地域列表、实例 ID 和 SDK DEBUG。复用已有 HTTP 端点检查、禁用环境代理与重定向、20 秒超时及响应大小限制。电源与释放客户端共用一次尝试门控；SDK 和 transport 均不重试。

结果只表示请求递送情况：

| outcome | 含义 |
|---|---|
| not_sent | 本地校验、取消、等待或 SDK 阶段没有尝试发送 |
| rejected | 收到结构化 4xx 拒绝，408 除外；不存在实例也记拒绝，不冒充本次已释放 |
| unknown | 响应丢失、传输异常、5xx、408、重定向、无 RequestId 或畸形响应；不能自动重发 |
| accepted | 收到 2xx、非空 RequestId 且无错误 Code；不能直接显示“已释放” |

错误只返回稳定分类；删除保护、安全锁、计费类型、状态或关联依赖错误均不会触发自动关闭保护、解绑、停机或删盘。云错误原文、RequestId、AccessKey 和签名不向调用者或日志转发。

## 已确认与后续工作

用户已确认两项产品规则：

1. 首期只允许已停止的按量付费/抢占式 ECS，不开放包年包月或运行中实例。
2. 沿用关联资源当前删除属性并列明影响，不自动修改属性、不另外释放 EIP 或手动快照。

这些是功能语义确认，不能代替真实操作授权。调用层尚无执行入口。

后续须先改面板契约，再实现影响预览、输入实例名二次确认、持久任务与发送前边界、账号/实例配置变化检查、结果查询及历史保留。必须独立处理恢复后的旧释放任务、发送后不确定结果与关联资源信息缺失。不能仅因某次发现扫描未返回实例，就断言释放成功。

PRD 修订建议：F7.4 补充允许释放的状态/计费类型、关联资源去留和安全锁例外；F7.5 区分受理与确认完成，明确无幂等支持时不重放未知结果。PRD 原文未改。

## 验证

模拟 transport 覆盖固定签名请求、三个端点/站点组合、非强制参数、非法目标/地域/DEBUG/取消零请求、稳定错误分类、原始错误脱敏、请求丢失及不重试。既有电源操作测试继续覆盖共享门控；只读 Client 的拒绝写动作测试继续包含 DeleteInstance。

本阶段实际命令与输出（均退出 0）：

```text
go test ./internal/cloud/aliyun -run 'TestRelease|TestPower|TestWrite|TestReadOnly' -count=1
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 0.454s

go build ./...
（无输出）

go vet ./...
（无输出）

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/agentclient 7.927s
ok github.com/JasperXzy/harbormaster/internal/api 113.950s
ok github.com/JasperXzy/harbormaster/internal/cli 15.712s
ok github.com/JasperXzy/harbormaster/internal/cloud/aliyun 4.393s
其余有测试的包均 ok (cached)，命令包等显示 [no test files]。

git diff --check
（无输出）
```

没有面板契约变更，不需要前端启用任何释放按钮。

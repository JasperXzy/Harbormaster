# hm-server 身份识别的元数据基础

2026-11-12；对应 PRD F6.13，作为定时停机/其他自动化的自身排除前置工作。此阶段完成共享的身份读取组件，尚未接入 hm-server 身份标注、部署绑定或自动停机守卫，不把前置组件称为自动化功能已完成。

## 已确认的来源与范围

[阿里云实例元数据官方文档](https://www.alibabacloud.com/help/en/ecs/user-guide/view-instance-metadata/)规定加固模式先向固定元数据地址取得短期令牌，再带令牌读取身份；令牌 TTL 有有效范围，实例身份宜缓存，不能把频繁查询当作通用 API 轮询。

共享组件 `internal/cloud/aliyunmeta.Read` 固定执行：

1. `PUT http://100.100.100.200/latest/api/token`，TTL 60 秒。
2. `GET /latest/meta-data/instance-id`。
3. `GET /latest/meta-data/region-id`。

没有 RAM 凭据目录、用户数据、任意路径或 Cloud Assistant 操作。PUT 仅用于元数据短期令牌，不是创建/修改阿里云资源。当前 hm-agent 已改为使用此组件，保留原 Metadata 接口与错误码；未来 hm-server 可复用同一实现。

总调用预算 5 秒，保留调用方更短的超时；响应单项最多 4096 字节。标准 HTTP transport 克隆后禁用代理与连接复用，不改变调用方 client/transport；不携带 Cookie，不接受重定向，即使调用方 client 允许重定向也不会跟随。短期令牌不返回给业务调用者、不持久化、不写日志。错误仅为稳定 aliyun_metadata_unavailable，不附原始响应或网络错误。

实例 ID 与地域须通过与现有注册约定一致的字符/长度校验，任一字段失败则不返回部分身份。读不到元数据只代表身份未知，不能据此推断机器不在阿里云，也不能推断它不是被纳管的实例。

## 轻量实例限制

既有真实 sha-pro 验证已证明：轻量元数据返回底层 ECS ID，不是 SWAS API 的实例 ID（见 [V6](V6-swas.md)）。共享读取组件不会改变或掩盖这个事实。hm-agent 继续使用用户批准的预绑定注册码方案；元数据不是远程身份的密码学证明。hm-server 的轻量自身绑定与“不在纳管实例中”的声明仍需明确部署策略，不能直接把元数据 ID 当作 SWAS ID。

## 已提出的两个规则边界

正在等待用户选择，未据此启用自动化：

- 新建/启用/修改定时规则是否从下一个计划点生效；建议 PRD 的默认 15 分钟补偿只针对已启用规则因停服/延迟错过的计划。
- 元数据身份未知时是否阻止自动停机；建议允许部署配置明确指定自身实例或声明不在纳管实例中，且这些主机相关配置不随数据库备份迁移。

PRD F6.3 已明确默认 15 分钟补偿窗口，未将其改成全部跳过。其余自动化要求（频控、优先级、自身排除、试运行等）仍待完成，当前无定时规则进入真实电源队列。

## 验证

本轮只使用内存 HTTP transport，不在用户本机或远程机器上实际请求元数据。验证固定方法/路径/令牌头、总预算与调用方更短的期限、Cookie 隔离、禁止重定向、标准 transport 禁用代理且调用方配置不变、过长/空/换行令牌、非法身份/地域、错误响应与部分身份丢弃。现有 agent 注册与上报测试继续通过。没有执行真实云写操作。

实际工程检查：

```text
go test ./internal/agentclient ./internal/agentcli
ok internal/agentclient 0.684s
ok internal/agentcli 0.632s

go test -race ./internal/cloud/aliyunmeta ./internal/agentclient ./internal/agentcli
ok internal/cloud/aliyunmeta 1.651s
ok internal/agentclient 1.694s
ok internal/agentcli 1.649s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

go test -race ./...
ok github.com/JasperXzy/harbormaster/internal/store 52.103s
其他有测试包均 ok (cached)，命令包等为 [no test files]；exit 0。
```

本阶段没有修改面板接口契约或前端文件，没有执行真实元数据请求、云资源写操作或部署。

# 生产 agent 接收端验证

日期：2026-11-12。范围：注册、撤销、批量落库、状态与速率；尚未在真实机器安装 agent。

已实现：

- 一次性注册码预绑定已纳管实例，10 分钟有效；ECS 比较元数据 ID/地域，SWAS 使用用户确认的预绑定方案。元数据文本不作为远程身份认证。
- 派生 token 的相同注册请求可重试，服务端仅存哈希。签发注册码不提前中断旧 agent；成功注册才轮换 token。
- 单条与批量上报，事务全部提交后 ACK；相同样本幂等，冲突整批回滚，已退役 epoch 在原始数据清理后仍不可恢复。
- 只接受启用账号且仍在纳管快照中的实例；HTTPS 校验与管理员登录策略分离。面板撤销仍必须通过管理员会话和 CSRF。
- 独立记录采样时间/接收时间、计数规则版本、IPv4/IPv6 分量、溢出样本数与质量。历史补传和重复请求不刷新在线心跳。
- 实例列表叠加真实 agent 状态；30 秒离线，当前速率仅从相邻有效样本计算。SSE 每 5 秒合并变动并可用 null 清除速率。CDT 保护仍为未对齐状态。

实际检查：

```text
go build ./...
(exit 0, no output)

go test -race ./...
ok internal/api 23.244s
ok internal/cli 2.586s
ok internal/cloud/aliyun (cached)
ok internal/collect 1.950s
ok internal/config (cached)
ok internal/devmode 5.404s
ok internal/logging (cached)
ok internal/m0 (cached)
ok internal/security (cached)
ok internal/store 4.954s
ok internal/ui/prepare (cached)
其余包 [no test files]；exit 0

go vet ./...
(exit 0, no output)

npm --prefix web run lint:api
Your API description is valid.
```

OpenAPI 首次检查发现新增 nullable 缺少同级 type，已补 type: object 并复查通过。检查不生成或修改前端文件。测试覆盖注册失败/响应丢失重试/轮换/过期/撤销、轻量预绑定、批量原子性、旧数据不算在线、跨 epoch 防重放、状态期限、速率溢出、HTTPS/独立鉴权/CSRF 和 SSE 合并。

待完成：Linux nftables 客户端、加密本地状态、持久缓冲、安装脚本与发布文件；前端由 Claude 重新生成类型并处理 pending_first_report、data_quality 与 realtime=null。安装命令 API 在发布文件就绪后实现，当前不返回假命令。没有执行真实云写操作、没有安装真实 agent，也没有证明 V7 的实际吞吐/CPU/计数准确率。

工程取值：注册期限 10 分钟、批量上限 100、旧样本保留接收窗口 7 天、相邻样本速率最大间隔 30 秒，详见 agent-protocol.md。均为本地协议边界，不代表官方账单统计口径；后续若调整契约将记录变更。

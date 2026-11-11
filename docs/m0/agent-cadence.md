# Agent 上报周期

2026-11-12，补齐 PRD F12.4 的“每 10 秒（可配）”。默认仍为 10 秒。

## 配置与协议

`hm-agent serve` 和 `run-once` 接受 `--report-interval 10s`。可用范围为 1–60 秒的整数秒；这是当前实现的工程约束，PRD 没有规定上下限。小数秒、零、负数和超过一分钟均在打开状态目录或采集前拒绝。开发模式也使用同一参数。`run-once` 只采集一次，周期参数声明该次样本的预期采样间隔。

systemd 使用 `/etc/harbormaster/agent.env` 中的 `HM_AGENT_REPORT_INTERVAL=10s`，安装脚本保留已有值，升级失败时随 env 一起恢复。部署文件要求规范形式 `1s`–`60s`；CLI 也接受等价的 Go duration，如 `1m`。本阶段没有服务端远程修改 agent 配置的动作。

上报新增可选 `report_interval_seconds`。省略或 0 按 10 秒解释，显式 10 与省略在重试校验中等价。默认 agent 省略新字段，维持旧服务端严格 JSON 解码的兼容；使用其他周期前须先升级服务端。注册响应里的 10 秒表示默认值，不覆盖本地配置。

迁移 008 为 `traffic_sample_detail` 和 `agent_counter_state` 新增该列，历史值补 10。周期变化保持累计计数、counter_epoch、sequence 连续；缓存中的旧样本保留原周期。相同序号重试更换周期会返回冲突。缓存容量仍为 8640 条：默认约 24 小时，1s 约 2.4 小时，60s 约 6 天；溢出、过期仍显式标记丢样。

## 速率、在线状态和面板字段

实时速率窗口为 `max(30 秒, 3 × 当前样本声明周期)`。同 epoch 的单调时间间隔超过窗口则标记 gap，不把长时间平均值当成实时速率；之后正常连续采样可恢复 ok。该窗口是对既有默认 30 秒窗口的扩展工程策略，不代表 CDT 计费窗口。

入库心跳仍要求采样时间与接收时间相差不超过 30 秒，延迟补传不会因为声明长周期而刷新在线时间。掉线阈值继续取持久化 `agent_offline.offline_after_seconds`，不会自动随周期增长。调整较长周期前，应先调整掉线阈值并留出网络余量；例如 60s 采样与 180s 阈值。PRD 的默认 30 秒掉线目标不能同时适用于一分钟采样，建议在验收文字中限定默认周期。

面板 `Agent.report_interval_seconds` 新增可选可空整数，表示最新接受的样本周期；当前注册尚无新鲜上报或已撤销时为 null。`Settings.collection.agent_report_interval_seconds` 仍是默认周期，并非全局覆盖。前端可按此字段说明实例实际周期；不要求前端参与 agent 协议或配置下发。本阶段未修改 `web/` 和 `docs/design/`。

## 实际验证

全部使用本机临时数据、模拟计数和模拟系统命令，没有安装远程服务或改动真实云资源。

```text
go build ./...                         exit 0（无输出）
go vet ./...                           exit 0（无输出）
go test -race ./...                     exit 0
  internal/agentcli                     1.378s
  internal/agentclient                  1.480s
  internal/agentrelease                 2.967s
  internal/agentstate                   1.702s
  internal/api                          26.791s
  internal/cli                          2.809s
  internal/collect                      2.276s
  internal/devmode                      5.493s
  internal/notify                       1.514s
  internal/store                        8.427s
  其他有测试的包                        cached / passed
sh -n deploy/agent/install.sh           exit 0
sh deploy/build-agent.sh                exit 0（Linux amd64、arm64）
npm --prefix web run lint:api           Your API description is valid.
git diff --check                       exit 0
```

额外在临时目录构建 agent 并运行 `serve --dev --report-interval 1s`，2.3 秒后终止，读取输出得到：`2 samples, monotonic sequences, declared interval=1`；临时文件已清理。

回归覆盖：旧 schema 有样本时迁移及幂等重试；默认与显式 10 等价；不同周期重放冲突；60s 速率有效期；独立掉线阈值；长间隔缺口与恢复；补传不刷新心跳；重新注册不泄露旧周期；修改周期不重置累计计数或重写队列；首次安装默认、失败回滚、成功升级保留本地配置。

Linux 内核计数和真实 systemd 运行验收仍依赖已提出的环境操作授权；本机交叉构建不替代这两项验收。

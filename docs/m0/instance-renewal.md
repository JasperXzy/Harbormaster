# 实例自动续费状态

2026-11-12，PRD F9.3 的只读状态查询部分。实例快照新增 `renewal`：status、source、checked_at、last_error。前端按契约显示，不需要直接调用云接口。

## 来源与匹配

- 包年包月 ECS：[DescribeInstanceAutoRenewAttribute](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstanceautorenewattribute)，固定 RegionId 和已纳管 InstanceId，每批最多 100 个，按 PageNumber / TotalCount 收齐。RenewalStatus 映射自动、手动、不续费；AutoRenewEnabled 若与状态矛盾，返回 unknown 和 renewal_response_conflict。
- 轻量：[ListInstances](https://help.aliyun.com/zh/simple-application-server/developer-reference/api-swas-open-2020-06-01-listinstances) 提供到期时间，但没有自动续费字段。改用 [QueryAvailableInstances](https://help.aliyun.com/en/user-center/developer-reference/api-bssopenapi-2017-12-14-queryavailableinstances)，只传已发现的包年包月实例 ID，按 PageNum / Data.TotalCount 收齐。逐条核对 InstanceID、Region、SubscriptionType，重复、额外实例或归属冲突不发布为有效结果。未使用底层 ECS 元数据 ID，也不猜测产品代码映射。
- 已知按量或抢占式实例不请求包年包月续费 API，状态为 not_applicable，到期时间为 null。计费类型未知、记录缺失、状态未知或请求失败则保持 unknown；空响应不代表关闭自动续费。

ECS 新增的读权限为 ecs:DescribeInstanceAutoRenewAttribute；轻量 BSS 查询需要 bss:DescribeInstances。现有 BSS 财务查询成功不能覆盖续费查询缺权限的结论。每个请求仍受账号地域配置、租约、只读动作/参数白名单和统一限速约束。

status=auto_renewal 只说明云端策略已开启，不保证以后扣款或续费成功。本阶段不调用 RenewInstance、ModifyInstanceAutoRenewAttribute 等写接口。到期提醒单独接入告警阶段，默认阈值以 PRD 第 10 节的 7 天为准。

## 真实只读结果

构建并对既有仓库外验收数据库执行 run-once，未输出凭据或财务金额：

```text
go build -o deploy/bin/hm-server-readonly-check ./cmd/hm-server
HM_MASTER_KEY_FILE=<已有仓库外主密钥文件> ./deploy/bin/hm-server-readonly-check run-once --data-dir <已有隔离验收目录>
{"time":"2026-11-11T22:41:21.550Z","level":"INFO","msg":"run_once_complete","cloud_writes":0}
```

| 实例 | 本次计费类型 | 自动续费状态 | 云端到期时间（UTC） |
|---|---|---|---|
| sg-server-nano | spot | not_applicable | null |
| hk-server-nano | spot | not_applicable | null |
| Ali_ShangHai | subscription | auto_renewal，ECS 来源 | 2026-11-20T16:00:00Z |
| sha-pro | subscription | auto_renewal，BSS 来源 | 2028-05-11T16:00:00Z |

四条快照 last_error 均为 null，检查时间为 2026-11-11 22:41:15–20 UTC。这确认了 sha-pro 能按轻量 API 的实例 ID 从 BSS 取得续费状态；不代表其他账号、缺少该权限或没有匹配记录的实例一定可查询。

## 验证

```text
go build ./...                       exit 0（无输出）
go vet ./...                         exit 0（无输出）
go test -race ./...                  exit 0
  internal/api                       27.515s
  internal/cli                       2.725s
  internal/cloud/aliyun               1.431s
  internal/collect                   2.571s
  internal/devmode                   5.378s
  其他有测试的包                     cached / passed
npm --prefix web run lint:api        Your API description is valid.
git diff --check                    exit 0
```

针对性测试覆盖：过滤非包年包月实例、100 个 ID 分批与分页、身份/地域/计费类型匹配、重复/空页/额外记录拒绝、空结果保持未知、ECS 状态冲突、BSS 权限合并、签名请求以及续费写接口继续被拒绝。开发模式提供续费状态示例；前端目录未修改。

# CDT 服务状态、阶梯与已切换产品验证

2026-11-13，补充 V1/V2/V3。三个固定只读接口已取得真实响应；**Traffic 的单位声明、统计截止时间和资源级免费额度资格仍未确认**。本批不改变生产计量质量、保护状态或面板契约。

## 方法

[阿里云 ActionTrail 的 CDT 操作目录](https://help.aliyun.com/en/actiontrail/product-overview/audit-events-of-cdt)将 GetCdtInternetServiceStatus、ListCdtTrafficTiers、ListSwitchedCdtProducts 列为查询操作。目录确认操作名称与读取性质，不提供稳定参数/响应契约。

新增 M0 固定检查 `--check cdt-service`，沿用已验证累计查询的国内站端点 `cdt.aliyuncs.com`、版本 2021-08-13、服务地域 cn-hongkong、官方通用 Go SDK 的签名 POST。开通、切换和回滚服务等操作不在白名单；不支持 DryRun、任意 RPC、自定义端点或用户附加参数。国际站端点未验证，联网前拒绝。

检查一次账号服务状态，再为各个已配置业务地域查询阶梯和已切换产品。`--region` 可缩小业务地域范围；BusinessRegionId 必须来自导入的地域列表。报告分别保存服务 region 与 business_region，不能把服务端点所在地当作被查询资源所在地。

先前试探只带 RegionId，两个列表接口明确返回 MissingBusinessRegionId。随后仅增加该已知必需字段，重新执行全部检查。该参数来自真实失败响应与后续成功验证，仍不是公开文档所保证的长期稳定 API。没有添加推测的产品、线路或分页过滤参数。

## 实际结果

最终读取在 2026-11-13T06:31:01Z–06:31:03Z 完成：jasperxzy 上海/新加坡/香港，winnemjw 上海，合计 10 项查询，全部 passed / complete=true / cloud_writes=0。

| 接口 | 查询数量 | 实际响应 |
|---|---:|---|
| GetCdtInternetServiceStatus | 2 | 两个账号均返回 Enabled=true；顶层另有 RequestId |
| ListCdtTrafficTiers | 4 | 每个地域均返回相同的 10 个 TrafficTiers；行字段为 Tier、LowestTraffic、可选 HighestTraffic |
| ListSwitchedCdtProducts | 4 | 均返回 CdtProducts=[] |

四套阶梯首档上限是 10995116277760，即 `10 × 1024^4`，延续了此前“暂按字节解释”的推断。最后一档没有 HighestTraffic，不把缺失上限补为零。阶梯响应没有价格、币种或单位字段，不能将这份通用分档当作账号实际单价表。[官方公网计费说明](https://help.aliyun.com/zh/cdt/internet-data-transfers/)是公开规则来源，接口返回的分档不是已核实的金额计算契约。

本次三种响应均无账期、统计截止时间、更新时间或单位字段。因此没有升级 Traffic 的质量，也不能据此按“官方 + 抓取时刻后增量”估算。原有 cutoff_unknown、单位/计费口径待核实和停用精确保护继续成立。

Enabled=true 只证明本次接口返回的账号级服务状态，不证明所有已发现的 ECS/EIP 都可抵扣免费额度。空 CdtProducts 也不被解释为“账号没开通 CDT”或“当前资源都未切换”；它没有给出可核对的产品切换记录、时间或实例列表。旧累计查询曾取得非零流量，本次没有额外依据消解这两个接口的范围差异。

报告的 complete=true 在这里表示请求结束且未返回需要跟随的 NextToken；未证明私有 API 的默认筛选覆盖全部产品历史。真实多页、未开通服务账号、非空切换记录、国际站及单位/水位均未验证，不制造这些环境。

## 结论及 PRD 修订建议

- 第 7 节可补充这三个查询通路，但应标注非公开稳定契约及实际必需的 BusinessRegionId；不能将服务状态或空列表当作资源计费资格判据。
- 第 6.2 节及 V3 的 cutoff 可空建议仍需保留；不能因为新增查询成功就启用对齐估算、历史日分配或保护。
- 两个账号的只读来源已可用，不需要重新提供凭据。单位/时间语义仍需要官方接口说明或支持答复、可对照的控制台/账单明细及同窗长期数据。没有为获得这些证据执行云写操作、安装 agent 或启用服务。

脱敏摘录见[机器可读证据](cdt-service-evidence.json)：只含账号别名、地域、状态、时间、返回字段名、服务布尔值、产品数量与通用阶梯；不含账号数字 ID、请求 ID、资源身份、AK 或令牌。生产白名单与扫描器未改，web/、docs/design/ 未修改。

## 实际命令和检查

本地包装器创建仓库外 0700 目录后编译并并行查询，实际命令参数如下：

```text
go build -o /Users/jasperxzy/.local/share/harbormaster-m0/cdt-service-regions-6jptfdzt/hm-server ./cmd/hm-server
/Users/jasperxzy/.local/share/harbormaster-m0/cdt-service-regions-6jptfdzt/hm-server m0 query --account jasperxzy --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key --check cdt-service --output /Users/jasperxzy/.local/share/harbormaster-m0/cdt-service-regions-6jptfdzt/jasperxzy.json
{"account":"jasperxzy","cloud_writes":0,"status":"report_written"}
/Users/jasperxzy/.local/share/harbormaster-m0/cdt-service-regions-6jptfdzt/hm-server m0 query --account winnemjw --vault /Users/jasperxzy/.local/share/harbormaster-m0/credentials.enc --key-file /Users/jasperxzy/.config/harbormaster/keys/m0.key --check cdt-service --output /Users/jasperxzy/.local/share/harbormaster-m0/cdt-service-regions-6jptfdzt/winnemjw.json
{"account":"winnemjw","cloud_writes":0,"status":"report_written"}
```

三条均 exit 0，私有报告 0600，临时二进制已清理。首次缺少 BusinessRegionId 的两条账号查询均 exit 1，报告另存于仓库外 cdt-service-fn23muvu，不作为最终完整性证据。

最终本地测试包括根据实际响应补充的 boolean/array 形状检查；累计查询原有 TrafficDetails 检查继续有效。另覆盖签名/固定 HTTPS、业务地域越界、非法参数和云写动作在联网前拒绝、未确认续页不算完整以及国际站拒绝。

```text
go test ./internal/m0 -count=1
ok  github.com/JasperXzy/harbormaster/internal/m0  0.608s
go test -race ./internal/m0 ./internal/cli
ok  github.com/JasperXzy/harbormaster/internal/m0   1.515s
ok  github.com/JasperXzy/harbormaster/internal/cli 15.098s
go build ./...                          # exit 0，无输出
go vet ./...                            # exit 0，无输出
gofmt -l internal/m0                    # exit 0，无输出
git diff --check -- internal/m0 docs/m0  # exit 0，无输出
```

本批只影响 M0/CLI，未重跑无关生产包的全量 race 或未改动的契约 lint。此前全量检查见[延迟保留验证](latency-retention.md)。

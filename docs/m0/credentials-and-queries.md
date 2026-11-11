# 本机凭据导入与 M0 只读查询

2026-11-11。只增加 `hm-server m0` 命令，不改变面板接口，不新增常驻程序。工具先用假凭据完成离线测试，用户导入后又完成首轮真实只读验证，最新结论见[实测记录](live-verification-2026-11-11.md)。

## 1. 准备 RAM 只读用户

为两个账号分别准备 RAM 用户/AccessKey，绑定 [只读策略](../../deploy/ram/m0-readonly.json)。这是本轮工具所需查询能力的模板，不是所有未来功能的完整策略；没有 Stop/Start/Create/Delete/Allocate/Associate 等写权限，也没有云助手权限。不要为查询排错追加管理员策略。

权限来自已核对的 [API 元数据](api-evidence.json)。CMS 的真实 Action 是 cms:QueryMetricList，BSS 是 bss:DescribeAcccount / bss:DescribeBillList；不是直接按 API 方法名拼接。DescribeRegions 的公开元数据未列 RAM Action，因此模板没有凭空添加这一项，实际可访问性以只读调用结果记录。Resource 使用 `*` 以便发现地域/实例，未来可按账号实际资源收窄。

CDT 的完整公开定义仍未确认；工具新增了显式选择的固定只读探测，两账号现有凭据已读取成功，没有增加猜测的 RAM 权限。BSS 当前读取月度计费项，不能据此断言已确认小时水位。

## 2. 在你自己的终端导入

先编译命令，不需要重建前端：

```sh
cd /Users/jasperxzy/Projects/Server/harbormaster
go build -o deploy/bin/hm-server ./cmd/hm-server
./deploy/bin/hm-server m0 import --account jasperxzy
./deploy/bin/hm-server m0 import --account winnemjw
```

每次依次输入：账号站点 `china` / `international`、逗号分隔的地域列表、只读 AccessKey ID、AccessKey Secret、可选 STS Token（普通长期 AK 直接回车）。输入均不回显，不进入 shell 历史。站点必须按实际账号填写，程序不会根据昵称或机器位置推断。不要粘贴凭据到聊天；也不要用 `echo secret | ...`，导入会拒绝非交互终端。

默认位置：

- 加密账号文件：`~/.local/share/harbormaster-m0/credentials.enc`
- 独立主密钥：`~/.config/harbormaster/keys/m0.key`

账号名、站点、地域、AK ID、Secret、可选 STS Token 全部在 AES-256-GCM 密文中；成功输出只显示账号别名、打码 ID、文件路径和 `cloud_requests: 0`。首次导入生成随机 32-byte 主密钥；文件 0600，专用目录 0700。导入过程不联网，不做权限测试。

支持 `--vault` / `--key-file` 自定义路径。两者都拒绝 Git 仓库内路径及绕过边界的符号链接；主密钥不能放在账号文件所在目录内。已有 HM_MASTER_KEY 或 HM_MASTER_KEY_FILE 时使用该来源，不再自动生成；显式 --key-file 与环境密钥冲突则拒绝。默认文件来源完全不需要把主密钥打印或 export 到命令行。

第二次导入同名账号默认拒绝；需要替换时添加 `--replace`。替换使用加密后的临时文件和原子重命名，不产生明文临时文件；并发导入通过锁目录互斥。导入中断可能留有 `.lock`，确认没有导入进程后才手动移除，不自动抢占。已有库缺失/错误主密钥时失败，不生成新密钥覆盖旧库。备份时凭据密文与主密钥分开保管。

本机导入完成后，只需告诉后端“两个账号已导入”，或提供你自定义的文件路径。主密钥和 AccessKey 的值都不用发送。

只修改地域时无需再次输入凭据：`hm-server m0 set-regions --account NAME --regions cn-shanghai,ap-southeast-1,cn-hongkong`。它只更新加密库中的地域字段、保留账号凭据，在锁内原子保存，不联网。用户已授权并完成 jasperxzy 的这次扩展；winnemjw 保持上海。

## 3. 先查看计划，再只读查询

```sh
./deploy/bin/hm-server m0 plan --account jasperxzy
./deploy/bin/hm-server m0 query --account jasperxzy --check inventory
./deploy/bin/hm-server m0 query --account winnemjw --check inventory
```

plan 只解密本地元数据并输出计划，无网络；query 才发送 HTTPS 查询。工具有固定的操作/参数白名单及最终 HTTP 发送门禁，不接受任意 API 名、端点、请求参数或 DryRun。SDK 使用显式导入的凭据，不扫描其他阿里云 CLI 配置/实例角色。禁止重定向，不使用环境代理，每次请求最多 20 秒，串行节流 250ms，自动重试关闭。

inventory 在配置地域中查询 ECS、网卡、EIP、共享带宽、轻量实例；发现轻量 ID 后每批最多 50 个读取流量包。列表最多 50 页，遇到未知分页结构/截断不会标为完整成功。权限失败不会阻止其余读取尝试；得到空列表和查询失败是不同结果。ECS 规格、系统事件等未加入本批工具，不把 M0 的文档核对误认为已调用全部接口。

账单查询必须明确月份：

```sh
./deploy/bin/hm-server m0 plan --account jasperxzy --check billing --month 2026-11
./deploy/bin/hm-server m0 query --account jasperxzy --check billing --month 2026-11
```

读取余额、月度账单概览和按计费项的实例月账单。端点按账号站点选择，月度结果不提供可信的实时 cutoff；不会自行将 fetched_at 当成统计截止时间。

CMS 辅助指标需要已查到的真实资源 ID，以及配置地域中的单个地域。参数示例中的 ID 必须换成真实查询所得值：

```sh
./deploy/bin/hm-server m0 plan --account jasperxzy --check metrics \
  --region ap-southeast-1 --metric-source bandwidth --resource-id cbwp-REPLACE
```

将 plan 改为 query 才查询最近一小时。支持 eip 的 net.rx/net.tx，bandwidth 的 net_rx.rate/net_tx.rate；这里只获取辅助数据，不做流量额度保护。是否有数据、统计单位及实际延迟仍需真实账号验证。遇额外 NextToken 不擅自宣布结果完整。

`--check metric-schema --region ap-southeast-1` 读取这四个固定指标的元数据；已实测 EIP 单位为 B，共享带宽速率为 bit/s。结果现在包含 metric_name 以区分方向。

`--check cdt` 仅调用 ListCdtInternetTraffic：固定国内站端点、固定服务地域 cn-hongkong、RPC POST/HMAC-SHA1，经官方通用 SDK 签名。它不更改资源扫描地域，也不包含 OpenCdtService、SwitchToCdt 等写动作。调用方式来自 PRD 指定参考项目，读取性质由官方操作审计目录确认；真实响应已验证，但无可信 cutoff，不能作为接口长期稳定性的保证。国际站探测仍拒绝执行。

## 4. 输出与失败处理

默认 JSON 输出含 action、region、status、error_code、complete、fetched_at、脱敏响应页。也可用 `--output` 写到新的仓库外文件（0600），不覆盖现有文件。资源 ID、公网 IP、余额/账单仍属账户资料，应保存在仓库外；只把脱敏结论写入 docs/m0。

Secret、Token、Authorization、Cookie、签名、UserData 和原始错误 Message 等被移除，AccessKey ID 打码；还替换已知凭据在其他字段中的出现。网络/SDK 原始异常不直接打印，只保留经过校验的错误码。存在 SDK DEBUG 配置的进程拒绝 query，因为 SDK 的 debug hook 在包初始化时捕获配置，可能打印签名请求；需要退出后在未设置 DEBUG 的新进程运行，不能在进程内临时 unset 后继续。

返回码：0=所选查询完整通过；1=参数/凭据错误或至少一项查询失败/未完成。Forbidden/NoPermission 类记录 missing，其余错误保留 check_failed；不根据只读成功推断写权限。部分失败仍输出已经取得的脱敏记录。

## 已验证与未验证

已用本地假凭据检查：交互输入不回显、加密文件不含明文、独立密钥位置、重复导入保护、无网络 plan、SDK 签名请求经过固定 HTTPS/Action/Version 门禁、分页与 SWAS 后续查询、错误/响应脱敏。测试使用模拟 HTTP transport，没有真正联系云 API。

主要资源、BSS、CMS/CDT 真实查询可用性已取得首轮结果；这些结果不代表验证了所有权限、所有地域或长期数据准确性。已使用已有 SSH 配置做机器只读检查，没有安装软件、修改 nftables 或执行 V9 停开机实验。

实际命令与结果见 [credentials-validation.txt](credentials-validation.txt)：go build、go test -race、go vet、依赖校验、六平台 server 交叉构建均通过。伪终端验证覆盖提示出现时已关闭 ECHO、立即输入、成功退出、Ctrl-C 和 SIGTERM；最初测试发现的提示/关闭回显竞态已改为先进入 raw 模式再显示提示。构建好的 `deploy/bin/hm-server` 已包含这些命令。未提交或推送。

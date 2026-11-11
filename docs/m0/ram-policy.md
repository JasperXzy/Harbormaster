# 只读 RAM 策略草稿

2026-11-12。当前后端公开可核对的只读权限草稿位于 [deploy/ram/readonly.json](../../deploy/ram/readonly.json)。它没有开关机、续费、创建/释放资源、云助手或 RAM 管理权限。本轮只生成仓库文件，未向任何真实 RAM 身份授予权限。

原 m0-readonly.json 保留为初始 M0 范围的记录。新草稿在其基础上补充两个当前实际读取所需的动作：

| RAM Action | 用途 | 官方来源 |
|---|---|---|
| ecs:DescribeInstanceAutoRenewAttribute | 包年包月 ECS 续费策略 | [ECS 自动续费属性查询](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-describeinstanceautorenewattribute) |
| bss:DescribeInstances | 通过 QueryAvailableInstances 匹配轻量实例续费记录 | [BSS 可用实例查询](https://help.aliyun.com/en/user-center/developer-reference/api-bssopenapi-2017-12-14-queryavailableinstances) |

余额查询仍使用官方命名 bss:DescribeAcccount（三个 c），账单使用 bss:DescribeBillList，CMS 数据查询使用 cms:QueryMetricList；不能把 API 方法名机械地当作 RAM Action。其他既有来源见 [接口证据矩阵](api-table.md)。

该 JSON 是已知动作集合，不是“完整且已实测的最小权限方案”：

- ListCdtInternetTraffic 的 RAM 动作仍缺少公开确认，不添加猜测的 cdt 动作或 cdt:* 通配授权。
- DescribeRegions / DescribeInstanceTypes 的既有公开元数据没有列出 RAM Action；没有据此宣称它们一定不需要权限，也没有凭 API 名添加未确认动作。
- Resource 使用 `*` 以覆盖账号资源列表与不支持资源级约束的查询；这不是已按实例或地域 ARN 收紧并验收的策略。
- 现有真实查询使用此前导入的凭据，不证明仅拥有此模板的独立 RAM 身份能完成全部查询。完整验收需要用户准备仅绑定该策略的只读身份，并在已有安全导入流程中提供其仓库外来源，不能在聊天发送密钥。
- 含开关机版、EIP/创建版及其关联资源权限尚未提供。它们属于 F1.4 后续工作；没有本模板就绪便认为整个 F1.4 完成，也没有将写权限模板等同于执行真实云写操作的授权。

本轮 JSON 解析检查通过：11 个动作均为显式只读动作，与旧草稿的差集恰为上面两项，未出现 Action 通配符或云写动作。没有修改 Go 代码，因此没有重复执行上一阶段已通过的全量 Go 测试。

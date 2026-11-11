# 生产主密钥与数据库绑定

2026-11-12。修复了“另一把格式正确的主密钥可被旧数据库启动流程接受”的问题。原先只能检查密钥格式和文件位置，不能证明它能解密该数据库中的凭据；继续创建账号或渠道可能产生混合密钥的密文。

## 当前行为

生产命令在打开数据库/执行结构迁移之后、初始化向导或业务 worker 启动之前校验主密钥。serve、run-once、migrate、import-accounts、reset-password 共用此检查；不通过时退出并只记录固定错误码 master_key_verification_failed。

新库在 credential 表建立 AES-256-GCM 加密验证记录，AAD 为 `system/master-key-verifier/v1`。记录不保存主密钥明文、编码值或可导出的密钥。以后启动只核对该记录；实际读取各凭据时仍分别验证 GCM 认证。

已有库尚无验证记录时，先用当前密钥及各 owner_field 认证全部 credential 行，并认证 Telegram 渠道配置。全部成功才在同一事务中建立绑定。错误密钥、混合密钥或不能认证的旧密文都不会建立/覆盖绑定；现有凭据不重加密、不重写。

验证失败可能来自错误密钥或损坏的数据，不能仅凭这一错误断言是哪一种。恢复正确密钥后可再次启动；更换密钥文件不是密钥轮换。空库第一次生产启动也会绑定所用密钥，若要重新初始化，应明确选择新的数据目录。初始化密钥选择必须由操作者保管好。

开发模式仍使用独立示例库。healthcheck/version 不需要访问数据库里的秘密；M0 独立加密 vault 使用自己的既有认证机制。本批没有新增远端操作、没有读取真实凭据进行测试，也没有修改面板契约。

## 实际验证

先添加 CLI 回归测试并在修复前执行：

```text
go test ./internal/cli -run TestProductionCommandRejectsWrongMasterKeyBeforeWriting -count=1
FAIL: production command accepted a key that cannot decrypt existing credentials
```

随后修复并执行：

```text
go test -race ./internal/store ./internal/cli ./internal/notify
ok internal/store 5.749s
ok internal/cli   2.849s
ok internal/notify 1.773s

go build ./...
exit 0

go test -race ./...
exit 0
ok internal/api 25.420s
ok internal/collect 2.261s
ok internal/devmode 5.723s
其余测试包 cached / passed

go vet ./...
exit 0

git diff --check
exit 0
```

覆盖新库绑定、错误密钥、损坏验证记录、旧账号凭据/Telegram 配置的首次绑定、混合密钥拒绝、正确密钥恢复和错误日志脱敏。全部使用临时数据库与假密钥。

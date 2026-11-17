# agent 安装包发布与错误修复

2026-11-18，Asia/Singapore。用户在 sg-server-nano 安装弹窗获取命令时遇到 `agent_release_unavailable`。main-server 的后端健康、发布目录只读挂载正确，但目录为空：上次面板部署只创建了目录，没有放入安装包。接口按设计在文件检查通过前拒绝签发注册码，因此返回 503。这是发布遗漏。

## 实际修复

从与线上后端一致的 `103f3a9a3856dd678ea8fbe8ce5716306e27d32d` 创建仓库外源码快照，使用现有构建脚本生成 `v0.1.0-trial.20261118` 发布包。没有修改 Go、接口契约、前端或安装脚本。

发布到已挂载的 `/opt/harbormaster/agent-releases`：

- hm-agent-linux-amd64
- hm-agent-linux-arm64
- hm-agent.service
- install.sh
- VERSION
- SHA256SUMS

发布前检查两个二进制为相应架构的 64 位 Linux ELF，逐项验证 SHA256SUMS；在 main-server 只执行 amd64 二进制的 `version` 固定动作，返回正确版本。该动作不读写 agent 状态、不注册、不创建计数表或服务。

保留只读 bind mount 指向的目录本身，在目录内逐文件原子替换，最后发布 SHA256SUMS。没有用重命名整个目录的方式让容器继续看到旧 inode，也没有重启后端。文件由 root 持有、0644；后端仍以只读挂载读取。

清单摘要为 `f99d8e450c5a9fc5ff1d382a8a42530fbdf7af714cc602546c42e2f8b2d7874f`。完整来源、文件摘要和下载记录见 [agent-release-runtime-evidence.json](agent-release-runtime-evidence.json)。服务器发布记录位于 `/opt/harbormaster/releases/agent-v0.1.0-trial.20261118`。

## 验证结果

```text
（独立源码快照）sh deploy/build-agent.sh <仓库外发布目录> v0.1.0-trial.20261118
exit 0，无输出

go test ./internal/agentrelease ./internal/api -run 'Test(ReleaseMustBeCompleteAndChecksummed|DownloadsNeverEscapeReleaseRoot|CommandIsValidShellAndPassesCodeOnStdin|InstallerRestoresFilesButRetainsStateAfterFailedUpgrade|VersionMetadataMustBeValidAndChecksummed|VersionCacheExpiresAndRechecksChangedDirectory|InstallCommandChecksArtifactsBeforeIssuingGrant)$' -count=1
ok github.com/JasperXzy/harbormaster/internal/agentrelease 4.718s
ok github.com/JasperXzy/harbormaster/internal/api 0.594s
exit 0

（main-server，原始 curl User-Agent，开启 TLS 校验）
GET https://cdt.jasperxzy.com/agent/v1/releases/{固定发布文件名}
六个文件全部 200，下载字节的 SHA-256 全部匹配发布清单
```

通过 Go 后端的源站下载路径读取 VERSION，也得到新版本，确认正在运行的容器能看到发布内容。容器保持 healthy，电源/释放环境开关仍为 false。

现有 API 测试验证缺失/损坏文件不签发注册码、有效发布返回 201、CSRF、版本提示与安装脚本恢复流程。本次没有为验证而签发真实实例的注册码，没有读取管理员会话或向日志写入安装命令。实际生产请求的 201 留给用户在弹窗“再试一次”时产生，避免替换其正在使用的一次性注册码。

## 使用与边界

用户在原弹窗点击“再试一次”，重新复制命令。安装命令包含一次性秘密，不应粘贴到对话或调试日志。版本提示缓存最长 30 秒；安装命令接口每次直接校验文件，不依赖此缓存。

本轮只修复 main-server 上的发布与下载；未登录 sg-server-nano、未执行安装、未修改目标主机的服务或网络，也未触发任何云写操作。实际 Linux 内核计数、真实安装和 V7 的 24 小时偏差仍需对应验收，发布成功不代表计量验证通过。

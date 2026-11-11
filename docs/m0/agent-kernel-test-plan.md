# 隔离 Linux 内核计数验证申请

2026-11-12。安装脚本已交付，但主机原生 nftables 仍未执行。本计划先在 main-server 的独立网络命名空间验证内核计数，不依赖面板公网域名、真实账号凭据或服务安装。

## 已准备产物

源码：`internal/agentmeter/native_linux_test.go`。命令：

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c \
  -o deploy/bin/hm-agent-kernel-test ./internal/agentmeter
```

本机交叉编译实际 exit 0。程序默认跳过内核测试，只有显式 `HM_AGENT_KERNEL_TEST=1` 且自身网络命名空间与 PID 1 不同时才运行；同命名空间直接拒绝，避免误改主机网络。

测试创建一对 veth、测试用私网地址、静态路由/邻居和专用计数表；通过构造的小 UDP 包验证 IPv4/IPv6 公网入出计数、私网排除、进程重开继承计数、表重建生成新 epoch、同名表被修改时拒绝覆盖，以及删除本工具表时保留其他表。不会读取主机业务数据或产生公网压测流量。包仅在新命名空间内部的 veth 之间传递。

## 拟执行动作（等待用户同意）

1. 通过已有 SSH 别名将测试二进制上传到 main-server 的 `/tmp/hm-agent-kernel-test-9017ae1`。
2. 以如下固定命令在新网络命名空间运行，记录测试结果；不启动常驻服务：

```sh
sudo -n unshare --net -- env HM_AGENT_KERNEL_TEST=1 \
  /tmp/hm-agent-kernel-test-9017ae1 -test.run '^TestKernelCounters$' -test.v -test.timeout 30s
```

3. 删除上传的临时二进制。测试进程退出后独立命名空间自动销毁，其中的 veth、地址、路由、邻居和 nftables 表随之消失。不调用主机上的全局 flush，不改主机网卡/防火墙，不读取 AccessKey/主密钥/Token，不调用阿里云接口。

允许测试不等于允许安装 agent、部署面板、改 DNS 或执行真实云写操作。若内核规则测试失败，将在本地修复、重新交叉编译并在相同隔离范围重试。

## 不能据此证明的事项

这不是 V7 的 24 小时真实业务计数验证；不证明容器/NAT/转发/offload 场景、CDT 时间对齐或 ≤5% 偏差，也不验证目标 sin-pro/hkg-pro 的实际内核环境。上述验收仍需后续明确授权和真实环境。

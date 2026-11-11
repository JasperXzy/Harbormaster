# V9：节省停机后的 IP、恢复与错误码

**状态：需要真实环境验证。**

## 方法与实际结果

核对[StopInstance](https://help.aliyun.com/en/ecs/developer-reference/api-ecs-2014-05-26-stopinstance)、[节省停机说明](https://help.aliyun.com/zh/ecs/user-guide/economical-mode)及 StartInstance 官方元数据。文档确认 EIP 保持，实例系统分配公网 IP 可能改变，私网 IP 保持；释放计算资源后可能因库存不足无法启动。还确认不支持节省模式时可能普通停机成功而不报错，因此“请求成功”不代表实际进入节省模式。错误码列表含 `LackResource`、`OperationDenied.NoStock` 等，但本轮没有观察真实错误。

## 结论与 PRD 修订建议

F6.2 增加：停机后查询 StoppedMode，区分成功进入节省模式与回退普通停机；重启后重新发现 IP，错误码分类保留未知项。F6.5 不得将异步开机请求成功当作实例已运行。实际 IP 变化、失败概率、耗时均未测得。

## 需要的条件

这项实际实验必然包含 StopInstance/StartInstance，**本轮只读约束下不执行**。后续需用户另行明确授权具体实例、时间窗、允许的停机模式与恢复措施，并提供受限写权限；不能仅因获得 AccessKey 就认为获准停机。可先只读核对实例是否满足节省模式条件。不要为制造错误码而释放/创建实例或耗尽库存。

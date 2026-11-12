# 离线 IP 数据库与资源验证

2026-11-13；PRD F10.7 / F10.8 / F10.11 的数据基础。本阶段交付内置 ASN/国家库、可选城市库、离线读取组件、月度更新和状态接口；逐跳结果的持久化归属信息、PTR 和线路标签接入仍待后续阶段。没有执行真实公网线路测量，也没有使用云助手。

## 用户已确认的取舍

1. 默认内置 ASN＋国家级地理库，城市级数据作为可选离线增强。避免在默认二进制中加入约 60MB 的城市压缩库，并保留 PRD 默认常驻内存 ≤100MB 的目标。城市模式需要单独观察资源占用。
2. 线路标签按证据范围显示：AS4809 标为 CN2，GIA/GT 待确认；AS45102 仅标阿里云网络，是否跨境待确认。国家/城市序列展示数据库估计位置，不直接据此认定绕行。原 PRD 未修改。

## 数据来源和许可

采用 [DB-IP Lite](https://db-ip.com/db/lite.php) 的 November 2026 官方月度 MMDB，许可为 [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)。提供方要求使用结果的网页显示指向 DB-IP 的来源链接。后端在 `/network/ipdb` 返回 attribution；Claude 在显示归属数据的页面接入可见链接，后续文本导出也应保留来源。

仓库 `internal/ipinfo/data/` 保存未修改的 ASN/Country gzip 文件、来源/哈希 manifest 和许可说明；许可说明也嵌入 server。City 不进入仓库或默认二进制。新增 reader 依赖锁定 `github.com/oschwald/maxminddb-golang/v2 v2.9.0`，未升级其他依赖；随包保留其 ISC 许可。

| 数据库 | 压缩字节 | 展开字节 | 使用方式 |
|---|---:|---:|---|
| ASN Lite | 5,237,630 | 9,648,279 | 默认内置 |
| Country Lite | 4,205,904 | 8,515,063 | 默认内置 |
| City Lite | 60,125,343 | 126,873,350 | 显式启用后下载/缓存 |

默认两库合计压缩 9,443,534 字节、展开 18,163,342 字节。首次加载检查 gzip 完整性、展开大小、内置 SHA-256、MMDB 类型/结构，之后复用不可变数据；未命中或字段缺失保留未知，不填入推定 ASN/城市。

实测 1.1.1.1 的 ASN 为 13335、国家库给出 AU；8.8.8.8 为 15169/US；2606:4700:4700::1111 为 13335/US。59.43.1.1 的 ASN 库没有记录，国家库返回 CN。不能从 IP 名称或常见号段强行补成 AS4809。Anycast 地址的库位置不代表本次报文经过的物理节点。

官方 ASN 格式页面的示例实际上列了 country 字段；真实 ASN 文件实测是 autonomous_system_number / autonomous_system_organization。本实现以实际文件与类型校验为准。城市字段来源见[官方 MMDB 格式](https://db-ip.com/db/format/ip-to-city-lite/mmdb.html)，国家/省/市和经纬度分别保留缺失状态。

## 读取与更新

`internal/ipinfo.Manager.Lookup` 仅查本地内置或缓存的 MMDB，不发 HTTP/DNS。只为普通公网地址提供归属；私网、回环、链路本地、元数据和保留/文档地址返回 not_public。AS 和地理结果独立命中，均保留匹配前缀及数据库版本；国家模式没有省、市或坐标。城市库提供的是近似位置，不作为保护或路由等级的事实来源。

配置项：

- `ipdb_city_enabled` / `HM_IPDB_CITY_ENABLED`：默认 false。为 true 时读取已缓存 City；自动更新启用时补下载。缺少 City 仍使用国家库，状态返回 city=null、mode=country。
- `ipdb_auto_update` / `HM_IPDB_AUTO_UPDATE`：默认 true。仅生产 serve 启动后台检查；每 24 小时检查一次，只有缺库或已到新月份才下载固定 DB-IP 月度文件。同月不重复下载，系统时间回退不降级。关闭后保持内置/既有缓存，离线查询仍可用。

更新请求只获取数据库文件，不上传待查询 IP。压缩单文件上限 96MiB，展开上限 256MiB，单请求最长 2 分钟、整批最长 5 分钟。拒绝非 200、截断/损坏 gzip、错误数据库类型及与请求月份不符的数据。全批验证后以原子 manifest 发布；失败保留整套旧数据，不混合半批。进程间文件锁防重叠发布，读取锁避免 mmap 关闭与并发查询冲突。缓存损坏回退内置库，并允许后续有效下载修复。

缓存位于数据目录的 ipdb/，是可再生的公共数据，不含凭据。更新后清理不再引用的旧缓存代际；不会改动 SQLite、主密钥或 agent 数据。程序退出取消更新并等待 worker 后再关闭读取器。run-once、migrate 和开发模式不启动下载 worker。

## 状态接口

`GET /api/v1/network/ipdb` 使用管理员 Session，读取内存状态，不触发更新。返回各库的版本、构建时间、字节数、bundled/downloaded 来源、memory/mmap 存储方式、来源 URL、城市开关、更新开关、最近检查/发布/错误及 attribution。组件不可用时为 503 ipdb_unavailable；错误不透传 HTTP/SDK 正文。

资源字段分别是当前 Go 堆、操作系统报告的进程峰值 RSS（不支持时 null）、映射文件总字节。三者不能互相替代；RSS 是整个进程的峰值，不是当前常驻值或库独占内存。开发模式提供同一内置库的只读元数据，无需真实阿里云账号。

## 资源实测与边界

本机 macOS，Go 1.27.1。下表是独立读取组件/测试进程，尚未包含完整面板负载，不能据此宣称整个 hm-server 已达到 100MB 长期运行验收。

| 方法 | 峰值 RSS 字节 |
|---|---:|
| 直接 mmap ASN＋City，1 万个不同 IPv4 查询 | 70,467,584 |
| 直接 mmap ASN＋Country，同样查询 | 17,121,280 |
| 实现的内置 ASN＋Country，加完整校验和测试框架 | 61,669,376 |
| 实现启用 City，并做全库结构校验 | 252,805,120 |

最后两项用预先编译的测试二进制运行，`/usr/bin/time -l` 不包含 Go 编译器。默认测试的 Go heap 实测 27,296,704 字节；城市库测试读到 US / Mountain View，国家级模式不会输出该城市。完整城市校验存在明显的瞬时开销，默认保持关闭，状态接口如实报告峰值。

PRD 修订建议：F10.11 明确“默认国家级＋ASN，城市级可选且资源预算独立”；F10.8 区分 ASN/前缀证据与服务等级、跨境/绕行结论。用户已确认这两项实现方向。离线国家/城市数据不证明物理路由，不能用它决定停机或实时额度保护。

## 验证结果

本地测试覆盖 IPv4/IPv6、真实 ASN 缺口、私网/元数据拒绝、国家模式不补城市、并发查询与更新、同月零下载、错误更新不半发布、损坏缓存回退/修复、缓存路径检查、进程间更新锁及取消。HTTP 接口测试验证管理员鉴权和 GET 零网络工作；普通 CI 不下载数据库。

另以显式 opt-in 运行了真实 City 文件读取，以及本实现对官方 ASN/Country 文件的真实下载、校验和缓存发布。这些都是公共数据读取，不是阿里云资源操作。

```text
go test -race ./internal/ipinfo
ok github.com/JasperXzy/harbormaster/internal/ipinfo 10.055s

HM_TEST_CITY_MMDB=<已下载的官方 City 文件> go test ./internal/ipinfo -run TestCityFileIntegration -v
PASS；city=2026-11，126873350 bytes，US / Mountain View

HM_TEST_IPDB_LIVE=1 go test ./internal/ipinfo -run TestLiveMonthlyDownload -v
PASS；下载 ASN=2026-11 country=2026-11，映射字节 18163342；6.431s

go build ./... && go vet ./... && git diff --check
exit 0，无输出

./web/node_modules/.bin/redocly lint docs/api/openapi.yaml
契约有效，6 条既有警告，exit 0

go test -race ./...
exit 0，全部通过；此次重跑：agentclient 8.554s、api 98.042s、cli 17.467s、
config 1.327s、devmode 13.156s、ipinfo 12.690s；其他 cached 或 no test files。
```

实际交叉构建使用 `CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -o <临时路径> ./cmd/hm-server`，Linux/macOS/Windows 各 amd64/arm64 全部通过；Linux agent 两架构也通过。server 为 34.9–37.0MB，agent 为 11.1–12.0MB；产物在临时目录 hm-ipdb-build-0rx_ztav，未部署。编译通过不代表相应 OS 的运行验收。

补充嵌入许可文本、细化文件锁错误分类后，`go test -race ./internal/ipinfo` 再次通过（10.035s），build/vet/diff 检查通过，Windows amd64 编译再次通过。公开接口和数据内容未改变；没有重复运行无关测试。

## 依据

- [DB-IP Lite 许可及更新](https://db-ip.com/db/lite.php)、[Country 下载](https://db-ip.com/db/download/ip-to-country-lite)、[City 下载](https://db-ip.com/db/download/ip-to-city-lite)、[ASN 下载](https://db-ip.com/db/download/ip-to-asn-lite)。
- [实际采用的 Go reader](https://github.com/oschwald/maxminddb-golang/tree/v2.9.0)：只读数据生命周期、校验、并发和版本接口；同时核对下载到模块缓存的源码。
- [APNIC AS4809](https://rdap.apnic.net/autnum/4809)、[AS4134](https://rdap.apnic.net/autnum/4134)、[AS45102](https://rdap.apnic.net/autnum/45102)、[AS9929](https://rdap.apnic.net/autnum/9929)、[AS4837](https://rdap.apnic.net/autnum/4837)、[AS58453](https://rdap.apnic.net/autnum/58453)：实际只读返回分别为 CN2、CHINANET、ALIBABA-CN-NET、CUII、China169、CMI-INT-HK。注册信息不能证明某条路径的等级或跨境状态。

# RayBox-rx：Xray-RX 阶段 1 历史验证

本文为历史记录，文内“未提交”和artifacts证据路径指原阶段工作区，原始日志与产物不在本源码仓库。当前发布检查和已知失败见根 `VALIDATION.md`，不要把历史通过结果当作最新快照全绿。

日期：2026-10-29。基线 Xray-core v26.10.29 (`0afec66085a85191e9dd2a4a6646c7c9a94688d1`)，Go 1.27.3，Windows amd64。实验功能默认关闭。代码未提交、未推送、未部署到生产。

最新现场更新：指定俄罗斯机经 CF `104.17.1.1:2083` 到英国源站，普通 XHTTP 未启用 RX，使用 x25519、原自有 SNI/Host 与正常证书验证，单次完整 1GiB 已通过：28.095 秒、36.447 MiB/s，字节数及 SHA-256 一致，curl 0；源站 request_id 13 完整写出。已只读核对另一会话的三份原始证据，没有重复下载。该结果不证明 RX 解决受限入口冻结；`104.21.44.55` 上的 uplink2 仍为 600.007 秒仅 4096 字节。私有 1KiB 有限响应仅完成 64KiB 前缀，65.141 秒，未合入默认源码。详细口径、抓包、配置与限制见 [Cloudflare/俄罗斯现场报告](xray-rx-field-20261029.md)。

历史 phase1b 在相同本地 50MiB 实验中将 TCP 从 7,225 降至 3,651，耗时从 12.595 秒降至 7.081 秒；Linux 已在两台 Debian 实际运行。英国经 CF 的历史 1GiB、3 次主动断线对照通过，此后不再重跑。下文 phase1 数值保留为原始基线，不代表最新代码或受限入口成功。

2026-10-29 本轮接手后的首次定向回归：`go test ./transport/internet/splithttp ./transport/internet/tls ./infra/conf -run '^(TestRX|TestXrayRX|TestCertificateReloadPublishesImmutableSnapshots)' -count=1 -timeout 180s`，三个包均通过（3.153s / 0.040s / 0.039s）。该次未设置 `XRAY_RX_LONG_TEST`，没有运行全量、race 或重新构建 Xray，不把旧日志算作该次结果。

随后针对俄罗斯现场上行超时实现双方协商的上行重试。运行 `go test -race ./transport/internet/splithttp -run '^(TestRXUplinkReliability|TestRXIntegrationCompatibility|TestRXIntegrationGETHeaderHasNoBody|TestRXIntegrationUploadWhileDownlinkResumes|TestRXClientReconnectBudget|Test_regression_readzero)$' -count=1 -timeout 90s`，旧端安全修复后的最新源码通过（1.406s）。新增一个测试入口覆盖 H1/H2 的请求丢失、已部分消费后的 ACK 丢失、新 TCP、同包去重、错误确认、旧 RX 对端、取消、共用预算、队列关闭，以及无 ServerHello 的 RX 原生 TLS 截止和连接释放；没有重跑全量或长测。独立 Linux 产物与真实线路结果见现场报告。

复核中曾有一次检查失败：新增断言要求普通 HTTPS 的 `OpenStream` 在 TLS 握手前返回。Go 1.27 的 `net/http` 本就会在 `GotConn` 前调用自定义连接的 `HandshakeContext`，上游原生 TLS 也遵循此流程。已将断言改为直接检查非 RX 原生 TLS 拨号仍返回惰性连接，不为该错误断言修改普通 HTTP 流程；该修正阶段通过 1.412s，随后旧端安全修复版通过 1.406s。

当前增量仅涉及 `client.go`、`dialer.go`、`hub.go`、`rx_client.go`、`rx_server.go`、`upload_queue.go` 和现有 `rx_integration_test.go`。公共拨号处仅对 RX 启用的原生 TLS 增加显式 `HandshakeContext`；uTLS 保留既有显式握手，两者失败均关闭 TCP，普通非 RX 原生 TLS 保留原惰性拨号。上传序列状态在会话独立锁中推进，不持有全局会话锁等待入队；队列发送后复查关闭状态，新包失败不确认，最近已成功入队的同包仍可补确认。未改 protobuf、配置结构或其他协议。旧 RX 未协商上传重试时也改用专属单次请求，禁止重定向以保护 capability，并在超时后释放未完成握手的连接；没有给旧端增加重试或 seq/nonce 确认要求。检查覆盖 302 不跟随及旧 RX 实际 `PostPacket` 无 ServerHello 后关闭 TCP。上述复核和旧端安全修正尚未包含在现场 `uplink2` 二进制中。

本次七个修改文件经固定版本 gofumpt v0.12.0 格式检查通过，`git diff --check` 通过。全仓 `vformat -mode check` 失败，列出大量本次未修改文件；没有批量写回。直接 `go run mvdan.cc/gofumpt` 因 CLI 额外依赖缺少 go.sum 项失败，改为带明确版本的独立运行，未修改 go.mod/go.sum。

## 实现与文件

| 位置 | 改动 |
|---|---|
| `infra/conf/transport_method.go`, `xray_rx_test.go` | `rx` JSON、参数上下界、配置/序列化测试 |
| `transport/internet/splithttp/config.proto`, `config.pb.go`, `rx_config.go` | 配置生成、启用范围及保留头校验 |
| `rx_buffer.go`, `rx_buffer_test.go` | 固定环形回放缓存、ACK 释放、满时反压、关闭与取消 |
| `rx_server.go`, `rx_server_test.go`, `hub.go` | 独立逻辑会话、capability、offset/generation、TTL、并发/会话上限 |
| `rx_client.go`, `rx_client_test.go`, `client.go`, `dialer.go` | 专属底层连接、ACK/探测、停滞判断、重连预算、首字节恢复统计、协商降级 |
| `rx_integration_test.go` | 真实 TCP 故障代理、hash、并发/上传/空闲、资源和 JSON 计量 |
| `transport/internet/tls/config.go`, `certificate_reload_test.go` | 本次大量握手暴露的原有证书热更新竞态修复与回归测试 |
| `splithttp_test.go` | 原 QUIC 测试跨 goroutine bool 改用 atomic，消除测试本身的数据竞争 |
| `scripts/xray-rx-smoke.py` | 实际二进制 SOCKS→VLESS→XHTTP→HTTP 的环回故障验收 |

没有新增 Go 模块依赖。TLS 热更新修复把证书与 OCSP 更新改为不可变快照的原子发布，每个更新器持有独立配置副本；`GetTLSConfig` 保留热更新，公开 `BuildCertificates` 签名不变但返回稳定快照。该故障在未修改的上游源码已通过 `Test_ListenXHAndDial_QUIC -race` 复现，记录在 `artifacts/baseline-quic-race.log`。

## 已运行检查

- 修改前：`go test ./transport/internet/splithttp ./infra/conf` 通过。
- 修改后：`go test -race ./transport/internet/splithttp ./transport/internet/tls ./infra/conf -count=1 -timeout 180s` 全部通过，日志 `artifacts/final-race.log`。
- protobuf：`go run ./infra/vprotogen -mode check` 通过。
- 格式与补丁：`gofmt`、`git diff --check`。
- 编译：Windows amd64 可执行文件；Linux amd64 交叉编译（未在 Linux 上运行）。
- `go vet` **并非全绿**：上游 XMUX protobuf 按值复制锁警告，以及 `infra/conf/xray.go:674` 不可达代码；在未修改基线复现完全相同告警，保存 `artifacts/baseline-vet.log`。没有为消除历史告警扩大重构范围。

短集成包含每条 TCP 16384 字节后 FIN、RST、静默冻结；H2+TLS 也在同一字节阈值下成功。阈值计入经过 TCP 转发器的 HTTP/TLS 字节，不把 HTTP 请求或 Go Write 次数当作连接/网络数据包。实验下行每 4KiB Flush，避免一个完整大 H2/TLS 单元跨阈值而完全无法交付；若预算连握手都容不下，测试验证有界失败。

兼容测试覆盖双端关闭、仅客户端开、仅服务端开、双端开；协议测试覆盖错误 offset/nonce/version、部分头丢失、恢复后头丢失、过期、跨 token、旧代关闭、缓存反压、取消、最终 offset 与 EOF。GET Header 上行检查无 Content-Length 非零或 Transfer-Encoding，读取请求 Body 为空。还验证了 4 并发下载，以及 512KiB 持续上传期间 128KiB 下行恢复。上述检查不等于线上抓包。

## 50MiB 真实 TCP 故障模拟

同一 Windows 本机进程中的客户端、服务端、TCP 转发器和测试；H1 明文；每条 TCP 下行 16KiB 后 FIN。毫秒级测试参数：probe=20ms、stall=80ms、request=500ms、retry=1ms，长测显式将生命周期重连预算提高到 8192。不是生产推荐参数。

| 指标 | 实测 |
|---|---:|
| 应用字节数 | 52,428,800（50MiB） |
| SHA-256 | `0fb55a02577e00290105ccdadcde65bc4269037fb578cd21096d9487fc97eb45` |
| 完整性 | 完全一致 |
| 耗时 / 吞吐 | 12.5951s / 3.9698MiB/s |
| 注入中断 / 重连尝试 / 成功恢复 | 3297 / 3297 / 3297 |
| 恢复 p50 / p95 | 3.119ms / 7.7131ms |
| 客户端实际 TCP（含控制） | 7225 |
| 每 MiB TCP / 每十进制 MB TCP | 144.5 / 约 137.81 |
| 客户端方向传输下行 / 上行字节 | 55,520,605 / 6,664,757 |
| 相对应用下载的额外 socket 字节 | 约 18.61%（上下行合计） |
| 每会话回放缓存上限 | 262,144 字节 |
| 采样 Go HeapAlloc / Go Sys 峰值 | 3,576,960 / 21,637,368 字节 |
| 测试进程采样工作集峰值 | 25,976,832 字节 |
| 测试进程采样 CPU 时间 | 13.953125s |

证据为 `artifacts/research/metrics/TestRXIntegration50MiB.json`、`process-50mib-final.json` 和 `rx-50mib-final.log`。恢复从“客户端观察读取异常或触发取消”计到“新连接首字节交付”，**不含故障发生到检测之间的等待**；静默冻结的总中断时间还包含 stall/probe 时延。测试未模拟 WAN RTT、TLS 握手（此 50MiB 为 H1 明文）、网络丢包、CDN 排队或限速，不能外推公网吞吐。

流量数字只覆盖代理客户端侧 TCP socket 字节，未包含 IP/TCP 头、TCP 重传、CDN 回源预取/丢弃字节，不能直接作为 CDN 账单开销。Go 内存每 5ms、进程资源每 25ms 采样，可能漏掉更短峰值；是客户端+服务端+故障代理+测试合计，不是独立服务端 RSS，且不含内核 socket 内存。CPU 是累计 CPU 时间，可因多核超过墙钟时间。

## 并发资源小样本

每会话 2MiB，同样 16KiB/TCP FIN，各组独立进程；所有 hash 一致。

| 指标 | 1 会话 | 4 会话 |
|---|---:|---:|
| 下载总量 | 2MiB | 8MiB |
| 传输墙钟时间 | 0.3767s | 0.5022s |
| TCP 总数 | 284 | 1160 |
| Go HeapAlloc 峰值 | 3,050,056 B | 3,743,624 B |
| 进程工作集峰值 | 22,507,520 B | 28,721,152 B |
| CPU 采样累计时间 | 0.390625s | 3.109375s |

此样本仅说明资源随并发变化可测，不能证明几十并发或 1000 DAU 容量。

## 实际 VLESS 双进程验收

另外用编译出的 Windows Xray-RX 启动两个独立进程，执行 SOCKS→VLESS→XHTTP GET Header→本地 HTTP 文件下载；Python TCP 代理同样每连接 16KiB FIN。保持同一个应用 HTTP 请求，不依靠下载器重试。

50MiB 完整下载 SHA-256 为 `624bbe3f61588f97cfaad1af50360bb8c5fc94774d3c15dbf471dcd42b9bea8e`（此脚本使用另一套确定性数据，故与 Go 测试 hash 不同），3393 次截断、9310 条 TCP、50.594s，约 0.988MiB/s。客户端侧 socket 下行 60,361,179B、上行 12,627,855B。证据：`artifacts/smoke-50m/result.json` 和两端日志。此结果包含真实 VLESS 与跨进程 Python 代理成本，不应与前面单进程 Go 测试混为一组。

最终脚本还以 1MiB 验证携带 `data-0` 的 GET，其实际客户端→源站请求字节数等于 HTTP 头部长度，且无非零 Content-Length 或 Transfer-Encoding；证据 `artifacts/smoke-1m-final/result.json`。该复测 67 次恢复、175 条 TCP，首字节恢复 p50/p95 为 4/31ms。它仍是环回原始 TCP 观察，不是 Cloudflare 线上抓包。最终重新构建的 Windows 二进制 SHA-256 与实测版本完全一致：`9980c80d64edfdd4d34b45f08a3904f50e55c964cd32672ac235b48f7beafa5e`。

上游当前默认阻断 VLESS 到私网目标；验收配置仅为本地源站的 `127.0.0.1/32:临时端口` 加显式允许，其他目标继续拒绝。生产策略没有修改。

## 可复现命令与未完成范围

```powershell
$env:XRAY_RX_LONG_TEST = '1'
$env:XRAY_RX_METRICS_DIR = Join-Path (Split-Path (Get-Location) -Parent) 'artifacts/research/metrics'
go test ./transport/internet/splithttp -run '^TestRXIntegration50MiB$' -count=1 -v -timeout 180s
```

阶段 1 已提供下行恢复原型和本地故障证据。阶段 2 尚需：真实双向 IP 包数量触发、长时间 idle/上传与随机断线组合、WAN RTT/丢包、进程分离资源曲线、更高并发、50MiB H2/TLS 冻结长测及真实 CDN 路径。服务端进程重启不支持无损恢复，上行连接冻结仍会结束会话。

阶段 1 交付时的历史状态为：本地模拟通过，国内 Cloudflare 与俄罗斯实测尚未执行。其后俄罗斯实验及普通 XHTTP 完整 1GiB 成功已在本文开头和现场报告更新，不把此历史缺项当作当前状态。受限入口 RX 完整 1GiB、并发容量、PacketDown、多路径及轮换自适应仍未验收或未实现。

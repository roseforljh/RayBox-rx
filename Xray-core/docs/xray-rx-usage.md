# RayBox-rx：Xray-RX 实验构建、使用与回滚

该补丁基于 Xray-core v26.10.29，包含实验性下行恢复及双方协商的有限上行重试。仅本地故障验证通过不能代表国内 Cloudflare 或俄罗斯运营商实测通过。未经实测不要替换生产内核。

指定俄罗斯机 → CF → 英国源站的单次完整 1GiB 已通过：固定 `104.17.1.1:2083`，普通 XHTTP 未启用 RX，原生 TLS 使用 `curvePreferences=["x25519"]`，自有 SNI/Host 不变且正常验证证书；28.095 秒、36.447 MiB/s，1,073,741,824 字节与 SHA-256 均一致，curl 0。该成功属于普通 XHTTP 可用入口，不能归功于 RX，也不外推整个网段或所有运营商。

受限入口 `104.21.44.55` 的 uplink2 RX 1GiB 仍失败：600.007 秒仅 4096 字节，83 次重开。私有 overlay 的 1KiB 有限响应在 65.141 秒完整收到 64KiB 前缀，但不是完整 1GiB，且约百万连接才能完成 1GiB，超过预算；8KiB 前缀检查超时。限额未合入默认源码。当前源码包含协商式上行重试及旧 RX 单次上传的防重定向、TLS 超时释放修复，尚无这些改动解除该入口冻结的证据。版本、原始指标和历史英国对照见 [现场报告](xray-rx-field-20261029.md)。

## 启用范围

`rx` 默认关闭；客户端与服务端都需要显式启用。只支持原生 H1/H2 的 `packet-up`。不支持 H3、Browser Dialer、downloadSettings、stream-up/stream-one 组合；配置不支持时明确报错。GET Header 上行复用上游既有功能，不另定义上行编码。

在现有 VLESS + XHTTP 的两端 `streamSettings.xhttpSettings` 配置中加入：

```json
{
  "path": "/your-existing-path",
  "mode": "packet-up",
  "extra": {
    "uplinkHTTPMethod": "GET",
    "uplinkDataPlacement": "header",
    "uplinkDataKey": "data",
    "scMaxEachPostBytes": 4096,
    "rx": {
      "enabled": true,
      "stallDetect": true,
      "maxReplayBytes": 262144,
      "maxSessions": 128,
      "probeIntervalMs": 1000,
      "stallTimeoutMs": 3000,
      "requestTimeoutMs": 5000,
      "sessionTtlSecs": 30,
      "maxReconnects": 64,
      "retryDelayMs": 100
    }
  }
}
```

以上只是 XHTTP 配置片段，保留已有 VLESS 身份、TLS、监听和路由配置。`rx` 位于 `extra` 时，与其他 extra 参数一起使用；不要再在外层重复设置 extra 内的字段。`stallDetect` 独立默认关闭；启用恢复但关闭检测时仍可恢复已观察到的读取异常，无法主动解除静默冻结。

会话使用随机 bearer capability 认证恢复/ACK/上行关联。真实网络必须使用经过验证的 TLS（CDN 到源站也需保护）；明文仅用于环回测试。不要把 capability 头写入访问日志，也不要允许中间件缓存或改写 `X-RX-*`。响应使用 no-store，自定义偏移头需完整往返。VLESS 身份验证与恢复 capability 不是同一个概念。

## 行为与限制

- 缓存满时反压，不丢弃未 ACK 数据；ACK 只累计客户端已交付字节。每会话默认最多 256KiB，128 个会话的回放容量上界为 32MiB，另外仍有 HTTP、Go、上传队列、内核等内存。
- 每次下行恢复与 ACK 探测都新建独立底层 TCP；双方协商上行重试后，每个上行包的各次尝试也使用独立 TCP。H2 重开 Stream 不足以解除冻结，控制与握手成本必须计入。
- 正常空闲不依据“没有收到数据”直接判坏；探测显示服务端有未交付数据、且客户端正在阻塞读取时，才确认停滞。控制路径无响应只标为未知故障。
- 生命周期内重连次数有上限；连续无进展指数退避至最多 2 秒。默认 64 次预算无法完成严格每连接 16KiB 的 50MiB 下载。理论至少 3200 条有效数据连接，加上控制请求、HTTP/TLS 和截断损失。
- phase1b 将显式 `maxReconnects` 的允许上限提高至 131072，默认仍为 64。实验大文件应同时设置外层截止时间；HTTP/TLS 损耗可能使所需连接更多，提高预算不保证特定受限线路可以完成 1GiB。
- 首次协商完全没有 RX 响应头时可退回普通 XHTTP；只收到部分头、偏移/nonce 错误，或已协商成功后恢复丢失确认时，终止连接，不能从零重放。
- phase1b 仅恢复下行；当前源码通过可选 `X-RX-Upload: 1` 确认后，增加串行上行确认与有限重试。服务端保存最近成功入队 seq 的摘要，ACK 丢失后的同包重发只确认、不重复入队；每次上传使用独立底层 TCP，沿用现有 GET Header/Body 编码。旧端未确认能力时保留单次上传和原确认语义，已确认后缺失或错误的 seq/nonce 确认直接失败。未协商的旧 RX 上传仍只尝试一次，但使用专属 transport、禁止重定向并在超时后关闭连接，避免令牌泄露及握手连接残留。
- 上下行额外尝试共用 `maxReconnects` 的会话终身预算，正常首次上传不扣；每个上行包的全部尝试最多持续 `sessionTtlSecs` 秒，单次尝试最多 `requestTimeoutMs` 毫秒，取消会关闭连接。串行确认降低高 RTT 上传吞吐，是当前单包去重状态的边界。单包总截止是额外限制，即使控制探测持续刷新服务端空闲 TTL，长期上行反压达到该时长也会结束会话。
- 首次握手失败、源站进程重启、回放过期、认证失败、上行重试总截止或预算耗尽仍结束代理会话。没有跨进程会话持久化，也没有 PacketDown、多路径重排或自适应入口选择。
- TCP 字节阈值可能落在 H2 DATA 帧或 TLS 记录中途，导致同一 offset 一直无法交付；有界失败是正确行为。不能把只测 H1 明文的成功推广到 H2/TLS 或 H3。

## 主动分段与连接预热（实验）

默认关闭。在两端现有 `rx` 对象内显式设置：

```json
{
  "enabled": true,
  "maxResponseBytes": 1024,
  "preconnects": 2,
  "maxSegments": 2097152
}
```

- `maxResponseBytes`：0 关闭；启用范围 256 至 65536，不能超过本端 `maxReplayBytes`。双方确认后采用较小值；旧端不确认时保留原 RX 行为，不预热。分段头缺失、改变或边界错误会终止会话。
- `preconnects`：0 关闭，最多 4。只预建下行 TCP/TLS，不发送 GET 或 H2 SETTINGS。K 只计排队连接与后台握手；当前串行 GET 的连接或空池冷拨另计，总下行连接最多 K+1；控制与上传也另计。取用时丢弃过期连接，关闭会话会取消后台握手并关闭队列。后台失败累计达到 `maxReconnects` 后停止预热，不扣前台故障预算。
- `maxSegments`：分段启用时，0 取 2097152，上限 16777216。客户端所有分段数据 GET 尝试均计数；服务端通过身份、能力和 generation 校验后的数据 GET 尝试独立计数，包含 slots/offset 拒绝。预算耗尽不再发数据 GET，允许一次有界 HEAD 核对最终完成。服务端到限返回不可重试错误，不释放未确认字节或替换旧 generation。
- 响应头立即 Flush，不等待首个上传。客户端交付满本段额度时立即关闭底层 TCP，下一次读取再串行换段，不等 HTTP EOF。正常换段不扣 `maxReconnects`，不计故障恢复；提前 EOF 先 HEAD 核对逻辑完成，否则按故障恢复。
- 每段日志记录起止偏移、首字节耗时与结束原因；TCP 日志以编号关联请求，计量 TLS 下的 socket 字节。使用 FinalMask 时计量位于其 `DialTCP` 返回连接处。该值不含 IP 头及重传，不等于 CDN 账单；SYN 数另以抓包核对。故障耗时最多保留前 1024 个样本，总故障计数仍累计。
- 预热只减少握手等待，串行每段仍需约一个请求 RTT。1KiB、90ms RTT 的理论吞吐约 11KiB/s，1GiB 约需 26 小时。上行每包仍独立握手并串行确认。当前本地 H1/H2+TLS 的 16KiB/TCP 冻结代理检查已完成。2026-10-30 本版俄罗斯受限入口64KiB验收在27.018秒收到0字节，首次RX确认未完成，未进入上传或段间轮换，120秒窗口跳过；原实验服务已恢复。该样本尚未验证分段效果，不宣称正常速度或完整1GiB验收通过，证据见现场报告。

## 有界多段并发（实验，默认关闭）

在两端已开启分段的 `rx` 内设置 `maxConcurrentSegments`，0保持原串行；显式范围2至256，要求 `maxConcurrentSegments * maxResponseBytes <= maxReplayBytes`。双方经 `X-RX-Parallel` 确认后取较小并发上限，首次无该扩展确认则继续串行RX；重复、非法或协商后能力改变均失败。旧RX和普通XHTTP默认行为不变。

- 客户端活动GET最多N个；包含已完成未消费作业的队列最多 `window / 段长` 个，固定段缓存总分配不超过回放窗口。乱序字节按offset保存，只按上层连续交付offset累计ACK。分段GET携带显式 `X-RX-Ack`，段起点不再隐含ACK。服务端继续使用有界回放缓存和反压。
- 新模式段GET使用固定会话代际、显式作业终点和专属新TCP。部分段补发从未交付处续读且不扩展原作业终点；只取消重叠的旧尝试，其他在途段保持。故障补段和上传共用终身 `maxReconnects`，所有数据GET计 `maxSegments`。并发恢复耗时从作业发现失败到实际连续交付，包含队头和消费者等待，日志明确标记，不当作纯网络恢复时延。
- 请求的拨号和TLS受 `requestTimeoutMs` 限制；并发segment在取得连接后使用固定1秒响应头截止，已知完整区间的body同样使用固定1秒截止。等待尚未生产数据的尾段保留正常空闲语义，启用stallDetect时才根据已生产待交付数据判断停滞。固定1秒是实验参数，不能视作适合所有高RTT线路的通用值。
- GET活动数及预热排队数分别有界；RX握手竞速在首路500ms未完成时每隔500ms追加一路，单次拨号最多4路，胜者以外全部关闭。TCP尝试数可能高于GET数与N+K，控制和上传也独立另计。不会把H2新stream当成新TCP。统计包含作业/活动段/预取字节峰值和预热命中/冷拨数，真实SYN及线上字节仍需抓包。
- `spreadAddresses` 可选，最多64个IP字面量，要求启用分段；每次新RX连接轮换目的IP，端口、SNI、Host和证书验证保持。必须由操作者确认所有地址都能为相同服务提供正确响应，不能填无关目标或把多地址当成持续吞吐保证。
- 并发N4与N16已有64KiB完整前缀和hash核对，但包含启动的整请求仍分别耗时41.286秒及48.395秒。完整1GiB与正常速度目标保持未完成，后续更大样本报告必须区分对应源码/产物。现场窗口、请求数和档位需单独授权。

## 本地端到端验收

`scripts/xray-rx-smoke.py` 只使用 Python 标准库及指定二进制。它启动自己的环回 HTTP 源站、VLESS 服务端、SOCKS 客户端和 TCP 故障代理；每条代理连接的下行（包含 HTTP 响应头）达到 16384 字节后 FIN。通过一次 SOCKS HTTP 请求检查完整下载 SHA-256，同时记录实际 GET `data-0` 请求及无 Body Header 形态。测试退出仅终止自己创建的子进程，不按进程名称清理系统服务。

```powershell
python scripts/xray-rx-smoke.py --binary (Resolve-Path ../artifacts/raybox-rx-xray.exe).Path --size-mib 1 --artifacts ../artifacts/smoke-1m
```

增加为 `--size-mib 50` 可运行 50MiB；此脚本为验证设置了 16000 次预算及毫秒级间隔，**不能作为生产推荐参数**。输出 `result.json`、`server.log`、`client.log`，包括 hash、字节数、断开次数、TCP 数、吞吐时间及 GET Header 观察。脚本测得的字节是 TCP socket 有效字节，未包含 IP/TCP 头、重传或 TLS 握手（此测试是明文环回）。

源码验证：

```powershell
go test ./transport/internet/splithttp ./transport/internet/tls ./infra/conf
go test -race ./transport/internet/splithttp ./transport/internet/tls ./infra/conf
go build -trimpath -buildvcs=false -ldflags '-X github.com/xtls/xray-core/core.build=RayBox-rx-experimental' -o ../artifacts/raybox-rx-xray.exe ./main
```

具体执行结果与阶段 2 待验证项见交付验证报告；这些命令本身不代表全部已执行。严格 GET Header 无 Body 的线上抓包、俄运营商、大规模并发、长会话仍应分别记录环境和证据。

## 回滚

1. 先在独立实验进程和端口运行，保留当前官方 Xray 二进制及原配置副本。实验产物使用 `xray-rx-phase1-*` 名称，不覆盖生产文件。
2. 停止自己启动的实验进程，恢复原二进制和配置；或将两端 `rx.enabled` 设为 `false` / 删除 `rx`，重启实验实例走原上游路径。旧会话不会跨进程重启迁移。
3. 不依赖普通客户端的自动降级来验证回滚；使用原配置复测已有 VLESS + REALITY / 普通 XHTTP / WebSocket 实际业务。
4. 源码补丁、上游基线和许可证随发行保留。没有自动 Git 提交、远程推送或生产部署。

## 构建包

本仓库发布完整对应源码，不附带旧工作区的phase1/phase1b二进制包、工具链、私有证据或回滚产物。使用系统PATH中的Go1.27兼容工具链，构建入口见根README；产物放入根目录被忽略的 `artifacts/`。

Linux交叉编译在独立终端设置 `$env:GOOS='linux'`、`$env:GOARCH='amd64'`、`$env:CGO_ENABLED='0'`，并使用独立输出名，避免影响随后Windows本机检查。历史实验文件名保留旧名称，不代表新仓库附带这些文件。

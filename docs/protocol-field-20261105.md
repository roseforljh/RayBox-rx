# 协议联网验收（2026-11-05）

## 方法

使用获授权的独立 Linux 实验服务、Windows RayBox API 探针和临时 Cloudflare 入口。客户端由标准 outbound 注册表创建，不启动外置协议进程，不配置 TUN 或系统代理；Windows 已有用户 TUN 保持运行。两种服务端入口分别承载原生协议和扩展协议，不代表客户端回退为两个内核实例。

每个正向案例创建、启动、关闭实例两次，检查：

- TCP 单次写入 90,112 字节，逐字节校验回显。
- UDP 1、1200、4096 字节，两个目标的包边界、内容和来源地址。
- DNS 查询事务 ID、响应标志及来源地址。
- 关闭实例后既有 TCP 连接不可继续读取。

这两轮是实例重建，不是同一实例的热重载。SS2022 的 Windows 公网验收限为 1/1200 字节；4096 字节另作 Linux 回环对照，不能合并称为公网大 UDP 包通过。Naiveproxy 在需求截图中禁用，本轮没有启用。

## 通过结果

以下 18 个配置各有两轮完整通过，共 36 条正向成功记录：

| 配置 | 实际测试传输 | UDP 上限（本轮） |
| --- | --- | --- |
| VLESS Reality Vision | TCP / REALITY / Vision | 4096 |
| VLESS XHTTP Reality Encryption | H2 / REALITY / VLESS Encryption | 4096 |
| VLESS XHTTP Encryption | H1 / 无外层 TLS / VLESS Encryption | 4096 |
| VLESS WS Encryption | WS / TLS / VLESS Encryption | 4096 |
| VLESS XHTTP TLS | H2 / TLS / XUDP | 4096 |
| TUIC native | QUIC，native UDP relay | 4096 |
| TUIC quic | QUIC，quic UDP relay | 4096 |
| AnyTLS | TLS | 4096 |
| AnyTLS Reality | REALITY | 4096 |
| SS2022 AES-128-GCM | 原生 TCP/UDP | 1200 |
| SS2022 AES-256-GCM | 原生 TCP/UDP | 1200 |
| SS2022 ChaCha20-Poly1305 | 原生 TCP/UDP | 1200 |
| CF VLESS XHTTP | H2，边缘 TLS，严格验证源站证书 | 4096 |
| CF VLESS WS Encryption | WS，边缘 TLS，严格验证源站证书 | 4096 |
| VLESS XHTTP TLS over UDP | H3 / QUIC，独立 UDP 监听 | 4096 |
| VLESS XHTTP TLS H1 | H1 / TLS | 4096 |
| VLESS XHTTP fixed UDP | H2，显式空 packet_encoding，每个目的单独建立连接 | 4096 |
| VLESS WS Encryption early data | Sec-WebSocket-Protocol，2048 字节 early-data 配置 | 4096 |

H3 案例真实使用 UDP 承载，与 H2/TCP 案例不同；不再用两份重复配置表示截图中的 UDP/TCP 两种入口。实际配置、探针、原始结果及认证材料仅存于被忽略的私有 artifacts，不加入公开源码。

错误 hostname 的 AnyTLS 和 XHTTP TLS 负向检查均观察到具体 x509 域名不匹配错误，未关闭证书验证。Cloudflare 的 strict 规则仅匹配临时实验 hostname，没有修改整个 zone 的 SSL 模式。

## 找到并修复的问题

1. **REALITY 客户端 JSON 往返被识别成服务端。** `json.RawMessage` 的空 Target/Dest 序列化为 `null`，原 Build 只检查是否 nil。公共配置构造现排除 JSON null，客户端 SNI、公钥、short ID 校验保持。新增直接及双轮 JSON 往返回归。
2. **AnyTLS 大写入帧长截断。** 当前依赖把数据长度转换为 uint16，90,112 字节写入的帧头变成 24,576，流被破坏。RayBox 的入站/出站流在统一包装处将应用写入拆成最多 65,535 字节的数据帧，同一流的整次写入持锁，防止并发写入在分片间交错；不同流仍可按协议复用。保留原 stream 的握手、关闭和 deadline 方法。不更改依赖缓存或协议格式；这是 RayBox 接入层修复，未修补外部依赖库本身。新增真实 session 帧解析回归，检查两个并发 90,112 字节写入各拆为 65,535 + 24,577，且应用数据不交错。
3. **session 组合 deadline 绕过读超时。** 外层只实现 SetReadDeadline，继承的 SetDeadline 调到底层空实现。TCP 返回连接现将组合 deadline 转发给读超时包装；设置非零写截止（含组合截止）明确返回 errors.ErrUnsupported，不再把空实现报告成设置成功。组合截止返回不支持时，读截止仍已生效，可单独使用 SetReadDeadline；零值清除成功。新增无数据读取超时、更新/清除截止和不支持写截止的回归。
4. **实验服务端默认私网阻断。** 扩展服务端 freedom 默认阻断代理请求访问私网，回显目标是 loopback，因此已认证连接被 blackhole。仅在两个隔离实验配置中允许 loopback 的四个 fixture 端口；没有放宽产品默认保护。修正实验配置后 XHTTP/WS 正向检查通过。

## 失败记录及边界

- 修复前的 XHTTP/WS 零字节结果和 AnyTLS 超时均保留，不以编译或配置检查替代业务验收。
- SS2022 在 Windows 默认禁止 IP 分片时，1/1200 字节双目标成功，4096 字节写入明确报 WSAEMSGSIZE。显式允许分片后仍读取超时，绑定既有物理网卡的独立探针也未解决；没有修改系统 TUN、路由或协议默认值。三种 SS2022 在 Linux 本机均完成两轮 TCP、1/1200/4096 字节 UDP、DNS 和关闭检查，共六条成功记录。
- AnyTLS 修复后首次公网四条中，TLS 两条和 Reality 一条完整成功，Reality 另一条在 1 字节 UDP 读取时超时。随后针对 Reality 的独立双轮全部成功；保留该次超时，不把后测成功描述为无间歇失败。
- 这是功能收发验收，不是吞吐、长时间稳定性、Android/Windows UI 或三客户端接入验收。

## 本地检查与清理

Go 1.27.3、GOTOOLCHAIN=local、D 盘构建缓存；五个包的本次定向回归通过：anytls、session、vless、streamconn、infra/conf。未跑全仓测试。两个 Linux CLI 的实际 version、配置校验和独立运行已执行，实验版本显示为 1.0.0-dev，不冒称正式发布工件。

四个临时服务已停止，fixture 和协议实验端口已释放，既有生产 sing-box/Caddy PID 与初始记录一致。两个本轮临时 DNS、临时 strict ruleset 和专用 Origin 证书已清理；未改动业务记录或收费资源。最初 baseline 文件在实验刷新时被覆盖，最终核对依据初始进程记录和独立清理审计，不将覆盖后的文件当作原始生产基线。

两项只读诊断子任务已结束。主会话收尾后收到延迟送达的完整报告，复核其服务端私网阻断、AnyTLS uint16 帧长溢出及 deadline 透传结论与直接验证一致。补充并发拆帧和写截止错误契约后，anytls/session/vless 三包定向回归通过；两项新增回归各重复 20 轮通过，AnyTLS 并发拆帧定向 race 检查通过（1.037 秒）。未重新启动已清理的公网实验。

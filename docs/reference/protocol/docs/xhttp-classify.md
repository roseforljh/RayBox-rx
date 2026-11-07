# 普通 XHTTP 固定入口分类取证

工具：`scripts/xhttp-classify.py`，Python 3.10+ 标准库。默认只生成离线计划，不执行 Xray、curl、SSH 或任何远端连接。只使用现有明确白名单，不发现地址、不扫描网段、不修改服务、不提交代码。

## 输入

准备独立私有实验模板，只包含一个 VLESS outbound、一个 vnext 和一个 UUID。模板允许现有 `log`、`inbounds`，生成时将它们替换成禁用日志和环回 SOCKS。拒绝 routing、DNS、备用出站、mux/xmux、sockopt、ECH、FinalMask、downloadSettings 和自定义证书验证，避免绕行及混 IP。

模板必须明确包含以下字段，省略的认证、路径、上行及填充参数取自操作者自己的配置，不能填公开示例值：

```json
{
  "network": "xhttp",
  "security": "tls",
  "tlsSettings": {
    "serverName": "entry.example.com",
    "fingerprint": "unsafe",
    "alpn": ["h2"]
  },
  "xhttpSettings": {
    "host": "entry.example.com",
    "path": "/your-existing-private-path",
    "mode": "packet-up"
  }
}
```

这里 `fingerprint="unsafe"` 是 Xray 的原生 Go TLS 选项，证书仍使用系统 CA 并核对 serverName；不接受 `allowInsecure=true` 或证书验证名称覆盖。H1 使用 `alpn=["http/1.1"]`。省略 fingerprint 会使用 Chrome 指纹，因此工具要求显式填写。工具按 Xray 的 extra 替代规则计算有效字段，删除外层/extra 的 RX，再扁平化；Host、path、mode 从外层取。原生证书验证不会关闭。

规格文件示例，保存为被忽略目录中的私有 JSON：

```json
{
  "host": "entry.example.com",
  "ips": ["104.21.44.55"],
  "url": "http://127.0.0.1:18090/check.bin",
  "sha256": "填写预先从自有源站核实的64位小写SHA256",
  "request_timeout_s": 8,
  "total_timeout_s": 30,
  "max_requests": 1,
  "max_connections": 8,
  "per_ip_connections": 8,
  "fast_MB_s": 1
}
```

示例 IP 和端口均不代表授权或当前服务状态。入口 port 来自私有模板；Host 与 SNI 必须和 `host` 完全一致。URL 是经 VLESS 到达的自有资源，可以与入口 Host 不同。示例环回 URL 指远端 VLESS 出站能访问的源站。必须先已有**正好 1,048,576 字节**的稳定资源及独立核实的 hash；工具不会部署源站或下载参考文件。不得使用可变内容、压缩后的 hash 或完整 1GiB 的 hash 代替。

IP 列表只接受 1 至 64 个不重复的公网字面量，不接受域名、CIDR、范围或 IPv6 zone。每 IP 一次 payload GET，串行执行，不补测。全局最多 64 个应用请求、512 次出口 connect、900 秒测量窗口，每轮单 IP 最多 120 秒。这些是工具上限，实际授权预算通常更小，必须明确填写。失败 connect 也扣预算。

## 离线准备

从 `Xray-core/` 运行，先已有根 `artifacts/`，每轮用一个全新目录：

```powershell
python -I scripts/xhttp-classify.py --spec ../artifacts/classify-spec.json --template ../artifacts/classify-template.json --output ../artifacts/classify-plan-01
```

输出 `plan.json` 和 `client-preview.json`。预览中的 1080/1081 是环回占位端口，**不要直接启动预览**；执行模式会重新校验输入并分配实际端口。离线检查包括 JSON 重复键、白名单、预算、TLS、extra 和可支持配置形态，不冒充 Xray 二进制 `run -test` 的完整语义校验。

## 明确授权后运行

现场运行前，用户必须确认测点、自有 Host、完整 IP 白名单、请求数、总时长和连接预算。迁移或本地实现授权不包含这个现场窗口。执行命令仅是使用说明，本次没有运行：

```powershell
python -I scripts/xhttp-classify.py --spec ../artifacts/classify-spec.json --template ../artifacts/classify-template.json --output ../artifacts/classify-run-01 --execute --binary ../artifacts/raybox-rx-experimental.exe
```

在实际测点本机使用相应 Linux 二进制，脚本不会 SSH 或部署二进制。需要系统 PATH 中现有支持 `%{json}` 和 `--max-filesize` 的 curl；不自动安装。显式 `--curl` 可指定工具路径。

每个 IP 都启动独立 Xray 进程和 TCP 门控，仅把门控出口连接到该 IP 的模板端口；SNI、Host 和端到端 TLS 字节流保持。门控一次调用 socket.connect，无 DNS、地址回退或自建重试；总/单 IP 次数上限在 connect 前扣除。Xray 的环回端口动态分配，每轮生成随机 SOCKS 密码；启动检查只进行方法协商及密码认证，不发 CONNECT，curl 使用同一凭据。普通其他 SOCKS 进程抢占端口会被拒绝，完整样本还要求本轮门控确实建立过连接；不声称对抗恶意本地进程。不能复用生产端口。

curl 禁用 curlrc、代理环境变量、应用重试、重定向、URL 展开及 Range；上下两层请求 identity 编码。每轮从启动算绝对截止，不扣掉冷启动来提高吞吐。门控持有单 IP 与全局截止的较小值，到期独立封闭 socket；清理先封门控，再等待子进程退出，避免清理宽限内继续远端传输。完成后再次检查截止，迟到完整数据也记 inconclusive。预算/截止/子进程异常会停止本轮；全局预算耗尽不启动下一 IP。退出只终止本工具创建的进程及 socket，写 `cleanup.json`，不按名称停服务。清理另有短宽限，不把它算进下载速度；总报告另记包含清理的真实耗时。Ctrl+C 也执行清理。

## 结果与边界

- 每 IP `result.json` 记录实际字节、完整 SHA-256、状态、curl 退出码、编码核验、包含启动的耗时、十进制 MB/s 与 MiB/s。必须 HTTP 200、无重定向、长度及 hash 完整一致且 identity 才能记完整样本。
- `verified_1mib_fast`：本轮完整且达到指定 `fast_MB_s`；`verified_1mib_slow`：完整但慢；`inconclusive`：失败、截断、错误 hash、压缩、预算耗尽或清理不完整。**三者都不能直接认定永久受限/永久正常**，更不能证明完整 1GiB 或“所有受限入口达到目标”。
- `application_requests` 是 payload GET 数。curl `num_connects` 只反映 SOCKS 应用连接。普通 XHTTP 可能有内部上传重试、H2 stream 或 HTTP 重定向，工具不改 Go，不能阻止或解密计数，相关字段为 null；出口 connect 仍受预算约束。完整样本也不能写成“单 TCP 通过”。
- `transport` 是门控的 connect 尝试/建立/失败/拒绝数及出口 socket 双向 TLS 字节，含握手数据，不含 IP/TCP 头和内核重传。SYN 数、丢失率、TLS 握手数、实际线上字节及额外线上流量均为 null，需独立抓包才能补齐；不能用 connect 次数冒充 SYN。
- **环回门控会改变缓冲、反压、TCP 参数和分包时机**。结果明确标记 `measurement_path=loopback_tcp_gate`，不能外推 Xray 直拨下的固定包数/限流机制。达到目标速度仅可作为候选样本；正式直拨验收仍须单独授权与证据。
- Linux 记录 Xray 的抽样 RSS 峰值、观察到的进程 CPU 秒，短峰值可能漏掉；其他系统为 null。未覆盖内核 CPU、curl 和所有线程的成本，不能当完整容量报告。
- `plan.json`、配置、URL、响应头、curl stderr 和原始指标均属于私有现场证据，只写新建 `artifacts/` 轮次目录。脚本不在终端打印认证配置或完整异常。POSIX 使用 0700/0600，Windows 私密性仍依赖目录 ACL。不要上传这些文件。

## 唯一最小自检

```powershell
python -I scripts/xhttp-classify.py --self-test
```

只做离线输入/extra/RX/TLS/编码/hash 断言和环回字节透传、硬拨号预算、退出清理，不启动 Xray、不访问公网。本次自检通过。另经用户单独授权完成一次34IP现场分类，结果和边界见 [首轮报告](xhttp-classify-field-20261030.md)，不将自检当现场证据。

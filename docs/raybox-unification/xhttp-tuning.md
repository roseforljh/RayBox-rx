# Chrome 155 与 XHTTP 可选调优

## Chrome 指纹

`chrome` 默认使用 Chrome 155，原生 TLS/REALITY 和内置协议 TLS/REALITY 均更新实际 ClientHello。两条路径均接受 `chrome_155` / `hellochrome_155`。协议层保留 `hellochrome_133` 等显式旧模板；不会修改用户显式选择的指纹。

Chrome 155 模板包含 ML-DSA 签名能力、28 个 trust-anchor IDs 和 X25519MLKEM768。trust-anchor IDs 是浏览器公开快照，不替换验证服务器证书所用的根证书库。QUIC Chrome parrot 使用独立实现，不能将 TCP 指纹升级当作 QUIC 同步完成。

## Packet-Down

默认关闭。客户端和服务端均配置，且 `X-Packet-Down: 1` 请求/响应确认成功后，才使用下行分帧与空闲 padding：

```json
{
  "network": "xhttp",
  "xhttpSettings": {
    "path": "/your-path",
    "mode": "packet-up",
    "extra": {
      "packetDown": {
        "keepAlive": true,
        "keepAlivePeriod": "10-30",
        "keepAliveLength": "24-64"
      }
    }
  }
}
```

KunBox 的标准节点对应 `transport.extra.packetDown`，链接导出回导保留该对象。服务端使用支持相同帧格式的版本；旧端不协商时继续原始字节流。

- 中间代理剥离确认响应头会造成帧格式不一致；这种路径不要开启。
- 不与 RX/OOB 特殊路径自动组合。
- 这是防止下行空闲超时，不是恢复按字节截断的响应，不能解决俄罗斯 16KB 截断。
- 保活会增加空闲流量。以上默认区间每次空闲发送 24–64 字节 padding，另有帧头和 HTTP/TLS 开销。

## XMUX 可选配置

保留现有默认值，不因单个网络体验改变全局配置。下面是用于现场对比的可选组合：

```json
{
  "xmux": {
    "maxConcurrency": 5,
    "maxConnections": 0,
    "hMaxRequestTimes": "300-600",
    "hMaxReusableSecs": "900-1800"
  }
}
```

放在 `xhttpSettings.extra`（KunBox 为 `transport.extra`）。`maxConcurrency` 和 `maxConnections` 不能同时为正数。比较并发 1 与 5 时先固定其他参数，再比较轮换参数，避免把不同因素混在一起。

本地 TLS 回显证明数据完整性、连接复用数量与轮换退出；不代表俄罗斯 LTE/Wi-Fi 现场性能。提高并发会让更多逻辑流共享同一连接，连接故障时同时受影响的流也会增加。

2026-11-06 定向 `-race` 实测：同一 TLS/H2 服务、20 个同时存活的会话，每个回显 16 KiB。并发 1 与 5 均为 20/20 成功、327680 字节完整；底层连接分别为 20 条与 4 条。两档请求数轮换、时间轮换及最后会话关闭后退出均通过。当前 JSON 默认仍为 3 条连接、每连接 600–900 次请求和 1800–3000 秒复用期，不将讨论里的并发 1 当成本项目默认值。

## 来源

- [Chrome 155 / uTLS](https://github.com/XTLS/Xray-core/pull/7135)
- [Vision/XUDP 并发写入修复](https://github.com/XTLS/Xray-core/pull/7337)
- [Packet-Down 保活](https://github.com/XTLS/Xray-core/pull/7159)
- [XMUX 现场讨论](https://github.com/XTLS/Xray-core/discussions/6527)

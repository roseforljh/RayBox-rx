# 源码来源

RayBox-rx 是单一 Go 模块 `github.com/roseforljh/RayBox-rx`。现有代码由两套上游源码导入后整合而来，未伪造或重写上游历史；所有修改随本仓库源码提供。

| 上游 | 基线 | 现位置 | 许可证 |
|---|---|---|---|
| sing-box | 官方 v1.14.3 源码包，SHA-256 `9a8e3712a5f611ad95f1d16874bbdcdffbd3b8eed16dae2d71ce7042a33c3386` | 仓库根运行层（`adapter/`、`route/`、`dns/`、`option/`、`protocol/`、`transport/`、`service/`、`cmd/`、`sdk/android/` 等） | GPL-3.0-or-later 及附加声明 |
| Xray-core | 官方 v26.10.31，`a4a3dff5b20c2faae567b742c172c7b95e75c083` | `internal/protocol/`；protobuf 生成器迁至 `tools/vprotogen/` | MPL-2.0 |
| sing-tun | 受控模块版本 | `third_party/sing-tun/` | GPL，见 [`SING_TUN.md`](docs/raybox-unification/SING_TUN.md) |
| metacubex uTLS | v1.8.7，本地移植 Chrome 155；保留 Meta 扩展接口 | `third_party/utls/` | BSD-3-Clause，见 [`SOURCE.md`](third_party/utls/SOURCE.md) |
| XTLS uTLS | `v0.0.0-20261106062530-666e20f266ac`，协议层 TCP TLS/REALITY 使用 | 固定 Go 模块依赖 | BSD-3-Clause，保留模块许可证 |

## QUIC/Hysteria 依赖锁定

本次 Hysteria 2 适配只升级以下三个直接依赖，未执行全仓 `go get -u`：

| 模块 | 精确版本 | 上游提交/标签 |
|---|---|---|
| `github.com/sagernet/quic-go` | `v0.61.0-sing-box-mod.12` | `80c132feb2112b5d0d88d02f941b1e68e56a2722`（标签同名） |
| `github.com/sagernet/sing-quic` | `v0.7.2-0.20261020082049-7e0a5869a5f4` | `7e0a5869a5f49ed0a685143345dfda0448bbc708` |
| `github.com/sagernet/sing` | `v0.9.7-0.20261023115459-5dc452e537bf` | `5dc452e537bf75c5c51c54c8c4a4be1d19d85c4f` |

这些版本保留现有受控 `sing-tun` 与 `utls` 本地 `replace`，未替换或删除其他本地改动。

上游地址：

- https://github.com/SagerNet/sing-box
- https://github.com/XTLS/Xray-core

## 导入与整合

1. 首次导入为组合快照（原 `sing-box/`、`Xray-core/` 两个模块），不复制原仓库 `.git`、不引入子模块。sing-box 本地来源顺序：`92d5d012` 上游源码包 → `4e30f6b0` Xray 出站集成 → `748ab305` 接入相邻 Xray-RX → `f6bd1de2` AAR 构建脚本；这些哈希仅为来源记录，不在本仓库历史中。
2. 2026-11 合并为单一根模块，产品名称、版本（`VERSION`）、CLI、SDK 包名和构建产物改为 RayBox 自有身份。上游 Go import 路径改为本模块路径；protobuf FullName、字段号、gRPC 方法名及线协议标识保持不变以兼容现有配置和对端。
3. Xray-core 本地改动：RX、OOB、TLS 证书重载、WebSocket 及相关测试；XHTTP 新增 `h2Params` 使用字段 32，`packetDown` 使用字段 33，RX/OOB 字段 30/31 保持兼容。

上游的文档、许可证和社区文件保留在 `docs/reference/runtime/`（sing-box）与 `docs/reference/protocol/`（Xray-core）。

## 不发布的内容

私有现场报告以脱敏版本替代。原始凭据、认证配置、私钥、现场抓包/日志、便携工具链、缓存、编译二进制和 AAR 不纳入仓库。上游公开测试资产保留。

## 许可证

- 运行层：[`docs/reference/runtime/LICENSE`](docs/reference/runtime/LICENSE)，GPL-3.0-or-later，保留原附加声明。
- `internal/protocol/`：[`docs/reference/protocol/LICENSE`](docs/reference/protocol/LICENSE)，MPL-2.0。
- 其他文件内适用的版权及许可声明继续有效。

本文件不重新许可上游文件。分发二进制须遵守所有适用条款并提供对应源码。

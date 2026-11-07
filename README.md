# RayBox-rx

RayBox 内核：单一 Go 模块 `github.com/roseforljh/RayBox-rx`，产品版本见 [`VERSION`](VERSION)。负责 TUN、DNS、路由和受保护拨号，并内置 VLESS / VMess / Trojan 的 XHTTP、REALITY、Encryption 及实验性 XHTTP RX/OOB 增强。RX/OOB 默认关闭。

## 目录

- `cmd/raybox/`：客户端 CLI（Windows 原生核心同一入口）。
- `cmd/raybox-server/`：Linux 服务端核心。
- `sdk/android/`：Android 绑定，Java 包 `io.raybox.raybox`，产物 `raybox.aar` / `raybox-legacy.aar`。
- `adapter/`、`route/`、`dns/`、`option/`、`protocol/`、`transport/`、`service/`：运行层。
- `internal/protocol/`：扩展协议、传输与配置实现。
- `tools/`：构建脚本、产物检查器、`vprotogen` protobuf 生成器。
- `third_party/sing-tun/`：受控 sing-tun 副本，见 [`SING_TUN.md`](docs/raybox-unification/SING_TUN.md)。
- [`SOURCE.md`](SOURCE.md)：来源、许可证；[`VALIDATION.md`](VALIDATION.md)：历史检查记录。
- RX 使用说明：[`xray-rx-usage.md`](docs/reference/protocol/docs/xray-rx-usage.md)。

## 版本

`go run ./cmd/internal/read_tag` 输出 `<VERSION>-<12位提交号>`，构建通过 `-X github.com/roseforljh/RayBox-rx/constant.Version=...` 注入。CLI `version`、SDK `Raybox.version()`、server `version` 和各 manifest 只报告这一个 RayBox 版本。改版本只改 `VERSION`。

## 构建

要求 Go 1.27.3（`GOTOOLCHAIN=local`）。

```sh
go build -trimpath -o raybox ./cmd/raybox
go build -trimpath -o raybox-server ./cmd/raybox-server
```

正式脚本（PowerShell 7，干净源码；本地候选加 `-AllowDirty`，输出到 `*-candidate/`）：

| 平台 | 命令 | 输出 |
|---|---|---|
| Android | `./tools/build-libbox.ps1` | `artifacts/libbox/`：`raybox.aar`、`raybox-legacy.aar`、`build.json` |
| Windows | `./tools/build-windows.ps1` | `artifacts/windows/`：`raybox-windows-amd64.exe`、`manifest.json` |
| Linux server | `python -I tools/build_server_candidate.py --allow-dirty` | `artifacts/server-candidate/` |
| Apple | `go run ./cmd/internal/build_libbox -target apple` | `Raybox.xcframework` |

Android 需 JDK 17、Android SDK、NDK 29.0.14206865、gomobile v0.1.13；标准（API 24）和 legacy（API 21，无 Naive）均含 `arm64-v8a`、`armeabi-v7a`、`x86`、`x86_64`。检查器实际校验 AAR 中 JNI、ELF 对齐（64 位 16 KiB / 32 位 4 KiB）、公开 API 和安全标记。

protobuf：`make proto_check`（对 `internal/protocol`、`daemon`、`experimental/boxdd`、`experimental/v2rayapi`、`transport/v2raygrpc` 逐个执行 `go run ./tools/vprotogen -pwd <目录> -mode check`）。

## 发布工作流

`.github/workflows/{android,windows,server}-core.yml` 在 push `main` 时构建并发布 `android-<SHA>`、`windows-<SHA>`、`server-<SHA>` 不可变 Release；手动运行默认只构建，`publish=true` 才发布。规则见 [`WORKFLOWS.md`](docs/raybox-unification/WORKFLOWS.md)，本地回归：`python -I tools/test_release_workflows.py`（需 PyYAML、pwsh、bash）。

消费端须锁定具体 tag，校验 manifest、size、SHA-256 和架构，失败即终止，不回退到未验证内核。

## 实验边界

RX 仅支持显式 `packet-up`、原生 H1/H2，只用于 Cloudflare 入口；启用 TLS 时必须验证证书。小载荷或本地故障测试通过不代表完整 1 GiB、持续吞吐或多用户容量达标。

## 许可证

- 运行层：GPL-3.0-or-later 及附加声明，[`docs/reference/runtime/LICENSE`](docs/reference/runtime/LICENSE)。
- 协议层（`internal/protocol/`）：MPL-2.0，[`docs/reference/protocol/LICENSE`](docs/reference/protocol/LICENSE)。
- 文件内其他版权与许可声明继续有效。RayBox-rx 是独立项目，与上游无官方关联；分发二进制须遵守各适用许可证并提供对应源码。

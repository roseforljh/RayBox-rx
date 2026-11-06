# RayBox-rx

sing-box 外壳 + 内嵌 Xray-RX 的组合内核。

sing-box 负责 TUN、DNS、路由和受保护拨号；Xray-core 提供 Xray 协议和实验性 XHTTP RX 增强。保留 VLESS + REALITY、普通 XHTTP 和 WebSocket。RX 默认关闭。

## 源码目录

- [`sing-box/`](sing-box/)：基于 sing-box v1.14.3，含 Xray 出站与 Android libbox 构建脚本。
- [`Xray-core/`](Xray-core/)：基于 Xray-core v26.10.31，含 RX、OOB、TLS 修复和相关测试。
- [`SOURCE.md`](SOURCE.md)：源码来源、许可证与导入范围。
- [`VALIDATION.md`](VALIDATION.md)：本次检查结果与已知间歇失败。
- [`Xray-core/docs/xray-rx-usage.md`](Xray-core/docs/xray-rx-usage.md)：RX 使用说明。

为保持上游结构和现有集成，Go 模块路径、子目录名、RX 协议头及历史补丁名称保持不变。`sing-box/go.mod` 使用相邻的 `../Xray-core`，克隆本仓库即可取得两套对应源码，不需要子模块。

## 构建

需要 Go 1.27 或更高的兼容工具链。本项目已使用 Go 1.27.3。

独立 Xray 内核，在 `Xray-core/` 执行：

```sh
go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X github.com/xtls/xray-core/core.build=RayBox-rx" -o ../artifacts/raybox-rx-xray ./main
```

独立 sing-box 外壳，在 `sing-box/` 执行：

```sh
go build -trimpath -buildvcs=false -o ../artifacts/raybox-rx ./cmd/sing-box
```

Android AAR 在 Windows 或 Linux 的 PowerShell 7 执行：

```powershell
./sing-box/build-libbox.ps1
```

AAR 构建使用 Go 1.27.3、JDK 17、Android SDK 37、NDK 29.0.14206865 和 gomobile v0.1.13。标准与 legacy 均编译 `armeabi-v7a`、`arm64-v8a`、`386` 和 `amd64` 四个 ABI；候选输出到 `artifacts/libbox-candidate/`，正式输出到 `artifacts/libbox/`。`build.json` 记录本仓库完整提交号、源码摘要、架构及两个 AAR 的 SHA-256。产物不包含在源码仓库。

### Android 内核发布

GitHub Actions 的 **Build and release RayBox Android core** 直接编译本仓库的 `sing-box/` 和 `Xray-core/`，不下载官方源码、不应用 App 仓库补丁。内核改动应先在本仓库提交并推送，然后为该提交建立 `android-<完整提交号>` 标签，再在相同提交上触发工作流：

```sh
commit=$(git rev-parse HEAD)
git tag "android-$commit" "$commit"
git push origin "android-$commit"
gh workflow run android-core.yml --ref "android-$commit" -f publish=true
```

工作流在 push 或默认手动运行时只编译验证并保存 Actions artifact，不创建 Release；只有显式 `publish=true` 才发布。先运行受保护拨号、XHTTP/RX 和证书测试，再构建 AAR。Release 包含 `libbox.aar`、`libbox-legacy.aar` 和 `build.json`。重试保留已有资产并检查实际内容，内容不同直接失败，不覆盖草稿或已发布文件。App 应锁定完整提交标签，下载后验证实际标签提交、来源、文件大小、SHA-256 和 AAR 内容；任何下载或验证失败都应终止，不能回退到未验证内核。

### Windows 原生核心

```powershell
./sing-box/build-windows.ps1 -AllowDirty
```

PowerShell 7 构建 `sing-box/cmd/sing-box` 的 amd64 PE，不是独立 Xray server。候选写入 `artifacts/windows-candidate/`，记录完整源码摘要、双核版本、资产大小与 SHA-256；构建前后源码不一致或回环 Clash API/selector 检查失败均不提升候选。测试不启用 TUN 或系统代理，也不连接远程节点。正式构建要求干净源码，发布仍须手动 `publish=true`。

### Linux 服务器内核发布

GitHub Actions 的 **Build and release RayBox server core** 在每次 `main` 提交后构建独立 Xray 服务器内核，支持 Linux `amd64`、`arm64` 和 `armv7`。构建使用 Go 1.27.3、`CGO_ENABLED=0`、`GOWORK=off`，产物来自 `Xray-core/main`，不使用 Android AAR 或官方预编译核心。

服务器 Release 仅在手动 `publish=true` 时创建，使用不可变标签 `server-<完整提交号>`，包含：

- `xray-linux-amd64`
- `xray-linux-arm64`
- `xray-linux-armv7`
- `manifest.json`
- `SOURCE.txt`
- `LICENSE`

`manifest.json` 记录仓库、完整提交号、版本、Go 版本，以及每个架构的文件名、大小和 SHA-256。SimpleNet 消费时必须锁定具体 `server-<完整提交号>`，下载后校验 manifest、文件大小、SHA-256 和目标架构，不使用可变 `latest` 地址，也不把 GitHub Token 下发到节点。

本地 Linux 三架构候选可用 `python -I tools/build_server_candidate.py --allow-dirty` 构建；输出独立 `artifacts/server-candidate/` 并检查真实 ELF、size/hash、构建前后源码摘要，不部署服务器。amd64 运行与配置检查需在 Linux/WSL 执行，交叉编译不代表运行验证。

原始补丁导出工具 `sing-box/export-simplenet.ps1` 需要显式指定 `-SimpleNet`、`-XrayRepository`、`-SingBoxRepository`；后两项必须是包含原始基线提交对象的独立仓库，当前组合快照不附带那些历史对象。普通构建无需运行导出工具。

## 最小集成检查

在 `sing-box/` 执行：

```sh
go test ./protocol/xray -count=1
```

其余测试按改动范围选用，禁止将命令列表当成已经通过的验证结果。

## 实验边界

RX 仅支持显式 `packet-up`、原生 H1/H2。双方须协商恢复、有限响应段及并发扩展；未配置控制密钥时保留原有兼容路径。启用 TLS 时必须正常验证证书。公网 HTTP/80 RX 要求双方配置独立 `rx.authKey` 并保留 VLESS 正文认证加密；保护范围、测速与限制见 [HTTP/80 安全加固](Xray-core/docs/xhttp-rx-security-20261030.md)。

小载荷或本地故障测试通过，均不代表受限入口完整 1GiB、持续吞吐或多用户容量达标。普通 XHTTP 可用入口的成功结果不能归功于 RX。此源码快照不提供生产可用性承诺，详细结果与历史阶段见脱敏文档。

## 许可证与来源声明

两套上游源码保留各自许可证及版权声明：

- [Xray-core：MPL-2.0](Xray-core/LICENSE)
- [sing-box：GPL-3.0-or-later 及原许可证附加声明](sing-box/LICENSE)

RayBox-rx 是独立组合项目，不代表与上游存在官方关联。分发组合二进制时必须遵守各适用许可证。

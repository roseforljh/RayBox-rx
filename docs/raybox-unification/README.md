# RayBox 统一迁移总计划

RayBox-rx 是四个项目唯一的内核源码入口。所有 Android AAR、Windows 原生核心、Linux server core 必须由本仓库源码构建，并由不可变 commit manifest 绑定。

## 核心工作

1. 建立 repository → 完整 commit → platform tag → build.json/manifest.json → 资产 SHA-256 的唯一身份链。
2. 逐文件移植 KunBox 定制补丁，不能直接把 v1.14.4 patch 整体应用到 RayBox 基线。
3. 标准 VLESS/VMess/Trojan 扩展协议统一通过 `protocol/session` 与 `internal/runtime` handler 运行；`type=xray` 仅保留为旧配置兼容标识，不创建第二个完整 `core.Instance`，也不使用 `context.Background()` 破坏保护链。
4. 保留 KunBox 当前实际使用的 `CommandServer.pause/wake/resetNetwork` 和平台回调；不恢复旧私有 API，补安全预算与接口定向测试。
5. Android AAR 统一四 ABI（ARM32/ARM64/x86/x86_64）和 NDK29；server workflow 构建 Linux amd64/arm64/armv7；新增 Windows x64 原生核心 workflow。push 仅编译，发布须显式手动开启。
6. RX/OOB 默认关闭，显式启用时严格校验；detour/TCP fast open/OOB 冲突必须明确失败。

## 当前状态

- 安全补丁、平台接口及 Android/Windows/server 构建门禁已实现，三平台本地候选与三个客户端的配置契约检查已通过；具体记录见各项目验证文档。
- 2026-11-05 用户将本轮范围限定为 RayBox Android 正式内核及 KunBox 测试，并明确授权本地提交和 Android tag；其他客户端暂不处理，不进行远端推送或 GitHub Release。
- Android 正式 AAR 从干净提交重新构建，使用真实 `android-<完整提交>` tag，实际检查两份 AAR 的四 ABI、API、ELF 对齐、安全标记和 SHA-256 后接入 KunBox；不能重标旧 dirty 候选。
- 构建后的身份、哈希与验收结果写入被忽略的 `artifacts/` 及 KunBox 消费端文档，不回写冻结源码，以免使正式构建再次变脏。
- Android 真实 TUN/Root/Proxy-only 仍由用户通过 KunBox 实机测试；构建和配置测试不替代真实线路验收。

## 产物门禁

每个发布资产必须包含 repository、commit、version、architecture、toolchain、size、sha256、RX/OOB 默认值、源码和许可证信息。已发布 tag 再构建时必须比较 manifest 与资产，发现差异即失败。仓库不提交 AAR、EXE、server 二进制、凭据、私钥或现场敏感日志。

详细进度见 `STATUS.md`。

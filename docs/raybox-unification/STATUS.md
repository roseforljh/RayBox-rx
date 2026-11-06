# 迁移过程状态

## Android 正式内核冻结（2026-11-05，最新）

- 本轮只处理 RayBox Android 正式 AAR 与 KunBox 安装测试包；用户已明确批准本地源码提交及 Android tag，不包含远端推送或 GitHub Release。
- 已完成的三平台 dirty 候选均对应 `c8349f9ea677d56bc107b34349c4d4ebb5a5aa7e`、`sourceDigest=3d77f9554e5d4b2f95dbbca37f05717c6a75a72121e0db8a92ffa70d27b55bec`，仅保留为历史候选，不能重标为正式 AAR。
- 正式流程：冻结并提交已验证源码 → 创建实际指向该提交的 Android tag → 不带 `-AllowDirty` 重建标准及 legacy 四 ABI AAR → 实检来源、工具链、Java API、ELF、安全标记及哈希 → 安装到 KunBox 并生成测试 APK。
- 正式构建前复验 dialer/direct/group/vless/xray/route/wireguard/build_shared 共 8 个 Go 包，全部通过；AAR 检查工具 5 项单测通过。
- Xray splithttp/tls/infra/conf 共 3 个 Go 包的 RX/配置/证书定向测试通过；sing-tun 根包的 egress/mixed/TCP 关闭定向测试通过。Windows 专属上游 `internal/winipcfg` 测试存在 `Errorf(%w)` vet 告警，本轮不修改无关上游代码、不运行无关 Windows 包；ping 在该定向过滤器下无匹配测试。
- Go 编译缓存与临时文件写入 D 盘 `artifacts/build-cache/`。构建完成后的准确提交、哈希及耗时保存在 ignored artifacts 和 KunBox 文档，冻结后不再回写本仓源码。
- Windows/SimpleNet 原候选、来源锁及部署保持原样；Android 真机 TUN/Root/Proxy-only 由用户通过 KunBox 测试。

以下为历史阶段记录，以本节和各产物实际 manifest 为准。

## 2026-11-05

- 完成四仓库只读盘点。
- 写入统一总计划：`C:\Users\33039\.claude\plans\parsed-squishing-pnueli.md`。
- 在本项目创建 `docs/raybox-unification/README.md`。
- 确认 Android、Linux server 和 Windows 产物必须由本仓库同一完整 commit 构建并由 manifest/SHA-256 绑定。
- 第一阶段仅规划；当前开始 Go 安全补丁、Android/server 显式发布门禁及 Windows 原生构建实现。
- 主会话负责 `sing-box/build-libbox.ps1`、`tools/check_android_artifacts.py` 与迁移文档；Go、workflow、Windows 构建各自隔离写入。
- Android 实检脚本最初 3 项 stdlib 单测通过，现已增至 5 项（含原安全标记不可削弱回归）；实际检查现有标准/legacy 两 AAR：均含四 ABI，64位 LOAD 对齐16384字节、32位4096字节，关键 Java API 存在。这仅证明旧产物结构，不代表源码身份/新补丁已通过。
- 旧产物标准 AAR：141246738 字节，SHA256 `0333400c60de3957d113ce537e46aa88c017696e375076a8085ef10a4651615c`；legacy：121012379 字节，SHA256 `bbffb1ff3839a66f6c787905d0ecbf1cc0819f05c8c706f5f89254739eab2486`。
- 主会话 `gh api repos/roseforljh/RayBox-rx/git/ref/tags/android-f38675b6269e6d116e78931bce9880e30f587092` 实证 object.sha 为 `c8349f9ea677d56bc107b34349c4d4ebb5a5aa7e`，与标签名声明不同，不更新客户端 lock 冒用该旧产物。

## 核心预算补充

- WireGuard 的额外 UDP egress 成员在建立 socket 前预留物理连接预算，监听失败、接口移除和关闭时释放；修正 egress 附加接收函数被误算为 anchor socket 的问题。定向 race 测试 6 项通过。
- WireGuard 与 OpenVPN 服务端 UDP egress 现在共用 `dialer.ReservePhysicalSocket`，创建前预留，监听失败、接口移除及重复关闭时释放。common/dialer、WireGuard、sing-tun 定向测试及 OpenVPN 编译均通过。
- Tailscale 在本次 Android/Windows 产物中禁用。libbox 的独立 STUN、网络质量和 F-Droid 探测工具不在 KunBox 当前调用链，暂不扩展其直拨实现。Mixed 栈两处启动失败分支仍缺故障注入测试。
- 上述源码修改发生在既有候选构建之后，既有候选的源码摘要不包含这些修改。

## 当前硬阻塞

- KunBox 当前实际使用的 `CommandServer.pause/wake/resetNetwork` 已存在；待移植的是安全补丁及验证 API/平台/ABI，禁止恢复旧私有 API。
- SimpleNet 和 dispatch 当前锁定的提交身份不统一。
- Android/server/Windows 三条 workflow 已建立；显式发布、不可变资产和清单一致性仍在逐项本地检查。
- Windows 客户端原生配置与安装链正在独立实现，不能把候选 EXE 的 CLI 成功当作客户端已接入。

## 统一构建准备（以此节替代上方早期进度）

- KunBox 安全预算、sing-tun 4096 TCP 上限、ICMP、WireGuard bind 与 egress UDP socket 补丁已写入受控源码，egress 补丁正在独立定向复核。
- Android 标准/legacy dirty AAR 已成功实检四 ABI、ELF对齐、Java API及全部安全标记；KunBox已接入首次候选，平台接口32项测试通过。此次预算变更完成后必须重建，不沿用旧候选摘要。
- `build_shared.ReadTag` 使用12位提交前缀；读取基线失败会终止。Windows CLI现在同时输出sing-box与Xray实际版本，构建脚本验证两者。
- Android/Windows/server 构建统一使用 `sha256-git-path-kind-content-v1`，dirty候选tag=null且独立目录；Windows workflow旧摘要算法预期已修正。
- 三平台输出附带源码说明与对应LICENSE，Windows校验实际PE、运行check/run/Clash API/selector两次切换；server校验amd64/arm64/armv7实际ELF，manifest保留Xray版本。
- 路由、selector、VLESS、build_shared 与 Xray RX/OOB/TLS/conf 定向 Go 测试通过；三条 workflow YAML与两份PowerShell脚本语法通过。

## 构建冻结和剩余验收

1. 预算复核通过后冻结本仓全部非忽略文件，并行构建Android/Windows/server；构建前后和三个manifest必须同commit、同dirty、同sourceDigest。
2. 新候选交由KunBox、SimpleNet、KunBoxForWindows执行真实配置检查和定向编译；未连接手机，Android真实TUN/Root/Proxy-only需设备。
3. 正式commit/tag/lock必须等待用户明确提交/发布授权，不能以dirty候选或错配旧tag代替。
4. 结果更新在三客户端文档中记录；若改动本仓非忽略文档也会改变完整源码摘要，现有候选仍保留其构建时冻结快照身份，不伪改manifest。

# 统一迁移实现检查点（2026-11-05）

## 已完成的本地纠错

- 恢复原 5 项安全标记契约，尤其 `system TCP connection limit reached: active=`；不能用物理拨号预算标记代替 sing-tun TCP 限额。dirty 候选同样必须严格通过。
- Android 构建统一 PowerShell 7，合并 AAR 结构/API/安全检查，使用两个直接 `outputs` 对象；不再为 JSON 数组格式反复完整编译。
- Android/Windows 先生成 staging，再核对源码摘要并提升候选目录；失败保留原候选。
- KunBox 旧 AAR 下的 `OutboundFixer` 提前接线已撤回，纯转换器保留。不能在新 AAR 验收前生成旧核心不认识的 `type=xray`。
- KunBox 新增本地候选安装工具：验证源码摘要、两份 AAR、四 ABI、Java API、完整安全标记、真实 size/hash，并保存旧 AAR；第二个文件替换失败会恢复旧 AAR。不伪造正式 lock。
- Windows 候选验证新增实际回环 Clash API 与 selector 切换；不启用 TUN/系统代理，不连接公网，只终止测试自有进程。

## 已实际通过

| 范围 | 实际结果 |
|---|---|
| Android 实检 Python | 5 项通过，含原安全契约固定回归及 ELF 边界/对齐破坏样例 |
| PowerShell 7 清单 | 两对象 JSON 序列化往返通过；已有候选清单 outputs 也确为两对象 |
| KunBox 回归检查点 | OutboundFixerTest 43 项 + 转换器 5 项，共 48 项；失败/错误 0，Detekt 通过，37 秒 |
| KunBox 候选安装工具 | 3 项通过：显式 dirty/备份、来源或安全失败不替换、第二文件失败回滚 |
| Gradle 来源任务 | 无新核心安装时 verifyRayboxCore 可运行；14 秒。尚不是新 AAR 通过 |
| Windows 旧候选 CLI/API | version/check/run、回环 /version 和 /proxies、Xray→direct 两次 selector 切换、自有进程清理通过 |
| Xray 定向边界 | splithttp RXBuffer/OOB、TLS CertificateReload、infra/conf TestXrayRX；3 个 Go 包通过，0.642/0.050/0.047 秒 |

以上均为本地检查，不是 Android 真机、Windows TUN、公网 RX/OOB 或正式发布验收。

## 当前产物与来源

旧的 Android dirty 候选：标准 124538174 字节，SHA256 `75bc725962fd9cb8075b98b5aa074667cb8630ee825f2de0d76c4c711f06c02b`；legacy 104635486 字节，SHA256 `685b0cf80028ba126916a7462c986d44ab668e8fe41ab4a53e6745c972b9bee8`。该版本缺原 sing-tun TCP 限额，不能消费。后续新候选以各自真实清单为准，不沿用这些 hash。

RayBox 源码仍基于 sing-box 1.14.3 与 Xray 26.10.31，不能把移植 KunBox 1.14.4 补丁说成上游基线升级。当前实现为 dirty，正式不可变 SHA 仍需要项目提交授权后才产生。

## 实现分工和剩余

- 受控 sing-tun 源码、TCP/NAT/mixed/ping 与 direct ICMP 预算正在补齐；模块替换必须纳入源码摘要，不能修改 module cache 或把补丁放入 ignored artifacts。
- SimpleNet fetch ZIP/ELF/tag/size 校验加固、Windows 正式与测速原生路径和安装链、三 workflow 不可变发布门禁、KunBox parser/exporter 字段保真分别独立实现；完成后主会话核实实际结果。
- RayBox 全部写入停止后才启动一次 Android/Windows/server 候选构建；构建期间不再修改该仓代码或文档，防止 sourceDigest 变化。
- 新 AAR 完整通过后，才适配 useProcFS 并接入运行配置，完成最小编译和来源门禁。真实设备 UI 仍由用户操作。
- 先前 5 个实现代理已停止且未回传最终报告；旧摘要中的结果仅采用主会话实际核实部分。本检查点的新实现代理须全部结束并核实后才能最终总结。

# sing-tun 受控源码

- 上游：<https://github.com/SagerNet/sing-tun>，模块 `github.com/sagernet/sing-tun`，版本 `v0.9.7-0.20261009020158-bca4bf029237`（提交前缀 `bca4bf029237`）。原许可证见 `third_party/sing-tun/LICENSE`。
- Go 模块原始 zip SHA-256：`4f678effd40bc6da3916641cdfaa023b3aeef26d1cb21c0df0cf0ee69826ec93`；Go h1：`h1:B9tUcRf1FAatPyzN8sKEvIHMvKNkFMBZikeIOL4gqtQ=`。
- KunBox 补丁 `sing-tun-v0.9.7-0.20261023115432-040db71fcd33.patch` SHA-256：`86b147c13a6b8bc59820bc2abfeedc6560920f2e226c2082404c3169d22d46f7`；补丁基于较新版本，已针对本底座 NAT 方法签名调整。
- `sing-box/go.mod` 使用 `../third_party/sing-tun`，源码随仓库纳入构建摘要，不使用模块缓存副本。模块内 `.gitignore` 明确保留上游四种架构的 Wintun 嵌入 DLL 和 README；这些 DLL 是 Go `embed` 编译输入，来自上述原始模块 zip。补丁包含跨 IPv4/IPv6 共享 4096 活跃 TCP 上限、连接及启动关闭清理、accept 资源耗尽重试、ping 注入连接器与关闭回调。旧版底座已包含跳过占用 NAT 端口的分配逻辑，保留并用补丁中的回卷测试验证。
- 在 `sing-box` 目录用本仓 Go 工具链执行：`go test github.com/sagernet/sing-tun github.com/sagernet/sing-tun/ping -run 'Test(System|TCPNatDoesNotOverwrite|DestinationClose|PortUsesDestinationConnector)' -count=1`；`go test -tags with_gvisor github.com/sagernet/sing-tun -run '^TestMixedCloseWithoutGVisorStackClosesSystem$' -count=1`。

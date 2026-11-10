# 独立服务器安装

在自己的 Linux VPS 上以 root 执行，无需后台账号或预填公网 IP：

```sh
curl -fSso /tmp/raybox-deploy.sh https://raw.githubusercontent.com/roseforljh/RayBox-rx/main/deploy.sh && bash /tmp/raybox-deploy.sh
```

默认创建 `vless-xhttp-reality` 节点，自动生成名称、检测公网 IP 和可用伪装目标，安装完成后打印 `vless://` 链接。公网 IP 检测失败或使用 NAT 时，可以显式传入 `--host`。

使用已有域名和有效证书安装 TLS：

```sh
bash /tmp/raybox-deploy.sh --protocol vless-xhttp-tls --domain node.example.com --tls-cert /etc/tls/fullchain.pem --tls-key /etc/tls/privkey.pem
```

`--protocol` 支持 `vless-xhttp-reality`、`vless-xhttp-tls`、`vless-xhttp-h2`、`vless-xhttp-h3`；H3 需要 UDP 可达。H2/H3 使用 443 端口，TLS POST 和 REALITY 自动选择可用 TCP 端口。安装器不会覆盖同名实例，也不会修改防火墙。

核心直接来自本仓库既有 `server-v0.0.1` Release，按架构下载 `raybox-linux-*` 并校验固定 SHA-256；不下载官方 Xray 或 sing-box，不请求 CF Worker 或 SimpleNet 后台。已安装且校验一致的核心直接复用。

脚本由 SimpleNet 后端的 `scripts/build-standalone-installer.ts` 生成，内嵌现有四协议安装逻辑。更新协议安装器后重新生成，执行 `bash -n deploy.sh` 并验证真实代理请求，再更新公开脚本。测试证书必须由测试 CA 正常签名和校验，不能以跳过证书校验代替验证。

2026-11-10 本机 Debian 验证：四协议安装成功、服务 active、分享链接生成，使用 RayBox 客户端经各节点请求 HTTPS 204 地址均返回 204。指定外部 VPS 的验证另行进行，不能用本机结果代替公网入站验证。

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

脚本由 SimpleNet 后端的 `scripts/build-standalone-installer.ts` 生成，保留四协议安装逻辑，并嵌入 `scripts/standalone-protocols.py` 的完整协议实现。更新后重新生成，执行 `bash -n deploy.sh` 并验证真实代理请求。测试客户端必须信任具体测试证书，不能以跳过证书校验代替验证。

2026-11-10 本机 Debian 验证：原四协议 HTTPS 204 通过；新增完整协议分支 14 协议均安装成功、服务 active、分享链接生成，13 协议实际代理 HTTPS 204，Naive TLS CONNECT 带认证 / padding 转发 1024 字节内容及 SHA-256 一致。ACME 用本地 Pebble 测试 CA 完成 certbot 真实 HTTP-01 签发并通过严格链验证；Argo 快速隧道真实公网域名 204；CDN 回源用 TLS 边缘转发到源站端口同样 204。验证只确认节点可用，未跑俄罗斯线路。

## 完整面板参数

```sh
bash /tmp/raybox-deploy.sh --protocols=xhpt:2053,tupt,sspt --sni=gateway.icloud.com --tls-mode=self-signed
```

支持 `vlpt,xhpt,vxpt,vwpt,xupt,xcpt,tupt,anpt,nvpt,arpt,sspt,hypt,vmpt,sopt`。每项可指定 `:端口`，未填写则自动选择空闲端口。完整协议分支要求 systemd、Python 3，安装器自动准备 Python / curl / openssl；使用 `--name` 固定名称可在重跑时复用已完成节点，不覆盖已有配置。

- Reality：`--sni`，未填写则检测选择。
- TLS：`--tls-mode=self-signed|acme|manual`；ACME 用 `--domain`，在终端确认条款；手动用 `--cert` / `--key`，支持带空格路径。自签链接明确携带 `allowInsecure=1&insecure=1`；严格验证的客户端配置另存节点目录。
- XHTTP：`--xhttp-mode=post|get-header`。GET Header 使用 4096 字节上传分包，不启用 RX。
- WS：`--ws-mode=cdn|direct|argo`、`--ws-host`、`--ws-path`。Argo 用 `--argo`，可选 `--argo-token` / `--argo-domain`；固定隧道回源在 Cloudflare 配置到打印的本机端口，临时隧道自动获取域名。
- SS2022：`--ss-pass`（16 字节 base64）；Hy2：`--hy2-auth` / `--hy2-up` / `--hy2-down`；Tuic：`--tuic-cc`；Naive：`--naive-user` / `--naive-pass`。未提供的认证值自动生成。

VLESS / VMess 使用固定服务器 Release 核心。Tuic / AnyTLS / Naive / Any-reality / SS2022 / Hysteria2 / SOCKS 使用自有完整 RayBox：首次从校验过的公开提交 `1880528af97c4ac3bc44a6c304321995539bccd3` 源码自动构建 Go 1.27.3 核心，以后直接校验复用。没有下载其他项目的代理核心，也不替换既有节点二进制。首次完整核心构建需要额外时间、内存和磁盘。

新分支安装目录 `/etc/raybox/nodes/名称`，服务 `raybox-名称`，保存 `config.json`、`client.json`、`result.json`、`share-link.txt`，均限制读取权限。ACME 正式签发 / 续期和真实 CDN / Argo 回源本轮未做外部全链路验证。

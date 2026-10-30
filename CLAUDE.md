# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目与布局

项目名称为 **RayBox-rx**，根目录为组合源码Git仓库。`sing-box/` 和 `Xray-core/` 是普通源码目录，保留各自上游Go模块路径。Go命令从对应模块执行，Git命令从仓库根目录执行。

`sing-box/go.mod` 用 `replace github.com/xtls/xray-core => ../Xray-core` 接入本地增强内核。`sing-box/protocol/xray/outbound.go` 将Xray实例作为出站，Xray拨号经 `internet.UseAlternativeSystemDialer` 回到sing-box受保护拨号器；该链依赖 `core.FromContext(ctx)`，不能用 `context.Background()` 丢弃实例上下文。

## 构建与检查

要求Go1.27，已有验证工具链为1.27.3。正式Xray入口为 `Xray-core/main`，sing-box入口为 `sing-box/cmd/sing-box`。命令与Android环境见根README。AAR脚本为 `sing-box/build-libbox.ps1`，输出 `artifacts/libbox/`。

只运行与改动有关的检查，纯文档不构建：

```sh
# sing-box/ 下
go test ./protocol/xray -count=1
# Xray-core/ 下
go test ./transport/internet/splithttp ./transport/internet/tls ./infra/conf -run '^(TestRX|TestXrayRX|TestCertificateReload)' -count=1
go run ./infra/vprotogen -mode check
# 根目录
git diff --check
```

并发/生命周期变更才使用相关定向race，需要CGO和C工具链。protobuf来源是 `.proto`，用仓库 `infra/vprotogen` 生成。格式工具为 `infra/vformat`，不得为消除无关上游格式问题批量重写。历史上游vet告警不算本次引入。

## RX流程

JSON `xhttp` 映射内部 `splithttp`，`infra/conf/transport_method.go` 构建protobuf配置，`rx_config.go:NormalizeRX` 为公共校验。`extra` 除host/path/mode外替代外层配置，不能当作普通增量合并。

`rx_server.go` 管会话和认证，`rx_buffer.go` 用有界回放缓存和单调累计ACK。`rx_client.go` 把多个HTTP下行呈现为有序reader；`rx_parallel.go` 处理协商后的有界并发段及缺段补发，只有交付上层的字节能推进ACK。恢复、控制及上传使用独立transport，不把H2新stream误当成新TCP。上传重试与下行恢复共享终身故障预算。

RX默认关闭，只支持显式packet-up、原生H1/H2，保留现有普通XHTTP、REALITY和WebSocket。首次完全无RX头可回退；部分确认或协商后能力丢失必须终止。真实TLS验证不能关闭；capability、nonce、认证头和完整认证URL不得进入新诊断日志。阶段测试不能外推真实线路、完整1GiB或多用户容量。

## 发布边界

不提交私有凭据、认证配置、抓包、原始现场日志、便携工具链和二进制。实验产物独立命名，不替换生产。提交、推送、部署和远端实验需要用户明确授权；保留所有适用上游许可证。

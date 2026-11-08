# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 当前研究交接

继续受限入口研究前完整阅读 `HANDOFF-rx-speed.md`。目标是实际1至3MB/s及完整1GiB字节/hash一致；旧实验工作区已迁入仓库外私有归档，本机位置见被忽略的 `artifacts/migration/LOCAL-HANDOFF.md`。不得在旧路径重新启动任务，也不得将归档中的凭据或原始现场日志提交Git。

## 项目与布局

项目名称为 **RayBox-rx**，根目录是单一Go模块 `github.com/roseforljh/RayBox-rx`，Go与Git命令都从根目录执行。产品版本只在 `VERSION`，构建注入 `constant.Version`。运行层在根 `adapter/ route/ dns/ option/ protocol/ transport/ service/`，扩展协议实现在 `internal/protocol/`，入口 `cmd/raybox`、`cmd/raybox-server`，Android绑定 `sdk/android`（Java包 `io.raybox.raybox`）。

扩展协议拨号必须回到RayBox受保护拨号器：`internal/protocol/transport/internet` 的系统拨号经 `internet.UseAlternativeSystemDialer` 回调，依赖请求上下文中的协议实例/服务，不能用 `context.Background()` 丢弃。VLESS、VMess、Trojan 的标准入口统一走 `protocol/session` 与 `internal/runtime` handler 直调；`type=xray` 仅作为旧配置兼容标识，不是独立产品或生产实例路径。

## 构建与检查

要求Go1.27.3，`GOTOOLCHAIN=local`；Go缓存放D盘（`GOCACHE=D:/RayBox-rx/artifacts/build-cache/go`）。命令与Android环境见根README，构建脚本在 `tools/`，输出 `artifacts/`。

只运行与改动有关的检查，纯文档不构建：

```sh
go test ./protocol/session ./protocol/streamconn ./common/dialer -count=1
go test ./internal/protocol/transport/internet/splithttp ./internal/protocol/transport/internet/tls ./internal/protocol/infra/conf -run '^(TestRX|TestXrayRX|TestOOB|TestCertificateReload)' -count=1
go run ./tools/vprotogen -pwd internal/protocol -mode check   # 另有 daemon、experimental/boxdd、experimental/v2rayapi、transport/v2raygrpc；make proto_check 全查
python -I tools/test_check_android_artifacts.py
python -I tools/test_release_workflows.py   # 改 workflow 发布步骤时
git diff --check
```

并发/生命周期变更才使用相关定向race，需要CGO和C工具链。protobuf来源是 `.proto`，用 `tools/vprotogen` 生成。格式工具为 `internal/protocol/infra/vformat`，不得为消除无关上游格式问题批量重写。历史上游vet告警不算本次引入。

## RX流程

JSON `xhttp` 映射内部 `splithttp`，`infra/conf/transport_method.go` 构建protobuf配置，`rx_config.go:NormalizeRX` 为公共校验。`extra` 除host/path/mode外替代外层配置，不能当作普通增量合并。

`rx_server.go` 管会话和认证，`rx_buffer.go` 用有界回放缓存和单调累计ACK。`rx_client.go` 把多个HTTP下行呈现为有序reader；`rx_parallel.go` 处理协商后的有界并发段及缺段补发，只有交付上层的字节能推进ACK。恢复、控制及上传使用独立transport，不把H2新stream误当成新TCP。上传重试与下行恢复共享终身故障预算。

RX默认关闭，只支持显式packet-up、原生H1/H2，保留现有普通XHTTP、REALITY和WebSocket。首次完全无RX头可回退；部分确认或协商后能力丢失必须终止。真实TLS验证不能关闭；capability、nonce、认证头和完整认证URL不得进入新诊断日志。阶段测试不能外推真实线路、完整1GiB或多用户容量。

## 发布边界

不提交私有凭据、认证配置、抓包、原始现场日志、便携工具链和二进制。实验产物独立命名，不替换生产。提交、推送、部署和远端实验需要用户明确授权；保留所有适用上游许可证。

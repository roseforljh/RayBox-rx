# 三平台工作流门禁

## 触发与权限

- push `main`：构建、验证、保存 CI artifact，并进入发布 job（用户 2026-11 前确定的“推送 main 自动编译并发布”）。
- `workflow_dispatch` 的 `publish` 默认 `false`，只构建；显式 `true` 才发布。
- 发布 job 才有 `contents: write`。本文档不授权触发工作流、发布、提交或推送。

## 来源身份

- 三个工作流都使用 `roseforljh/RayBox-rx` 的完整 40 位小写 SHA。产品版本取自 `VERSION`（语义版本 `X.Y.Z`），运行版本不再追加提交 SHA；旧 `<VERSION>-<SHA前12位>` 形式仅作为历史兼容被消费端继续接受。
- 入口：Android `sdk/android`，Windows `cmd/raybox`（`raybox-windows-amd64.exe`），server `cmd/raybox-server`（`raybox-linux-{amd64,arm64,armv7}`）。server 不当客户端核心。
- 平台标签为 `android-v<VERSION>`、`windows-v<VERSION>`、`server-v<VERSION>`（历史 `platform-$SHA` 标签保持不可变、不移动）。标签存在时必须解引用到 `GITHUB_SHA`；同一版本已绑定另一提交必须失败，不得覆盖，正式发版先更新 `VERSION`。
- 草稿 Release 没有 Git 标签，以 release 的 `target_commitish == GITHUB_SHA` 确认身份；已发布 Release 必须有指向本提交的标签。
- 发布 job 的下载目录（server 的 `downloaded/`）已加入 `.gitignore`，避免来源摘要把 CI 工件误判为 dirty。
- 只有 GitHub API 明确返回 HTTP 404 才视为“不存在”；认证、限流、网络等其他错误直接失败，不得当成不存在而新建草稿。
- 同 tag 名出现多个 Release 直接失败。
- manifest 的 repository、commit、版本、架构、工具链、资产 size/SHA-256、默认 RX/OOB 和 source/license 信息必须匹配真实资产。

## 不可变资产

- 已存在的发布或草稿 release 不使用 `--clobber`，不覆盖已有资产。
- 已发布 release 的全部资产必须存在且与本次构建逐字节一致；差异直接失败，一致则复用并结束。
- 同一 commit 重建时 manifest 的 `builtAt` 等字段可能变化。已存在的 manifest 保留原文件：已发布时校验其声明与下载资产一致；草稿时校验其声明与本次要上传的二进制一致。
- 草稿只补传缺失资产，已有资产必须与本次构建逐字节一致；已发布 release 缺失资产直接失败。

## 最短本地检查

```sh
python -I tools/test_release_workflows.py   # 假 gh 回放：首次发布、同 SHA 重跑、部分草稿、过期草稿、API 500、草稿指向他提交
git diff --check
```

不在本轮实际触发工作流、发布、提交或推送。

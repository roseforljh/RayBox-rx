# 三平台工作流门禁

## 触发与权限

- `push` 只执行源码验证、构建并保存 CI artifact，不进入发布 job。
- `workflow_dispatch` 的 `publish` 默认为 `false`；只有显式传入 `true` 才进入拥有 `contents: write` 的发布 job。
- 本文档不授权触发工作流、发布、提交或推送。

## 来源身份

- 三个工作流都使用 `roseforljh/RayBox-rx` 的完整 40 位小写 SHA。
- `android-$SHA`、`windows-$SHA`、`server-$SHA` 标签必须解引用到实际 commit，并与构建的 `GITHUB_SHA` 相等。
- server 产物来源入口是同一组合提交的 `Xray-core/main`；Windows 产物是 `sing-box/cmd/sing-box` 外壳并内嵌 Xray，不能把 server 可执行文件当作 Windows 核心。
- manifest 的 repository、commit、版本、架构、工具链、资产 size/SHA-256、默认 RX/OOB 和 source/license 信息必须匹配真实资产。

## 不可变资产

- 已存在的发布或草稿 release 不使用 `--clobber`，不覆盖已有资产。
- 已发布 release 的全部资产必须存在且与本次构建逐字节一致；差异直接失败。
- 同一 commit 重建时 manifest 的 `builtAt` 等构建时间字段可能变化。已存在的 manifest 保留原文件，并重新校验其身份、声明的 size/hash 与下载得到的实际资产；所有新建二进制必须与旧 release 的实际二进制逐字节一致。
- 缺失资产只允许在草稿 release 中补传；已发布 release 缺失资产直接失败。

## 最短本地检查

```powershell
Get-Content .github/workflows/android-core.yml -Raw | ConvertFrom-Yaml | Out-Null
Get-Content .github/workflows/server-core.yml -Raw | ConvertFrom-Yaml | Out-Null
Get-Content .github/workflows/windows-core.yml -Raw | ConvertFrom-Yaml | Out-Null
git diff --check
```

不在本轮实际触发工作流、发布、提交或推送。

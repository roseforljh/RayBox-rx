# 源码来源

RayBox-rx 将两套当前源码放在同一仓库，首次导入为组合快照，未伪造或重写两个原仓库的历史。原Go模块路径与目录保持，所有源码修改均直接随仓库提供。

| 组件 | 上游基线 | 本地导入前的HEAD |
|---|---|---|
| Xray-core | 官方 v26.10.29，`0afec66085a85191e9dd2a4a6646c7c9a94688d1` | 同基线，包含工作区未提交RX、TLS、WebSocket与测试改动 |
| sing-box | 官方 v1.14.3 源码包，SHA-256 `9a8e3712a5f611ad95f1d16874bbdcdffbd3b8eed16dae2d71ce7042a33c3386` | `f6bd1de2142b67de342374055ffc25bdbab27f96`，另含工作区集成修改 |

sing-box 本地来源顺序：

1. `92d5d012d71d355c2af2382a398d13665172ebb7`：上游 v1.14.3 源码包。
2. `4e30f6b047b18f54ae757c2d2e67dc33627c74ef`：SimpleNet Xray 出站集成。
3. `748ab30542d99abb41489de5aa314795691e453a`：接入相邻 Xray-RX。
4. `f6bd1de2142b67de342374055ffc25bdbab27f96`：libbox AAR 构建脚本。

本地提交哈希为来源记录，不表示这些对象存在于本组合仓库历史中。上游地址：

- https://github.com/XTLS/Xray-core
- https://github.com/SagerNet/sing-box

## 导入范围

包含两个Git工作区的完整已跟踪源码及审查后的未跟踪源码、测试和构建脚本。不复制原仓库 `.git`，不引入子模块。

私有现场报告被脱敏版本替代；个人路径和只存在于旧工作区的发布包说明已整理。原始凭据、认证配置、私钥、现场抓包/日志、便携工具链、缓存、下载资料、编译二进制和AAR不发布。上游源码公开测试资产保留，测试中运行时生成的证书与身份不属于现场认证配置。

## 许可证

- `Xray-core/LICENSE`：Mozilla Public License 2.0。
- `sing-box/LICENSE`：GNU GPL 3.0 或更高版本，保留原附加声明。
- 其他文件内适用的版权及许可声明继续有效。

本文件不重新许可上游文件。发行组合二进制需遵守所有适用条款并提供对应源码。

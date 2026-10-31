# 发布快照验证

日期：2026-10-30。项目名：RayBox-rx。

## HTTP/80 RX 控制安全加固

- 用户随后明确要求提交并推送。提交前对当前组合源码重新执行 RX/TLS/配置定向 race：splithttp 6.619 秒、tls 1.211 秒、infra/conf 1.107 秒；sing-box Xray 集成检查 0.381 秒、protobuf 生成检查及 Linux 内核构建通过。检查覆盖本次发布的安全、分段恢复及调度改动；现场速度仍对应报告中的独立测速快照，不宣称当前组合源码重新完成远端测速。
- 待推送的 30 个文件通过已知私有值、私钥标记、Go 格式和 diff 空白检查；`artifacts/` 无 Git 跟踪文件，服务器资料与私有配置继续被忽略。
- 新增 `rx.authKey` 配置及双向控制认证加密、防重放、服务端 capability 轮换、上传载荷摘要绑定和公网无密钥保护。测试包括重启后的随机响应 nonce、外部 `spreadAddresses`、关闭 RX 后的普通上传。
- 最终源码定向普通检查通过：splithttp 4.786 秒、tls 0.066 秒、infra/conf 0.062 秒；独立安全快照的定向 race 通过：splithttp 1.330 秒、infra/conf 1.099 秒。此前 protobuf 生成检查和 sing-box Xray 集成检查已通过，本次最后修正没有再改 protobuf 或集成配置。
- 最终独立 Linux 二进制在俄罗斯 → 既有 CF 池 → 英国、HTTP/80 + VLESS 加密条件下完整传输 64 MiB，66.489 秒、1.009323 MB/s，SHA-256 一致。原分段/并发参数保留，未实际下载 1 GiB。
- 收尾只读回查：四组实验服务均停止、实验端口已释放、原服务 PID 和监听一致。私有服务器资料与实验配置由 Git 忽略；现场实验阶段未提交或推送。
- `go vet ./transport/internet/splithttp` 的 9 条 protobuf 含锁结构复制告警与原测速快照逐字相同，未为清除旧告警修改 XMUX。保护范围、性能证据及仍可见的 HTTP 元数据详见 [安全加固报告](Xray-core/docs/xhttp-rx-security-20261030.md)。

## 本次发布检查

- 源码来自两套冻结的Git工作区。复制前后逐文件SHA-256和文件清单一致：Xray-core清单1203项，sing-box清单1575项。私有现场报告以脱敏摘要替代。
- 在新的组合源码目录执行 `go test ./protocol/xray -count=1 -timeout 120s`：通过，0.320秒。工具链Go1.27.3，CGO_ENABLED=0；验证本地Xray替换和现有双内核集成。
- 两个PowerShell构建/导出脚本语法检查通过。未执行AAR构建，也未运行会写入SimpleNet的补丁导出。
- 发布文件的私密标记、测试机地址和个人绝对路径预扫无命中；未复制artifacts、认证配置、密码容器、抓包、原始现场日志或二进制。
- 最终暂存2778个来源文件和5个项目入口文件，无嵌套Git链接，完整性核对通过。首次整仓导入的 `git diff --cached --check` 报695处空白问题，均在与来源逐字一致的既有文件中；本次改动的文档与脚本无新增空白问题，未批量格式化上游。
- 本次发布只改名称入口、可移植说明和两个源码管理相关脚本，未修改冻结快照中的Go协议实现。

## 快照已知失败

源码冻结前，协作会话报告相关包race检查中：TLS与配置包通过，splithttp包因 `TestRXSegmentH2NoImplicitRetry` 失败。该测试使用net.Pipe模拟H2，报1秒超时或 `read/write on closed pipe`，单独运行10次有3次失败，race及非race均可出现。后续已修复测试端握手顺序，见下方定向记录；历史失败记录保留，不宣称全包或全量检查通过。

新增握手竞速、多地址配置、并发段与TCP故障用例的定向race、修改文件格式检查由协作会话报告通过。本次发布未重复执行这些检查，不将协作报告记为本次本地重跑。

## 增量迁移检查（2026-10-30）

- 用户授权迁移发布后续地址择优改动。仅同步splithttp目录5个Go文件，SHA-256与冻结旧工作区逐文件一致；新增脱敏研究交接及CLAUDE入口，没有以旧私有文档覆盖新公开文档。
- 在新项目使用Go1.27.3及CGO_ENABLED=1执行：`go test -race ./transport/internet/splithttp -run '^(TestRXSpreadPrefersHealthyAddress|TestRXSpreadAddressesNormalize|TestRXHedgeDial|TestRXParallel.*)$' -count=1 -timeout 90s`，通过2.101秒。未重跑全包及已知间歇失败用例，不称全绿。
- 旧工作区已完整复制到仓库外私有归档，含隐藏.git，共27069文件、2555404247字节；全部相对路径、大小、SHA-256前后相同后才清理旧内容。旧根空目录因运行会话句柄及工具路径保护仍存在，移除已被工具阻止，不绕过保护、不在里面继续开发。私有归档及本机位置说明均不提交Git。
- Go工具链已放入新项目忽略的artifacts位置。迁移未触发远端实验、AAR构建或生产变更。

## 本地分类工具及H2用例修复（2026-10-30）

- 新增 `Xray-core/scripts/xhttp-classify.py` 及对应说明。默认离线准备；显式运行才使用固定入口TCP门控、普通XHTTP、单次1MiB请求与已知hash。拨号预算与应用请求分别计数，SYN/TLS/内部重试缺失指标保持null；门控分包差异明确记录，不据此宣称直拨或永久受限分类。
- 本会话使用同一个 `python -I Xray-core/scripts/xhttp-classify.py --self-test` 入口完成初版检查，协作复核修复后再运行一次，均通过。覆盖离线输入、extra/RX/TLS、长度/hash/编码判断，以及环回透传、连接预算、单IP截止封门控、SOCKS密码身份/拒绝NOAUTH、迟到结果拒绝和清理失败记录。协作会话独立静态复核定位了截止延迟、迟到误绿、端口身份和清理异常路径，本会话落实修复；未启动Xray、未构建、未访问公网或运行现场分类。
- 协作会话仅修改 `rx_integration_test.go`：net.Pipe模拟服务端先读SETTINGS ACK及HEADERS再写GOAWAY/REFUSED_STREAM，消除双向同步写互堵；原1秒截止、禁止重拨及第二请求断言保留，并核验实际H2错误。没有修改公共transport。
- 协作会话报告在Go1.27.3、CGO_ENABLED=1、GOPROXY=off、GOTOOLCHAIN=local下执行 `go -C Xray-core test -race ./transport/internet/splithttp -run '^TestRXSegmentH2NoImplicitRetry$' -count=20 -timeout 60s`，通过1.110秒，两个分支共40次。其gofmt与限定diff检查通过。本会话已读差异，未重跑该测试或全包检查。

## 授权分类现场及续段首刷新（2026-10-30）

- 用户明确授权34个既有IP有界分类，已执行34次应用GET、34次出口connect，整轮143.181044秒。12个完整1MiB/hash/HTTP200/identity通过，其中11个≥1MB/s；13个TCP建立但0应用字节，9个未观察SYN-ACK。现场与口径见 `Xray-core/docs/xhttp-classify-field-20261030.md`，不外推直拨、永久分类或1GiB目标。
- 原始抓包统计52个SYN包、34首SYN、18同代际重传，25代际有SYN-ACK。tcpdump captured9621、filter9624、kernel dropped0；snaplen128/6008记录截断，不能称无遗漏或无线上丢包。另一会话独立核对逐IP结果、hash参考及聚合算术，没有重复现场。
- 34份客户端清理均成功，英国新临时服务inactive/MainPID0，实验端口空，生产监听及PID未变。没有重启到期旧unit或扫描新地址。
- `rx_server.go`仅对整段已就绪的RX续段跳过header-only Flush；首open、未分段RX、未就绪续段保持立即Flush。新增一个表测试覆盖首Flush前字节数、ready/pending resume和parallel及未分段RX；Go1.27.3、CGO_ENABLED=1执行 `go test -race ./transport/internet/splithttp -run '^(TestRXServerSegmentFirstFlush|TestRXIntegrationSegments|TestRXParallelTLS)$' -count=1 -timeout 60s`，通过1.461秒。协作会话只读复核差异，无公共认证/ACK/段边界改变；不宣称实际TLS分包或速度收益已证实。
- 独立Linux实验内核构建通过。SSH短暂阻塞恢复后，已按授权部署新独立实验服务并执行13失败候选H2/H1各一次64KiB：H2为0字节/12.271807秒/TLS ready0；H1为1139字节/7.541951秒/44TLS ready（43恢复）后终身故障预算耗尽。两档失败且清理成功，生产未变。详情见 `Xray-core/docs/xhttp-rx-failedonly-field-20261030.md`；省去空Flush无已证明现场收益，保留原行为的独立对照产物仅本地构建，尚未部署。
- 协作会话独立解析本轮pcap：H2全部20条连接累计ACK停在0，已见后段的代表连接带SACK[1448,2772)；H1代表连接累计ACK停218、SACK[2059,3066)。支持TCP连续前缀缺失与用户态读取停滞相容，未确定缺失位置或机制。全IP墙钟秒首SYN峰值H2为2、H1为9；本会话未重复解析pcap或启动新现场。完整边界见对应现场报告。

## 实际网络与边界

最初发布快照整理未启动远端服务或执行现场下载。后续单独授权的分类及RX直拨实验已执行，结果见上方记录；未重建AAR或替换生产。最新两档连64KiB都未完整交付，完整1GiB与正常速度目标保持未验收。

协作会话另报告多地址实验4MiB完整hash一致约35.7秒；更高并发大样本在约62.5秒交付9.6MB后耗尽4096次终身重试预算而失败。上述是协作报告，本发布会话未独立核对全部原始证据，不能当作持续速度达标或完整1GiB验收。

## RX稳定性和内存（2026-10-31 UTC）

本轮按用户要求处理稳定性、缓存占用和连接选择，详见 [完整报告](Xray-core/docs/xhttp-rx-stability-20261031.md)。上方旧现场结论对应其各自配置；本轮使用HTTP/80、VLESS正文认证加密和RX v2控制认证加密路径。

- 修复控制探测失败拖住已知卡段恢复、取消拨号等待和迟到连接释放；回放缓存按需增长、全部ACK后释放，预取队列限为两批；共享入口健康记录并增加短暂冷却；修复Chrome指纹覆盖H1/H2协议选择。未改变协议、认证、默认配置或依赖。
- Go1.27.3执行相关RX/TLS/ECH/ALPN定向race，通过192个测试及子测试；sing-box的 `go test ./protocol/xray -count=1 -timeout 120s` 通过。Linux amd64独立内核构建通过，源码哈希与实验产物可核对；不是全仓测试或Android实机验收。
- 同一新版内核、同一段长64KiB/并发16的16MiB、1MiB、2MiB窗口均完整收到64MiB且SHA-256一致。UK进程峰值RSS分别73.50、39.57、43.31MiB；1MiB窗口有明显等待，当前场景优先2MiB。该差值为窗口配置比较，不是新旧代码A/B；客户端RSS没有下降。
- 2MiB窗口持续183.323秒收到181.320MiB，源前缀、落盘前缀和流式hash一致，分段重试0；UK代理峰值40.98MiB。按预设截止结束，curl28，未完整下载1GiB。
- 最终两端独立审计均确认实验服务、进程、句柄及监听清理完成，生产监听与PID不变；未替换生产、未构建AAR、未提交或推送。OOB原型尚未合入应用，连接成功率提升百分比、跨运营商、多用户与长期稳定性未验收。

## 原生OOB接入及新连接验证（2026-10-31 UTC）

接续上轮RX工作，OOB现已接入Xray源码，并补充sing-box保护拨号的TCP control桥接；配置默认关闭。实现、配置和完整证据边界见 [OOB报告](Xray-core/docs/xhttp-oob-20261031.md)。

- 主路径为TLS普通XHTTP，不开RX回放缓存；等待有效HTTP200再交给业务，候选有界重试、失败冷却，实际下行达到32KiB才记为优选；启动失败可退回显式配置的RX。中途断流不迁移或重放已开始的普通业务。
- 最终组合源码的RX/OOB/TLS/ECH/ALPN定向race通过205个测试及子测试，sing-box的dialer和Xray出站测试通过；Android arm64相关源码包交叉编译通过。不是全仓测试、AAR或手机实机验收。
- 俄罗斯→CF TLS/2083→英国，六次独立冷启动均完整收到1MiB且hash一致，启动加下载2.230～2.990秒；每档仅一条TCP、一个OOB紧急包，SYN MSS1440，客户端UID65534。全部首候选H2成功，未在现场触发备用或证明13个池地址逐一有效，不能外推全网成功率。
- 同一内核的183.303秒持续传输收到87.180MiB，源前缀、落盘及流式hash一致；全程一条TCP，客户端峰值RSS37.78MiB、英国代理35.84MiB。按预设截止退出curl28，未完整下载1GiB；不是与RX的内存A/B。
- 冻结源码/实验内核hash可在报告中核对，当前Go/模块/proto源码与现场清单一致。两端清理审计通过、生产PID及监听不变。现场实验阶段未替换生产、未打AAR、未提交或推送。

## 按用户指令提交前核对（2026-10-31）

用户手动调用submit-push，授权分类提交并推送当前改动。RX修复与OOB接入分别提交，使用说明及交接记录单独整理。

- 分组暂存的RX代码与此前通过192个定向race测试及子测试的冻结源码一致；最终Xray的Go、模块和proto源码与通过205个定向race测试及子测试、完成OOB现场验证的冻结清单一致。本次复用这些有效结果，未重复启动远端实验。
- 本次补跑 `go test ./common/dialer ./protocol/xray -count=1 -timeout 120s` 通过，分别为0.080秒、0.347秒。修改文件的Go格式、diff空白、文档链接及已知私有值检查通过。
- 忽略的artifacts、服务器认证配置、抓包及实验二进制没有纳入提交。本次发布源码和脱敏文档，未替换生产、构建AAR或增加手机实机验收结论。

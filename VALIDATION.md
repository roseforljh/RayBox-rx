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
- 在新的组合源码目录执行标准 `protocol/session`、`protocol/streamconn` 定向回环测试；历史 `protocol/xray` 测试记录仅保留为迁移前基线，不代表当前生产入口。
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
- Go1.27.3执行相关RX/TLS/ECH/ALPN定向race，通过192个测试及子测试；迁移前 sing-box 的 `go test ./protocol/xray -count=1 -timeout 120s` 通过。Linux amd64独立内核构建通过，源码哈希与实验产物可核对；不是全仓测试或Android实机验收。
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
- 本次补跑 `go test ./common/dialer ./protocol/session ./protocol/streamconn -count=1 -timeout 120s` 通过；修改文件的Go格式、diff空白、文档链接及已知私有值检查通过。
- 忽略的artifacts、服务器认证配置、抓包及实验二进制没有纳入提交。本次发布源码和脱敏文档，未替换生产、构建AAR或增加手机实机验收结论。

## OOB速度质疑后的现场对照（2026-10-31 UTC）

- 按原始配置修正上轮OOB报告：六次冷启动和87.180MiB长测实际为Go默认TLS/H2，并非Chrome；证书验证开启。长测真实平均0.498708MB/s，低于此前约1MB/s的RX，历史不同时间、不同握手的结果不能充当原生接入的新旧A/B。
- 同一固定入口、16MiB业务请求：原生OOB、旧辅助OOB、新内核配旧辅助OOB均0业务字节；RX完整校验通过，单IP0.517559MB/s，原13入口池0.602548MB/s。更换OOB位置/既有入口后新旧仍失败。关闭OOB、使用Go/H2的新旧内核也都0业务字节，改H1未恢复。
- 固定MSS1440直接访问CF小诊断页，Go/H1-H2证书验证和200可以成功；Chrome及Go加OOB组合在该轮诊断中TLS超时。继续在同一Go连接上请求CF诊断页，H2仅1次完整200、H1仅3次后等待至45秒截止；未达到32KiB累计正文。这在不依赖英国代理的俄罗斯至CF路径复现后续请求冻结，不证明固定16KB阈值或具体责任方。
- 历史抓包两档接收窗口中位数均约2MiB、零窗口0、RTT约107～109ms；原生长测CPU很低，没有内存窗口或CPU耗尽的支持证据。接收侧没有重复数据段不等于网络全程无丢包。没有可用OOB的同一时段测速A/B，性能差的单一根因仍未定位。
- Paramiko密钥交换失败不等于英国服务器故障；Windows OpenSSH用同一凭据成功，随后用于完成普通Go业务实验。逐档清理审计通过、生产PID与监听保持；审计不将已退出进程遗留的TCP状态算作归零。
- 产品代码、生产和AAR未变；新增脱敏 [对照报告](Xray-core/docs/xhttp-oob-compare-20261031.md)，更正 [OOB报告](Xray-core/docs/xhttp-oob-20261031.md)。原始证据继续由Git忽略，未提交或推送本轮文档。

## OOB故障定位及受控源端口测速（2026-10-31 UTC）

- 补充 [故障定位报告](Xray-core/docs/xhttp-oob-rootcause-20261031.md)，更新上节未有成功同条件OOB测速的状态。5组原型共35个计划TCP拨号档位，逐一核对源端口、SYN代际、抓包及业务结果；10个完整512KiB结果均核对源hash。另有4档自动源端口新旧实际代理失败，未计成功测速。
- 英国与俄罗斯同步抓包：两次普通CF请求均仅交付5476字节正文；源站各发送528779字节TCP载荷，CF累计ACK至最终相对边界528781。MSS1440与800普通连接均在第10个下行包后停；成功OOB分别350/581个下行包，完整512KiB/hash一致。
- 冷TLS交叉实验复现同四元组重连状态差异；记录独立SYN和完整非恢复握手。成功组合关闭OOB仍通、另一普通组合冻结后加OOB未建TCP；不能据此确定保存状态的设备或称OOB创造了永久放行，也不能把TLS恢复当作唯一原因。
- 私有Go overlay增加目标专用源端口/关闭方式控制，Linux amd64构建通过，项目源码未改。固定同一源端口、CF入口、Go/H2及MSS1440的四档实际代理完整4MiB/HTTP200/identity/curl0/hash一致，均单TCP、单URG、289字节ClientHello。原生0.425894/0.566277MB/s，旧辅助0.534745/0.535067MB/s；不宣称固定性能百分比或全网成功率。第四档在管理SSH切换持久会话后补跑，时间间隔限制已披露。
- 原始plan、认证资料、TLS secrets、抓包与实验构建均留在忽略的artifacts。新增前缀的两端清理及生产监听最终审计另存；未替换生产、改CF/DNS、打AAR、提交或推送。本轮产品代码无变动，未重复运行此前源码race或全仓测试。
- 最终独立审计覆盖rootcause、matched、matched-h1和bound全部新目录/服务前缀，两端活动实验unit、所属进程及句柄为0，实验端口空闲，生产监听及PID保持；私有plan/TLS keylog权限已收紧。不宣称全部外连TCP内核状态归零。
- 本地证据复核通过35个原型档位、10个完整512KiB hash、4个完整4MiB hash、同源端口/SYN/MSS/URG/ClientHello一致性和双端累计ACK核对；5份报告的数字、链接、已知私有值排除及diff空白检查通过。早前15条业务对照结果的原检查也通过。

## 持续传输补传复核（2026-10-31）

- 两份历史长测pcap下载后核对远端SHA-256；按到达顺序、序列号、SACK及CF发送时间戳分析，旧/新样本迟到补缺条目分别9344/6590，占6.966%/10.322%，其中9344/6589具有较新的发送时间戳。不能将该比例直接称为精确链路丢包率，也不能凭它量化全部速度减半幅度。
- 独立Scapy解码核对两份抓包的数据条目数、TCP载荷字节、迟到补缺条目/字节、SACK数及重复段数，全部一致。正常顺序、原包乱序、补发、连续缺段补发四个构造抓包自检通过；修正了初算漏计连续补缺的情况。
- 实际代理配置128MiB源文件、125秒下载截止，准备采集HTTP/2调试帧和两端TCP状态。7个现场档位均在正文传输前失败，无新增有效长测A/B、无提速修复。原生/旧辅助、Go/Chrome及原池另外8个地址均包含在失败证据内，不将空前缀hash相同算作业务验证。
- 四个新实验目录分别完成双端最终审计：活动实验服务、进程、句柄为0，实验端口空闲，生产监听及PID保持；临时配置及日志等权限收紧为root/0600。第一组runner存在审计结果打印键错误，已修正，保存的实际审计及最后独立复核均通过。
- 更新[持续传输证据与边界](Xray-core/docs/xhttp-oob-rootcause-20261031.md#追加持续传输中的tcp缺口与补传)、对照报告及交接记录。产品源码、生产和AAR未改，未提交推送。没有CPU或TCP接收窗口耗尽证据，但HTTP/2运行时流控尚未完全排除，速度目标仍未达成。

## 后续HTTP长测及路径失效复核（2026-10-31 UTC）

- 独立HTTP/80的MSS1440/800/1440各125秒、单/双/单各120秒，所有收到的非零前缀hash一致；源端TCP_INFO与客户端进度对应，支持慢样本源站供给充足。双并发未增加吞吐。结果和观察边界见故障定位报告后续长测段。
- 实际代理普通XHTTP、RX单入口、RX原13池、普通XHTTP后测均125秒，非零前缀hash一致；800ms私有overlay的相关RXParallel测试通过，但现场ABBA遭路径失效，无有效参数优化结论，默认400ms未改。
- 本地真实TCP/TLS/H2/XHTTP的Go/Chrome、普通/OOB四档各50MiB/hash一致，未发现本地固定0.5MB/s上限，不代替公网性能验收。
- 新增10个目录统一双端清理审计通过；无新增源码性能修复，未改CF/DNS或远端生产。CF事件查询未完成，不宣称已确认CF拦截。

## Xray v26.11.9 选择性修复验证（2026-11-10）

- 新增 6 项回归测试：超时后返回明确错误且保留迟到数据、MUX 客户端/服务端共享写入串行化与关闭/中断转发、带/不带 tag 的动态入站失败清理（含 Close 失败）、MASQUE 默认私网保护及显式允许覆盖、WireGuard 端点解析截止时间、解析成功/失败/取消及字面 IP。Go 1.27.3 普通测试与 5 包定向 `-race` 均通过。
- `with_utls` 下 5 包定向集成通过：原生/session REALITY 的 X25519MLKEM768 与 X25519 握手、标准 VLESS 验证 TLS/证书重载/上下文、VMess/Trojan XHTTP、RX TLS 并行分段、XMUX TLS 并发档位、gRPC 配置及 User-Agent。
- 新增 gRPC single/multi 两模式真实 TCP 回环，每种 12 KiB 回显字节一致；最初测试回调类型不匹配编译失败，修正为 `stat.Connection` 后通过。既有 MUX EOF、关闭、UDP 来源及出站泄漏共 4 项回归通过。
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` 的 `cmd/raybox-server` 构建通过；`git diff --check` 通过。仅本地回环与构建，不作为俄罗斯线路、CF 受限入口提速或 Android 实机验收。

### HK 真实节点验收（2026-11-10 工作批次）

- 按用户授权构建并上传独立 Linux amd64 完整内核及协议服务端，版本标记 `0.0.1-hk-candidate`。大小分别为 61,522,080 / 34,943,136 字节，SHA-256 分别为 `70dd74d7fea79b83b700a54a14404d3b515f65c6efd18ab3a71365b919a31a3b` / `ff76de81c783d8f7e55a83182e518c33831d7c757b5267baebecde90f0c3991a`；上传后逐文件核对。测试使用新端口及独立证书，没有覆盖原有节点。批次名沿用本地日期，HK 主机日志日期为 2026-11-11。
- 最新源码构建 Windows 公网探针（含真实 Cronet Naive 客户端），测试候选节点 27 个正常变体各 2 轮，既有节点 16 个正常变体各 2 轮，共 86 轮。每轮完整 TCP 回显 360,448 字节，累计 30,998,528 字节，逐字节及 SHA-256 一致；每轮通过节点访问 HTTPS，正常验证公网证书并核对出口 IP 为 HK。关闭检查均通过。候选及既有节点另各 1 个错误主机名负对照，均拒绝连接；最终共 45 个用例、88 轮符合预期。
- 覆盖 VLESS Vision/REALITY（原生及协议服务端）、VLESS Encryption、XHTTP H1/H2/H3、GET Header、packet-down keepalive、WebSocket/early-data、gRPC、TUIC 两种 UDP 模式、AnyTLS/TLS/REALITY、SS2022 三种算法、VMess TCP/WS、Trojan TLS、Hysteria2、Naive、SOCKS。标准 RayBox gRPC 出站只提供单流模式；另用协议内核的 gRPC multi 出站完成 MUX 检查，没有把配置里的服务端 multi 标志当成客户端 multi 已验证。
- 支持 UDP 的用例核对双目标回显、来源地址及 DNS 回复；Naive 为 TCP-only。VMess 的普通 UDP 为固定目标模式，多目标测试改为显式 XUDP 后通过。SS/SOCKS 跨公网 4,096 字节 UDP 首轮超时/超过本机消息大小限制，1/1,200 字节及 DNS 通过；同一最新源码的 Linux 探针在 HK 本机对四个节点执行 8 轮，含 4,096 字节均通过，公网大包限制保留，不记作已解决。旧节点首轮 3 个用例有 HTTPS 超时/EOF，定向复核各 2 轮通过，初轮失败证据保留。
- MUX 在 HK 实际 VMess TCP 与 VLESS/gRPC multi 链路各运行 32 条流，每条 1 MiB，分别耗时 1.091 / 0.407 秒，共 64 MiB，SHA-256 全部一致。XHTTP TCP 套 MUX 初测失败，服务端明确报 `unexpected network TCP`，核对为现有 `proxyman/inbound` 对 splithttp/masque 只允许 UDP MUX 的限制；没有修改该限制，也没有把该组合计为通过。
- 动态入站 API 真实占用端口触发 Start 失败，确认失败 tag 已清除；释放端口后同 tag 注册、SOCKS 握手、移除及端口释放全部通过。REALITY 本地静默目标触发含空 SNI 的 6 个探测连接，10.011 秒全部关闭、6 个缓存状态均完成；初版探针存在 EOF 与缓存 defer 更新竞态，修正探针等待后通过，未改产品代码。
- HK Linux 上执行新超时读取、WireGuard DNS 截止/成功/失败/取消、MASQUE 私网默认阻断/显式允许共 4 项定向测试通过。它们是内部故障触发验证，不宣称实际建立了 WireGuard/MASQUE 公网节点。此前本地定向 race 结果保留。
- 收尾停止本批独立服务，候选进程为 0、测试端口全部释放；原 6 个服务 PID/状态、2 份配置 SHA-256 和原监听逐项不变。构建源码清单逐文件保持一致。配置、认证、原始日志及完整结果仅存 Git 忽略的 `artifacts/hk-upstream-20261110/`。此轮验证直连 HK，不替代 CF 受限入口、俄罗斯线路、Android 或长时间稳定性验收。


## Actions 上传竞态及版本发布门禁修复（2026-11-10）

- 最新 main Actions 中，Linux 在 `TestRXAuthUploadAndParallelStream/header` 最后一包上传重试至 10 秒截止，后续三架构编译被跳过；Windows 与 Android 编译、产物上传成功，发布因 `*-v0.0.1` 已绑定旧提交失败。Linux 的 `server-v0.0.1` 同样已占用，不能只修测试后重发旧版本。
- 修复公共上传队列：发送到队列成功即确认接受，避免消费者读完最后一包立即关闭后被误报为失败。新增确定性关闭时序回归，旧实现直接失败，修复后通过；不放宽超时、不跳过测试。Linux 重复 20 次覆盖 60 个上传 placement 子测试及 20 次关闭竞态；Windows 相关定向 race 重复 10 次通过（4.838 秒）。原 Actions 三包测试通过（splithttp 5.746 秒、tls 0.261 秒、conf 0.055 秒）；Linux splithttp 原测试组通过，amd64 服务端编译并在 WSL 启动版本命令通过。
- 产品 `VERSION` 升为 `0.0.2`，`go run ./cmd/internal/read_tag` 输出一致。三端 main push 始终编译并上传 artifact，仅该次 push 前后 VERSION 内容变化时自动发布；手动默认只构建，显式 `publish=true` 才发布。共用标准库门禁脚本，旧标签绑定、资产哈希与不可变校验保留，不覆盖旧版本。
- `python -I tools/test_release_workflows.py` 通过 3 个测试方法（24.301 秒），包含 16 个事件场景、三套 YAML 接线及原三端共 21 个发布回放场景；覆盖相同版本、版本改变、多提交、改后改回、首次引入、缺历史/版本、手动布尔及字符串开关、旧版本冲突和草稿恢复。README 与发布规则的旧 SHA 版本说明同步修正。
- 当前结论来自本地定向验证；修改后的远端 Actions 尚待提交后执行，未把本地 Linux amd64 运行当作远端 Linux 三架构构建完成。

## 三平台统一版本发布页（2026-11-10）

- 经 GitHub API 核对，合并提交 `a65a4b5b49074c4de1b6271d9f0c1626ff150005` 的 Android、Windows、Linux 三套 Actions 均成功，`0.0.2` 三个平台发布均已存在；上一节“远端待执行”属于修复提交前状态。本轮处理同版本三个发布页的问题，不是新的内核编译故障。
- 新增统一 `release.yml`，并行调用三端只构建子流程。Linux 三架构 metadata/ELF/工具链/来源组装校验保留为独立 assemble；所有平台成功且版本门禁允许后，由单一发布器创建 `RayBox v<VERSION>`，标签 `v<VERSION>`。main 每次构建、仅 VERSION 内容变化自动发布及手动默认不发布规则保留。
- 发布页集中二进制，平台 manifest、来源及许可证按平台命名避免覆盖，另提供总 manifest 记录全部附件大小/hash。保留旧标签、下载链接、同 SHA 重跑、草稿补传、来源校验和不覆盖规则；平台 manifest 的 builtAt 变化按语义校验，总 manifest 的 hash 对应真正保留/上传的元数据文件。
- 本地假 gh 回放已通过：单一发布页、同 SHA/元数据时间变化重跑、两种部分草稿、旧版本冲突、陈旧附件、已发布缺件、API 500、错误草稿提交、三平台缺 manifest/二进制/身份错误和重复 Release；16 个触发事件与工作流依赖/权限/附件映射检查保留。Android 检查器 5 项测试通过；修改 PowerShell/Python 语法及 diff 空白检查通过。
- 本轮没有重建 AAR 或内核，没有在线迁移旧发布；新汇总流程的远端执行需代码提交后验证。现有部署脚本保留已固定的旧下载链接与真实 hash。

## 最新内核构建及两端项目接入（2026-10-31 UTC）

用户要求提交当前代码、编译最新内核并放入服务器与客户端项目。此次发布源码基于提交`b765f79e8ade9313c7211c9d10afacf6bfc2b074`；后续记录提交仅修改文档。RX/OOB默认关闭，未修改既有节点配置或远端生产进程。

- 修复`sing-box/export-simplenet.ps1`：从当前组合仓库取构建源码，原始仓库只提供基线对象；临时克隆隔离索引与对象。补齐sing-box受保护拨号的`common/dialer/default.go`和`option/outbound.go`，排除更新部分的测试、文档、现场数据。PowerShell语法检查及实际导出通过。
- 客户端两组补丁在干净上游源码上逐一`git apply --check --whitespace=error`并应用通过。与当前仓库逐文件核对，Xray 897项、sing-box 1078项构建源码一致，仅调整客户端私有Xray副本的模块替换路径。
- Go1.27.3构建Linux amd64/arm64/armv7，ELF架构检查及三个资产的大小/SHA-256读取校验通过。amd64在WSL实际启动版本命令，并通过含OOB和RX备用的配置自检。服务端项目的版本锁和构建脚本已固定对应提交，三个二进制放入项目现有资产目录。
- JDK17、NDK28.0.13004108与gomobile0.1.13构建标准API24及兼容API21两套AAR，每套均包含arm64-v8a、armeabi-v7a、x86、x86_64；ZIP完整性、ELF架构和输出SHA-256核对通过。标准AAR SHA-256为`0333400c60de3957d113ce537e46aa88c017696e375076a8085ef10a4651615c`，兼容AAR为`bbffb1ff3839a66f6c787905d0ecbf1cc0819f05c8c706f5f89254739eab2486`。
- 两套AAR已复制到SimpleNet现有`third_party/sing-box-build/`，同步来源锁、补丁、构建指纹及SHA-256。`:app:verifyNativeCore :app:compileDebugKotlin --offline`通过，未做手机实机联网验收。
- 标准 RayBox session 入口与 `protocol/vless` 的真实 XHTTP/TLS/UDP/reload 回环已验证；旧 `protocol/xray` 路径仅作为迁移前文档基线保留。
- 客户端和服务端的86个既有无关改动文件逐项hash保持，不纳入本次提交。二进制、AAR、配置样本、构建日志及可复核记录放在忽略的产物目录，没有提交凭据或实验资料。

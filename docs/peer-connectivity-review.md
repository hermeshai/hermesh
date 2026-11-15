# Peer connectivity 审查与测试报告

日期：2026-11-12。审查提交：`54b405f6627345d8069b624ce7b8a551bc01795f`。

范围：[测试计划](peer-connectivity-tests.md) §2 自动化、连接实现审查、后续修复，以及当前 Mac 和真实 iPhone Duo 的实验。§2–§5 保留首轮审查记录；最新结果见 §0，真机尚未验收。

### 最新安装：Claude 优化版 peerfix9 / iPhone 1119

本轮依据用户“看看 Claude 的检查优化，再安装手机测试”的要求执行。新版源码不在主工作区，而在 `.claude/worktrees/unruffled-kirch-cd5855`，基于 `f118f6ebc4e8176d1346fdcc61adc9d827f41134`，含未提交改动。固定快照后构建，没有覆盖 Claude 的工作区或把其改动合入主工作区。用户明确 ICE-TCP 先不做，本轮未增加该能力。

- 安装成功：Mac `0.1.5-dev.f118f6eb-peerfix9`（Local API 确认在线），Duo `1119`（设备查询确认并启动）。成品中已核验新版 `gathering.grown`、`stunRoundTrip`、`relayCannotReach` 符号，源码快照校验无变化。
- 自动化：screen race 74.833 s、relay race 11.387 s、Pion relay STUN race 2.450 s 均通过；屏幕 E2E 五项通过，36.527 s，普通首帧 551 ms、不可达 TURN 首帧 572 ms、换网后 250 ms 收到 30 帧、中继转直连 7.55 s（含人为阻断）。真实屏幕时延和下载页截图用例按环境条件跳过。
- UI 验证：第一轮因独立快照没有 Her 测试依赖而跳过，不能计为通过；补齐依赖后在模拟器键盘输入处发生 XCTest 事件合成超时，不能计为通过。记录的 XCTest 墙上时间 4669.557 s 与 Go 耗时 301.452 s 差异很大，原因未确认；保持测试进程唤醒后重跑通过：xcresult 核对 1 pass / 0 fail / 0 skip，XCTest 118.535 s、Go 253.501 s；没有修改产品代码、测试断言或跳过步骤。
- 真机：用户保持原共同 Wi-Fi、两端 Surge 开启，反馈仍中继，400–500 ms。`7d6aa990eec95281` 在 4.211 s 选中中继、5.140 s connected、5.786 s input ready；IPv4 同网检查 48 次、两组 IPv6 46 / 47 次，均 0 响应。`5c1fa1cba180e1d7` 在 1.996 s 选中中继、3.479 s connected、4.167 s input ready。两次均经过 `43.110.26.73`，没有直连证据；不据此认定 AP 隔离。
- 审查保留项：候选交换现会继续调用直至完成/期限，修补先前固定第二批遗漏；仍不是事件流。无效私网目的地址的中继探测被跳过，但其他地址对仍每秒探测，不能称自适应降频。`nearest()` 会按 UDP STUN 响应只保留一个 TURN，丢弃其他节点及 TLS-only 备选，多节点上线前应补充 allocation/媒体失败的回退。现网只有单个 TURN，本轮测试不触发该问题。
- 云端未部署：`pkg/relay` 的长期会话凭据处理只在本地测试，手机/Mac 安装不能证明线上 70 分钟会话已修复。直连与高延迟问题仍未验收。

构建、测试和可回退产物在 `/var/folders/rr/ycxylhln6_j0mq4q79h3chsc0000gn/T/hermesh-claude-device-p6_7mb0c`：`source-manifest.json`、`installed-source.patch`、`review-results.json`、各测试日志及 xcresult；`hermeshd-before-peerfix9` 保留更新前 daemon。以下 §0–§5 的版本和未修复状态是前一轮 peerfix8 的历史记录，不能与本轮源码混用。

### 后续根因核对：新 Wi-Fi 出现物理直连

Claude 最新提交 `b540d7ca875a53dd5abb2bba148dbc8c8db25061` 将多节点选择改为实际 TURN allocation 测量，并在 5 s 内没有 relay candidate 时使缓存失效、重新获取。`TestNearestTURNServer`、`TestServersDoubted`、`TestGatheringDoubtsServersThatGiveNoRelay` 的定向 race 测试通过（7.625 s）。它修的是中继选择/恢复，不改变同网 host UDP 路径；当前安装仍是 peerfix9 / 1119，未安装该最新提交。allocation 成功也不等于完整 relay 数据通路经过验证。

根因证据需进一步收紧：Pion `agent.sendBindingRequest` 先执行 `pair.UpdateRequestSent()`，再调用 `sendSTUN()` 写 socket。因此此前“48 次请求、0 回应”说明 ICE 尝试检查但未收到有效回应，不能证明 48 个包实际离开物理网卡，更不能直接认定 AP 隔离。当前 Claude 设计文档关于“该网络隔离设备”的确定表述证据不足。旧网络精确阻断点仍需该网络下的 socket 写结果与两端限定包迹对应。

用户确认换到另一 Wi-Fi 后尚未测试，随后按同版客户端、两端 Surge 开启、USB 拔线做对照。Mac 当前 en0 是 `192.168.77.95/20`，手机候选为 `192.168.66.35`。会话 `a9ca143798ded53b` 在 566 ms 选中这两个物理地址的 host UDP 对，601 ms connected、617 ms input ready；路由核对手机地址走 en0，Surge 规则推演为 LAN DIRECT。这是当前网络的真实直连证据，不是 TURN 或 Xcode 地址。但此前同一轮还有 `9ce49217d811e83a`、`5f2495d5941b3854`、`fd3e262cd8258d01` 等短会话后关闭，所以 566 ms 仅是最终成功会话的耗时，不能说用户第一次打开就秒连。用户随后确认新 Wi-Fi 能直连，手机日志确认该次 RTT 16 ms，后续两次为 22 / 25 ms；启动时间与稳态网络 RTT 应分开，详见下一节。

对照说明同一客户端具有可用的局域网 UDP 直连能力，故障与网络/路由环境或建连时序有关。旧 `30.235.x.x` 地址曾在 Surge 规则推演中落入代理，新 `192.168.x.x` 地址落入 DIRECT，这是重要差异，但规则推演不能证明实包经过该策略。两个网络不是同一个受控条件，不能据此独断“只有 Surge”或“只有 AP 隔离”。

### 新 Wi-Fi 与 5G：手机日志对照及弱网复现

用户反馈新 Wi-Fi 可以直连但启动约半秒，切 5G 后难连接、无画面或严重卡顿。按已有授权，在用户接线并解锁后只复制 Hermesh 的 `Library/Application Support/Hermesh/logs/core.log`，核对上述会话；没有发起新的手机连接，没有改变网络/代理配置，也没有更新安装版本。USB 只用于读取历史日志，不作为这次媒体路径的证据。以下时间为 2026-11-12 北京时间；手机原日志为 UTC。

| 阶段 | 手机证据 | Mac 证据 | 能得出的结论 |
|---|---|---|---|
| 新 Wi-Fi 第一次尝试 | 23:11:23 开始，连续三次 Connect 各 6 s 超时；第二次打开后又有一次 6 s 超时，随后成功，RTT 16 ms | 最后成功会话选中物理 host→host | 局域网媒体路径可用，但初始信令仍会卡住；最终成功会话的 566 ms 不能覆盖此前等待 |
| 新 Wi-Fi 再次打开 | 23:15:48 / 23:16:00 两次 Connect 分别 494 / 496 ms；connected 时 RTT 22 / 25 ms | 路径选中 518 / 565 ms，首个编码帧 520 / 512 ms，input ready 569 / 603 ms（均自 host 会话开始） | 半秒主要涉及协商和启动采集；不是稳态局域网 RTT 为 500 ms，也不是端到端首帧呈现的完整测量 |
| 5G 初次打开 | 23:17:09 开始，连续三次 Connect 各 6 s 超时；再次打开后还超时一次 | 这些尝试收到 offer 并很快生成回答，只有初始 host 候选，尚未进入完整的候选交换 | 不能把这四次失败一概归因于 NAT 打洞，信令尚未走完 |
| 5G 后续连接 | 23:17:39 / 23:18:38 connected，path=relay，RTT 562 / 575 ms | `ac3045809a433a6e` / `80a330220aa66c54` 经过 `43.110.26.73`，首个编码帧 499 / 512 ms；PC connected 3.300 / 3.874 s、input ready 4.420 / 6.000 s | 确实走现有硅谷中继，网络往返延迟已超过半秒；Mac 能采集编码不代表手机能呈现 |

**信令为何仍慢。** 当前 iPhone 的 `ScreenService.Connect` 走既有云端 E2E 隧道，后续媒体可以改走局域网；这两条路径不是同一条。局域网健康两次的 Connect 仍需约 0.5 s。对于超时尝试，Mac 的会话日志和仅筛选 `kind=screen` 的本地审计记录显示回答很早就准备好，未命中每 100 条记录的 checkpoint。现有证据把等待缩小到回答交付/隧道调用链，但没有 HTTP/2 响应写出与手机首字节时间，不能进一步断言一定是 Surge、云端或某个 transport 锁。当前 6 s × 3 重试只是有限恢复，不是该停顿的根因修复。

**5G 为什么没有直连。** 完成候选交换的两次会话中，手机有公网 IPv6 host 候选，Mac 提供的 IPv6 host 候选均不是公网地址；没有可见的两端公网 IPv6 候选路径。IPv4 上两端均收到了 srflx 和 relay 候选，但没有观察到成功的直连路径，最终选中 TURN。仅凭这些记录不能确定 NAT 映射/过滤行为、UDP 的具体阻断点，不能声称“5G 天生不能直连”。这次是退出 Wi-Fi 会话后重新打开的 5G 会话，日志没有对应的 `viewer network changed`；不是一条已证实的会话内 ICE restart 故障。

**发现并复现的接收端风险。** `internal/screen/assemble.go` 把丢包等待固定为 250 ms，`Viewer.receive` 每 100 ms 检查一次。一旦超时，会清除 `started` 状态，只等待新的关键帧，迟到的原帧中间分片不再用于补齐。在与安装版完全相同的源码上，通过 Go overlay 增加隔离复现（不改变产品源码）：首个关键帧缺一个分片，随后有一个依赖帧。

| 对照 | 补回缺失分片 | 完成帧数 |
|---|---|---|
| 保留实际超时逻辑，模拟 LAN | 25 ms | 2，内容逐字节一致 |
| 不执行超时检查的隔离对照 | 575 ms | 2，内容逐字节一致 |
| 保留实际超时逻辑，模拟本次 relay RTT | 575 ms | 0；300 ms 的检查已放弃组帧 |

`go test -race -overlay ../high-rtt-review-overlay.json ./internal/screen -run '^TestReviewRetransmitAtObservedRTT$' -count=1 -v` 实际运行两个通过的对照与一个失败用例，失败暴露上述行为，**不是修复后回归通过**。测试只模拟重传抵达时刻，没有真实执行 NACK、TURN 或 iPhone 解码，不能替代弱网 E2E 或证明本次黑屏由它触发。产物 `high-rtt-review_test.go`、`high-rtt-review-overlay.json`、`high-rtt-review.log` 在上面的 peerfix9 构建目录。第一次 overlay 用非规范化路径时未发现测试，已改为真实绝对路径重跑，并核对输出中三个子测试确实执行。

这也是不能只把 Pion 连接状态当验收结果的原因：媒体接收/组帧和 Swift 解码目前没有首包、首个完整关键帧、首个解码画面的本地事件，无法从现有日志判断黑屏卡在哪一级。发送端另有基于本机延迟测试而关闭 pacer 的选择；在跨洲、有丢包链路上需要复测突发丢包，不能把本机收益直接外推到蜂窝。标准方案本来就要求依据 RTT 和反馈时效选择丢包修复策略，见 [RFC 4585 §3.7.1](https://www.rfc-editor.org/rfc/rfc4585.html#section-3.7.1)；不能简单把 250 ms 换成一个更大的固定值便宣称弱网问题解决。

下一轮落实顺序与验收边界：

1. **P0：信令与首帧诊断。** 对单次屏幕请求记录建隧道、请求写出、host 回答就绪/交付、手机首字节/完成时间；增加手机首个 RTP、完整关键帧和解码呈现事件及丢帧/PLI 数量，只写本地，排除 SDP、密钥和屏幕内容。验证一次重试就能区分卡在信令、媒体传输、组帧还是解码，再针对停顿修复；不继续叠加盲目重试。
2. **P0：高 RTT 丢包恢复。** 在上述可复现用例上设计有上限、适配 RTT 的重传等待与关键帧恢复，避免每次重传尚未回来就弃帧；加入真实媒体链路上 20 / 150 / 600 ms RTT、单包丢失及突发丢失测试，核对画面能恢复、缓冲有界、正常 LAN 不额外等帧。结合真实接收统计再判断 pacing/码率调整，不能以调大常数替代验证。
3. **P1：可用的近端中继。** 现有 562–575 ms 真机数据已充分说明单一硅谷节点不符合当前交互目标；近端 TURN 不应继续等同于“未来可选项”。用户没有现成节点，先准备可审查的部署/成本选择，不购买或部署。最新 allocation 选优只能在已有节点中选择，不能凭空降低单节点跨洲 RTT。需要在相同手机/网络复测中继 RTT、首帧及持续操作。
4. **P1：同网启动路径。** 初始信令仍经云端，若要继续降低同网打开耗时，应把经过现有身份验证的本地信令路径纳入后续可复用 peer 连接设计，同时分段测量采集初始化；两者部分并行，不能把其耗时相加。不是仅继续缩短 ICE 等待。保持 ICE-TCP 暂缓的既定决定。

这一轮读取 Hermesh 历史日志时，尚未拿到 UU 的路径证据。随后按用户要求单独进行 UU 逆向研究：客户端报告 `P to P UDP`，限定包迹也确认了与手机公网地址的双向 UDP 路径，并观察到同一 socket 探测三个 STUN 端点。详见 [UU 连接实现逆向分析](uu-connectivity-analysis.md)。不能继续用“UU 只是中继更近”解释这次对照。上述 Hermesh 历史日志核对没有修复安装，也没有完成 5G 真机验收；当前版本仍为 peerfix9 / 1119。

## 0. 修复与回归

| 项目 | 修复 | 回归结果 |
|---|---|---|
| F1 接口缓存 | 新增 `systemNet`，每次枚举使用独立的当前接口快照；普通屏幕和故障注入共用此行为 | 注入失效缓存并 restart 的回归通过；真实 IP 切换仍待真机 |
| F2 候选代次 | 候选响应的代次检查与整批应用，共用 offer 的串行锁；候选 RPC 不持锁，避免阻塞重连；处理候选应用错误 | 锁竞争回归、旧响应晚于 restart 返回且画面继续的集成测试通过 |
| 旧机器兼容慢连 | 只有完整收集确实增加了候选，才发起兼容性 restart；候选相同时保留正在建立的路径 | 五次 `-race` 复测均通过，连接 3.067/3.111/4.338/3.384/3.283 s |
| F3 剪贴板往返 | 测试种子使用本地 UTF-8 字符串并核对写入；接收端显式写入本机剪贴板，避免 Universal Clipboard 再次跨设备传送 | 完整 UI 连续两次通过，Go 266.808 / 253.517 s；包括双向剪贴板、键鼠、工具栏、旋转关闭、中继转直连；两轮均 1 pass / 0 fail / 0 skip |
| F4 iOS 核心构建依赖 | Go 核心脚本移至应用依赖的 `HermeshCore` 独立目标，并声明 xcframework 产物，避免先处理旧库、再编译新库 | 两次实际 iphoneos 构建通过；第二次修改 Go 日志字符串，不清理 DerivedData，确认同一次构建重新编译核心、处理 xcframework、链接应用，最终二进制包含新字符串；build 1115 已安装 |
| F5 慢 TURN 阻塞直连检查 | 锁定 Pion ICE v4.4.8 的本地源码补丁，只将 relay STUN 写入移出 ICE 事件循环；每个地址对、消息类型至多一项在途，防止重复阻塞写堆积 | 12 个无效候选、TURN 权限响应延迟 150 ms：原版失败，补丁约 5 ms 连上；正式回归在原版 3.96 s 失败，补丁 `-race -count=3` 均通过；异步缓冲所有权、重复抑制、请求/响应独立的依赖回归 `-race -count=10` 通过 |
| F6 Apple socket 网卡归属 | host socket 使用地址所属接口的 `IP_BOUND_IF` / `IPV6_BOUND_IF`；通配 STUN/TURN socket 保留系统路由 | 真实 IPv4/IPv6 UDP 往返及 socket option 核验通过；Linux 交叉编译通过；两端 Surge 开启后的热点首条物理路径成功，见下文 |
| F7 首次回答迟到 | 首次 Connect 每次限时 6 s，暂时不可用或超时最多 3 次；超时隧道重建；已建立会话保留原 restart 策略，权限拒绝不重试 | 丢掉真实 helper 会话第一次回答，自动重新打开并收到画面的集成回归通过；重试分类、上限单测通过；peerfix7 三次用户操作均有画面 |

修复后完整 `go test -race ./internal/screen -count=1` 通过，42.146 s。
屏幕 E2E 五项通过，37.745 s：普通首帧 596 ms、不可达服务器首帧 644 ms、中继转直连 7.423 s、同 IP 换端口后 247 ms 收到 30 帧。

完整普通测试通过，60.891 s（与 UI 构建并行）；Linux amd64 测试二进制交叉编译通过，不代表 Linux 真机验收。
首次通过的屏幕 UI 的 Go 命令耗时 260.517 s，产物 `/tmp/hermesh-peer-fix-ui.ger5U9/iphone.xcresult`，已通过 xcresult summary 核对。当时只改了测试种子；随后发现回传仍不稳定，最终产品修复与复测见下文。没有跳过系统 Paste 按钮或放宽远端断言。

后续回归更新：修复构建依赖后的完整 UI 再次在第一次手机→Mac 粘贴超时；模拟器日志显示 Pasteboard 返回 `public.utf8-plain-text data=0 bytes error=(null)`。测试种子改为本地字符串并核对写入内容后，首次粘贴通过，Mac 文字回传后的再次粘贴仍失败。接收端现以显式 UTF-8 字符串、本机剪贴板写入，避免把已由 Hermesh 传送的内容再经 Universal Clipboard 跨设备同步。完整 UI 随后连续两次通过：Go 266.808 / 253.517 s，首轮 XCTest 118.781 s，两轮均 1 pass / 0 fail / 0 skip；仍点击系统 Paste 并检查原有远端断言。产物为构建目录下 `clipboard-local-only-ui` 与 `clipboard-local-only-ui-repeat`。

peerfix6 全部 screen race 测试通过（43.965 s），五项屏幕 E2E 通过（37.113 s）。peerfix7 加入首次协商恢复后，全部 screen race 测试通过（58.341 s），五项屏幕 E2E 通过（35.901 s）。依赖完整 `go test -race ./...` 在 `TestConnectionStateFailedDeleteAllCandidates` 超时；原始 v4.4.8 在相同环境单独运行也超时（5.738 s），补丁为 5.448 s，尚未获得完整依赖测试通过，不能省略这一限制。

最终源码的 screen race 测试通过（53.487 s），五项屏幕 E2E 通过（45.126 s，与 UI 构建并行）：普通首帧 992 ms、服务器不可达 1.093 s、换端口后 249 ms 收到 30 帧、中继转直连 7.464 s（含开头 6 s 的人为阻断）。根模块实际依赖版本下的 Pion 专项 race 回归通过（5.107 s），涵盖阻塞写关闭、自动 renomination、中继转直连、relay-only、TURN/TCP 及新增异步写测试。Linux screen 测试二进制及无 CGO 的 relay 构建通过。当前两端已部署 peerfix8 / iPhone 1118。

### 真机构建与部署

- 真实 iPhone Duo 已通过 USB 连接（Xcode 名称「Yi的iPhone」）。覆盖安装 `io.hermesh.ios` 0.1.1 / build 1112，并启动；没有删除应用或清理登录数据。
- 真机 iphoneos 目标构建及 Apple Development 签名通过；构建目录 `/tmp/hermesh-peer-device-build.ohzSC4/`。
- Mac「yee-homebook」原先运行 `0.1.5-dev.9486bda`。确认无运行中会话后，备份完整应用到上述目录的 `Hermesh-original.app`，更新 daemon 和屏幕 helper，以原 Developer ID 及标识签名，完整 bundle 签名校验通过。
- daemon 已重启为 `0.1.5-dev.54b405f6-peerfix`，本地 API 确认 relay 已连接。没有替换 Electron 界面、Her 程序或用户数据库。
- T1 正在等待用户在 Duo 打开 `yee-homebook` 的远程桌面；尚未获得真机会话数字。后续 T1–T11 以实测更新 §4。

### 真机首轮结果（修复验收未通过）

自动化通过没有解决当前真实网络的全部问题：

- USB 连接时，`41dde59896313a69` 在 9.112 s 首次选中中继，10.908 s 切到 IPv6 host→host。Mac 路由显示该地址经 `utun11`，不能计为 Wi-Fi 直连通过。
- 拔 USB、保持同 Wi-Fi 后，`5ba82322bc130c04` 在 8.267 s 选中中继，随后更换中继候选；用户报告无画面、延迟高。未完成 20 次，不填写虚构的成功率。
- 增加事件式本地诊断后，`bfb6f5974e55177e` 在 450 ms 产生 34,832 字节编码帧，16.053 s 选中中继，18.820 s 完成 PeerConnection 连接，26.478 s 输入通道开启；用户仍报告无画面。候选含 Mac `30.235.0.214` 与手机 `30.235.8.201`，同属 Mac 的 `/20`，但没有选中这条 LAN 路径。
- Mac 到手机的路由为 `en0`；三次 ICMP 探测均无应答、ARP 查询没有记录。这不单独证明应用权限或 AP 隔离，也不能代替 UDP 连通性证据。
- 本地新增候选地址、连接阶段、首个编码帧、同网段检查计数及 RTP 发送统计；不记录 SDP、ICE 密码或屏幕内容。诊断版 Mac daemon 为 `peerfix3`，手机仍是 build 1112，连接算法未因诊断改变。
- 限定 UDP 抓包没有取得有效样本：采集时会话已结束。采集进程已停止，空 pcap 已删除；不计为 T6。
- 切到 Duo 个人热点、USB 拔掉后仍失败。Mac `172.20.10.2` 到手机 `172.20.10.1` 的 ICMP 为约 3–5 ms；会话 `8184c0e22a1ba406` 的物理 IPv4 候选在约 8 s 时发送 26 次检查、收到 0 次响应，18.477 s 失败。
- Mac `peerfix4` 增加超过 100 ms 的 UDP 写入诊断。`a6b75bde20e20808` 在 4.017 s 取得统计，未记录慢写，7.275 s 关闭；当前证据不支持 UDP 写入阻塞的假设。RTP 统计缺失时现已明确记录 `rtp_stats_available=false`，不能把缺失统计当作发送量为零。
- 手机暂停 VPN/代理后，候选中的 `198.18.0.1` 消失，但 `f7409cd4044dcf6d` 仍在 18.539 s 失败；物理 IPv4 检查 31 次、响应 0 次。用户报 `client connection lost`；对应依赖中的 HTTP/2 存活 PING 失败，需要和手机端信令事件进一步对照。
- 补充手机信令诊断时发现构建缺陷：Go 核心源库在 15:05 更新，但 DerivedData 中参与链接的 `libhermeshcore.a` 仍为 13:47，成品没有新日志字符串。Xcode 的同目标脚本没有声明产物依赖，成功构建不能保证新核心进入应用。已修复并验证，见 F4；此前真机包不能视作最新源码的精确验收产物。
- 第二次限定抓包进程已按时停止，文件仍为 0 字节，已删除；没有取得可用 UDP 样本，不计为 T6。根因仍未确认，不签署真机验收。

### 真机后续：Surge 共存、慢 TURN 和首次信令恢复

- 用户明确：手机关闭 Surge 后无法访问 Hermesh，不能将关闭手机代理作为有效媒体对照。Mac Surge 开启增强模式、系统代理，规则推演中手机热点和 TURN IP 均为 `DIRECT`；这不等于绕过 VIF。没有修改用户的代理配置；准备的自动切换脚本未启动。
- 用户手动断开 Mac 代理后有画面。`a7969ac0c329f33d` 在 13.490 s 首先选中 `172.20.10.2 → 172.20.10.1` 的物理热点路径；22.326 s 才切到 Xcode 无线调试通道。后一条的约 7 ms 不计为热点 RTT。
- 独立实验排除 Surge 参与：本机两端、可用 loopback 路径、12 个无效 host 候选；不加 TURN 权限延迟约 6 ms 连上，加 150 ms 则两端约 23 s 失败；F5 补丁约 5 ms 连上。Mac 实测到当前 TURN 的 STUN UDP 为 307 ms、TCP 为 588 ms，表明该阻塞条件具有实际相关性，不单独证明所有故障都由它造成。
- peerfix6 / iPhone 1116（已核验成品含 F5、F6）在两端 Surge 开启时，首次 `5c22982777f91467` 仍失败：Mac 在 175 ms 生成回答，手机在 20.336 s 才收到，Mac 已在 17.117 s ICE 失败。后续 `9b2f003f2c6e7db7`、`36f1b6c268e9c96a` 分别在 460 / 657 ms 首先选中物理热点 host→host，用户看到画面；随后都有 Xcode 通道切换，不能混算其 RTT。
- 已部署 peerfix7 / iPhone 1117。用户报告连续三次均有画面，日志中物理热点首条路径出现过 2.362 / 3.084 s（对应信令回答 2.309 / 3.061 s），后续 IPv6 物理热点为 598 / 770 ms。没有用成功打开的数量代替 T1 的 20 次严格验收，秒连门槛尚未全部达到。
- 用户报告 `8c14a2fac5a5fb5c` 约两分钟画面一直正常。en0 限定手机候选及 TURN、排除 DNS 的 UDP 抓包取得有效样本，采集进程正常退出；原始 pcap 已分析并删除。稳定一分钟后的 60 s（16:07:03.746–16:08:03.746）共 1,298 个 STUN 包，21.633/s，单秒峰值 22，12 对地址，满足这个接口的 <50/s 门槛。
- 该会话在 30.559 s 切到 Xcode 无线调试 IPv6 通道，en0 抓包不覆盖其内部 UDP（外层为 TCP），也没有手机蜂窝能耗数据。因此上述结果仅为物理接口的部分 T6，不能签署完整 T6，也不能将整段画面连续性算作纯热点长会话。正在请用户退出 Xcode/Device Hub 并重连 Mac Wi-Fi，排除调试通道后重测；没有取消配对。
- 用户退出 Xcode / Device Hub、Mac Wi-Fi 重连后，`1df167ad1dbdac77` 的两端候选均不再含调试通道；617 ms 选中物理热点 IPv4，638 ms PeerConnection connected，随后仅在物理 IPv4/IPv6 间切换，4 分 24.303 秒后关闭，未见 failed。用户确认画面一直正常。这是有效的纯热点连续性样本，尚不是 70 分钟测试。
- peerfix8 返回原共同 Wi-Fi 后，`cb58ce43ad521733` 2.796 s 选中中继、3.694 s connected、4.648 s 输入就绪，用户确认共同 Wi-Fi 与蜂窝均能看到画面。手机切蜂窝触发同一 session 的两次 restart，候选不再含旧 Wi-Fi 地址，约 5.12 s 后恢复 connected，仍走中继；未完成 T4 全部子项及回程时延计量。
- 原共同 Wi-Fi 的 IPv4/IPv6 同网段 ICE 探测仍无响应。只读规则推演发现：Mac Surge 将手机 `30.235.8.201` 匹配到美国代理，IPv6 ULA 匹配 DIRECT；IPv4 路由为 en0，ARP 为 `00:00:00:00:04:01`，两次 ICMP 无响应，IPv6 邻居不完整。规则推演不能代替实际转发抓包，也不能单凭这些数据确认 AP 隔离。用户拒绝手动 DIRECT 规则作为解决方向；未安装该规则，不再等待此对照，改按下述标准能力审查推进。
- `hermesh-cloud/deploy/prod/README.md` 将实际 TURN IP `43.110.26.73` 记录为硅谷节点，与此前 307 ms 的 UDP 测量一致。没有现成近端节点，也未创建付费资源或部署云端变更；区域 TURN / TLS 443 仍需资源方案。

<a id="standard-path-review"></a>

### 标准方案复审：候选完整性与 TCP 直连

用户补充：UU 在同一环境连接快且稳定，但界面看不到连接类型。这是体验差距的证据，不能推断 UU 必然直连，也不能推断其协议实现。产品方案不得要求用户逐台填写 Surge DIRECT 规则。本轮没有修改代理、网络、在用应用或云端部署。

需要修正此前判断：选用 WebRTC/Pion 不等于已实现完整成熟的连接机制；“两批候选足够”和“只是网络/节点问题”均不能作为现阶段结论。

| 新发现 | 源码依据 | 实验 | 状态 |
|---|---|---|---|
| F8：第二批之后的有效候选未继续交换 | `gathering.wait(false)` 在 2 s 或首个 relay 时返回；`Viewer.exchange` 成功一次即退出；`hostSession.candidates` 返回单次快照 | 本地真实 TURN 的认证分配延迟 2.4 s；2.000 s 的第二批仅 host 11 / srflx 1，2.404 s 才出现 relay，已发送快照没有它 | 缺口已复现；完整 Trickle ICE 尚未实现 |
| F9：未启用标准 ICE-TCP 直连 | `newAPI` 仅设置 NetworkTypeUDP4/UDP6，没有被动 TCP mux；TURN URL 的 `transport=tcp` 不等价于 TCP 直连 | 丢弃所有 UDP：现有 API 2 s 内不能连接；采用 Pion 官方 ICE-TCP 接法，真实 DataChannel echo 经 host TCP 在 SDP 交换后 220 ms 完成 | 标准回退能力已验证；产品尚未接入 |

两个实验使用仓库锁定依赖、`go test -race` 和 overlay，未修改产品代码。总耗时 6.650 s，通过表示预期的能力差异被复现，不表示 F8/F9 已修复。TCP 实验限制在 loopback，没有视频、Surge 或真实 Wi-Fi，因此不算 T13 或原网络修复。F8 是确定的快照遗漏，不证明此前每次连接都遇到了该条件。

复现产物：

```text
/tmp/hermesh-peer-device-build.ohzSC4/standard-paths-experiment_test.go
/tmp/hermesh-peer-device-build.ohzSC4/standard-paths-overlay.json
/tmp/hermesh-peer-device-build.ohzSC4/standard-paths-experiment.log
```

```bash
go test -race \
  -overlay /tmp/hermesh-peer-device-build.ohzSC4/standard-paths-overlay.json \
  ./internal/screen -run '^TestStandardPathsExperiment' -count=1 -v
```

后续实现顺序、公共模块边界和兼容策略已修订至[设计 §5.0](peer-connectivity.md#50-standards-review-and-implementation-order)，新增 T12–T14 覆盖迟到候选、TCP 直连、区域 TURN/TLS 与凭据续期。优先修完整候选交换，再接 ICE-TCP，随后验证稳定探测预算和区域中继。无需先有近端节点才能修客户端，但客户端修复也不能替代中继布局。没有现成节点时先准备具体部署与费用，不能直接创建付费资源。

标准依据：[RFC 8835](https://www.rfc-editor.org/rfc/rfc8835.html#section-3.4) 区分 ICE-TCP 和 TURN 的 TCP/TLS 接入；[RFC 8838](https://www.rfc-editor.org/rfc/rfc8838.html) 定义候选增量交换与完成语义；[Pion ICE-TCP 官方示例](https://github.com/pion/webrtc/blob/v4.2.26/examples/ice-tcp/main.go) 展示 TCP mux 接法。代理的强制接管仍受系统策略约束，[Surge 官方文档](https://manual.nssurge.com/profile/general.html#vpn-tunnel-scope) 明确描述 include-all-networks 对接口绑定的影响；不能声称一个 socket option 保证绕过所有 VPN。

## 1. 发布判断

暂不签署完整验收。接口缓存、候选代次、旧版慢连、iOS 构建依赖、慢 TURN 阻塞、初次协商迟到和剪贴板回传均已修复并回归，当前安装构建为 Mac peerfix8 / iPhone 1118。完整 UI 连续两次通过，热点连续三次出画面，排除调试通道后也有 4 分 24 秒纯热点样本。尚未完成 T1 的 20 次，部分初次信令耗时 2–3 s；原共同 Wi-Fi 及蜂窝仍使用中继，局域网具体阻断点尚未确认，现按 F8/F9 补齐标准能力。T6 只有 en0 部分包量，手机能耗及 70 分钟长会话尚未完成，不能写作全部通过。

首轮按“两批交换、暂缓公共模块”的既定范围审查，现有实验已证明该收敛会遗漏有效候选，需修订实现范围。F8/F9 尚未修复，标准能力、原 Wi-Fi 直连和真机体验均未验收。仅本地记录诊断保持不变；公共模块先承载候选/代次/传输配置，RPC 直连权限与生命周期仍另行设计。TURN 部署和长会话凭据续期继续列为待办。当前安装版本仍为 Mac peerfix8 / iPhone 1118，没有把本轮实验称为已部署修复。

## 2. 发现

### F1 / P1：ICE restart 不刷新缓存接口，真实换网可能继续使用旧地址

位置：[viewer.go](../internal/screen/viewer.go:169)、[rtc.go](../internal/screen/rtc.go:109)。Mac 接收新 offer 后也使用同一 ICE agent，因此两端均受影响。

`Viewer.Restart` 只创建 `ICERestart` offer。Pion WebRTC v4.2.26 的 `ICETransport.restart()` 复用现有 agent，调用 `Agent.Restart()` 和 `Gather()`；ICE v4.4.8 的 restart 清理候选，但不更新 `stdnet.Net` 的接口快照。`stdnet.Net` 在创建时缓存接口及地址，`Interfaces()` 后续返回缓存。当前应用未调用 `UpdateInterfaces()`，也未使用会刷新接口的持续收集路径。

因此，检测到了系统地址变化，不等于 ICE 重新枚举了地址。当旧接口/IP 消失、新地址不在快照中时，新 offer 可能缺少实际可用的 host candidate，导致无法回到 LAN，甚至恢复失败。这比已接受的“Mac 换网多等 7–10 秒”限制更进一步。

隔离复现使用仓库锁定的依赖，不改变系统网络：通过 `stdnet.Interfaces()` 返回的对象注入旧的“接口均未启用”快照，然后运行相同的 restart/gather 调用链：

```text
Injected stale snapshot: host candidates=0
ICE Restart without interface refresh: host candidates=0
Explicit UpdateInterfaces then Restart: host candidates=6
```

复现代码保存在本机 `/tmp/hermesh-peer-review.9oH94V/ice-interface-snapshot.go`。这证明依赖层不会自动刷新缓存；不是一次真实 iPhone 切网测量。

当前 [TestScreenNetworkChange](../e2e/screen_test.go:226) 明确保持地址不变，实际观察到 `127.0.0.1` 的端口变化，因此该测试通过不能排除此问题。建议修复时覆盖接口刷新与并发安全，并补充真实 IP/接口集合变化的回归测试，再执行 T4 和 T9。

依赖证据：[stdnet 缓存](https://github.com/pion/transport/blob/v5.1.4/stdnet/net.go)、[ICE restart](https://github.com/pion/ice/blob/v4.4.8/agent.go)、[WebRTC restart](https://github.com/pion/webrtc/blob/v4.2.26/icetransport.go)。

### F2 / P2：旧候选响应可在检查代次后跨入下一轮 ICE

位置：[viewer.go](../internal/screen/viewer.go:233)。

`exchange()` 持有 `v.mu` 检查 `v.offers == n`，随后释放锁，再逐个调用 `AddICECandidate`。同时 `offer()` 只持有另一把 `omu`，并在调用 `CreateOffer(ICERestart)`、开始清空旧候选后，才递增 `offers`。

存在两种交错：restart 已开始但计数尚未增加；或者旧响应通过计数检查后 restart 才开始。两种情况下旧批次都可能被加入新的 ICE agent generation。发送的 candidate 字符串没有绑定 ufrag；当前 Pion 对旧代次的过滤依赖 candidate 字符串中的 ufrag 扩展，不能代替应用层序列化。`AddICECandidate` 的错误目前也被忽略。

可能结果是重复或无效的检查、额外探测及切网恢复延迟。这里记录的是代码级交错窗口，尚未在真机上复现业务失败。建议将“核对代次并应用整个响应”与 restart 串行化，并测试延迟候选响应与 restart 同时发生；仅在响应回来时比较一次计数不够。

### F3 / P2：屏幕 UI 自动化在剪贴板往返时失败

位置：[ComputerControlUITests.swift](../apps/ios/HermeshUITests/ComputerControlUITests.swift:154)。

`testComputerControl` 已打开画面并通过先前的输入步骤。Mac 的复制通知也已到达手机；随后点 Paste，等待“Mac 的文字从手机粘贴回 Mac”超时约 60 秒。XCTest 记录 1 项失败、0 项通过、0 项跳过；目标 UI 用例耗时 121.945 秒，Go `TestIPhone` 耗时 261.29 秒。

```text
failed - timed out: the Mac's text, back from the phone's clipboard
```

该会话的测试机器日志：

```text
screen: candidates session=da4a56a7e31015f0 local="host 4, srflx 2, relay 2" remote="host 11, srflx 2, relay 2"
screen: path session=da4a56a7e31015f0 local="host 127.0.0.1:52569" remote="host 127.0.0.1:50455" after=599ms
```

后续还记录了一次 IPv6 host→host 路径切换，未见连接断开记录。根因尚未定位，不能据此判断为 ICE、剪贴板实现或 UI 时序的问题。该失败导致后续 trackpad、工具栏、旋转关闭和 `controlRelayedThenDirect` 均未执行；不能把本次 UI 结果写成“中继转直连 UI 通过”。

## 3. 自动化结果

环境：Yee HomeBook，macOS 27.2（26B123），arm64；Go 1.27.1；Xcode 27.2（27B52）。自动化使用本机 relay、测试画面和模拟手机 core，没有跨越真实路由器。

| 命令或用例 | 结果 | 数字与说明 |
|---|---|---|
| `go test ./internal/screen -count=1` | pass | 34.397 s |
| `go test ./e2e -run '^TestScreen' -count=1 -v` | pass | 35.488 s；5 项屏幕用例通过，2 项环境限定用例跳过 |
| `TestScreenThroughTURN` | pass | 用例 11.22 s，不是首帧时间 |
| `TestScreenUnreachableTURN` | pass | 从页面打开到测试画面 600 ms |
| `TestScreenNetworkChange` | pass | 同一 IP 换端口后 251 ms 收到 30 帧，不代表真实 IP 切换 |
| `TestScreenTurnsDirect` | pass | 从页面打开到直连 7.422 s；包含开头 6 s 的本地 UDP 阻断 |
| `TestScreen` | pass | 从页面打开到测试画面 546 ms |
| `TestScreenLatency` | not run | 未启用真实屏幕采集，未测采集到显示延迟 |
| `TestScreens` | not run | 下载页截图用例，未设置输出目录，与连接验收无关 |
| 计划中的 iPhone UI 命令 | fail | 整体 508.484 s；首次登录通过，屏幕 UI 在剪贴板往返超时，见 F3 |
| 补充：`go test -race ./internal/screen -count=1` | fail | 46.661 s；未报告数据竞争，旧机器兼容用例连接 10.121 s，超过 10 s 门槛；当时与 UI 构建并行，独立复测见下一行 |
| 补充：UI 结束后独立运行旧机器兼容用例，`-race -count=3` | fail | 2 次通过（3.038/3.043 s 连接），1 次超时门槛（10.117 s）；未报告数据竞争，不应仅归因于 UI 构建负载 |
| 补充：旧机器兼容用例，不加 `-race`，`-count=5` | pass | 5/5，通过；连接 2.261/2.091/2.093/2.087/2.095 s，总计 35.287 s。尚无普通构建兼容失败证据 |
| 补充：接口快照实验 | issue reproduced | restart 后候选仍为 0，显式刷新后为 6 |

计划中的 `-run TestIPhone` 同时匹配 `TestIPhoneFirstRun` 和 `TestIPhone`。首次运行 UI 通过（Go 用例 246.85 s，XCTest 119.288 s）；后者的 `testComputerControl` 失败。后续只运行屏幕 UI 可使用 `-run '^TestIPhone$'`，避免额外执行整个首次登录流程。

UI 产物目录：`/tmp/hermesh-peer-review.9oH94V/`，保留了 `iphone-test.txt` 与失败的 `iphone.xcresult`。已用 `xcresulttool get test-results summary` 核对屏幕 UI 为 1 项失败、0 项跳过。

## 4. T1–T11 真机结果

| Test | Result | Numbers | Notes |
|---|---|---|---|
| T1 同 Wi-Fi 20 次 | fail（首轮）；修复后部分复测 | 原 Wi-Fi 会话曾 8.267 s 才选中中继且无画面；peerfix7 热点物理首条路径出现 2.362 / 3.084 / 0.598 / 0.770 s | 用户报告连续三次有画面；20 次及严格时延门槛尚未完成；Xcode 虚拟路径不计为 LAN 成绩 |
| T2 STUN/TURN 不响应 | not run | 自动化首帧 600 ms，仅作补充 | 未在两台真机上阻断 3478 并完成 10 次 |
| T3 本地网络授权 | not run | 无 | 需要手动处理授权及重新安装；没有更改现有权限 |
| T4 iPhone 换网 | partial | 同 session 从 Wi-Fi 地址切至蜂窝地址、两次 restart，约 5.12 s 恢复 connected | 用户报告两种网络均有画面；完整 a–f、回程及从系统切网起计时未完成 |
| T5 跨网基线 | partial | Mac 原 Wi-Fi、Duo 蜂窝，中继连通 | 尚未完成每类网络 3 次及多网络基线 |
| T6 探测成本 | partial | en0 稳定窗口 60 s，1,298 STUN，21.633/s，峰值 22/s，12 对地址 | pcap 已删除；后续媒体走 Xcode TCP 调试通道，其内部 UDP 及手机蜂窝未覆盖；能耗未测，完整 T6 未通过 |
| T7 中继延迟 | not run | 无 | 未取得两端到实际 TURN 的测量数据，不能归因于节点距离 |
| T8 新旧版混用 | not run | 模拟旧能力的单元测试通过 | 未实际安装发布版与开发版完成四种真机组合 |
| T9 Mac 换网 | not run | 已为热点对照切换网络 | 尚未在活动会话中完成换网、同会话恢复及耗时验收 |
| T10 Linux 机器 | not run | 无 | 本轮没有真实 iPhone 与 Linux 屏幕会话 |
| T11 70 分钟 | not run | 无 | 未启动长真机会话；凭据过期后 restart 的已知缺口未验证 |

设备检查时，Xcode 将两台已配对 iPhone（系统 27.2.1）列为离线。没有安装或删除手机上的应用，没有修改两端网络。用户已一次性同意本次所需的 Hermesh 日志读取、限定 UDP 抓包及配合切网，并安排手机步骤稍后进行。

## 5. 机器日志与后续验收

仅检查了 `~/Library/Application Support/Hermesh/logs/` 中日志的 `screen:` 行，当前 `hermeshd.log` 没有这种记录；没有可用于判定本次真机路径的会话样本。未读取聊天、凭据或其他业务内容，也没有生成 pcap。

恢复真机测试前，先排查 F3、让计划 §2 全部通过；处理 F1/F2 后按实际修复提交重跑回归，确保两端来自同一提交。真实 iPhone 在线并由用户操作后，执行 T1–T4、T6、T9；T5/T7 收集公网基线，T8/T10 视版本及设备可用性补齐，T11 单独安排至少 70 分钟。兼容用例在 `-race` 下的不稳定也需调查，当前结果不能写成所有并发检查通过。

首轮仅增加报告，后续按用户授权修复并补回归，见 §0。主工作区已从 `7efb8925` 快进到审查提交；原中文版设计和旧屏幕文档备份于 `/var/folders/rr/ycxylhln6_j0mq4q79h3chsc0000gn/T/hermesh-connectivity-docs-u92vh5e7/`。用户原有 `.env` 和 `skills/` 未修改。

## 附录：接口缓存隔离复现

把以下内容另存为临时 `.go` 文件，在本仓库目录执行 `go run /path/to/file.go`。它只修改测试使用的 stdnet 对象，不修改系统接口；IPv4 host 候选刷新后的数量由本机接口决定。

```go
package main
import (
 "fmt"
 "net"
 "time"
 "github.com/pion/ice/v4"
 "github.com/pion/transport/v5/stdnet"
)
func main() {
 n, err := stdnet.NewNet(); must(err)
 interfaces, err := n.Interfaces(); must(err)
 // Inject an old snapshot in which no interface was up. The OS network
 // is untouched; stdnet.Interfaces exposes its cached interface objects.
 for _, i := range interfaces { i.Flags &^= net.FlagUp }
 a, err := ice.NewAgentWithOptions(ice.WithNet(n), ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4}), ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost}), ice.WithIncludeLoopback()); must(err)
 defer a.Close()
 gather := func(label string) {
  done := make(chan struct{})
  must(a.OnCandidate(func(c ice.Candidate) { if c == nil { close(done) } }))
  must(a.GatherCandidates())
  select { case <-done: case <-time.After(3*time.Second): panic("gather timeout") }
  c, err := a.GetLocalCandidates(); must(err)
  fmt.Printf("%s: host candidates=%d\n", label, len(c))
 }
 gather("Injected stale snapshot")
 must(a.Restart("", ""))
 gather("ICE Restart without interface refresh")
 must(n.UpdateInterfaces())
 must(a.Restart("", ""))
 gather("Explicit UpdateInterfaces then Restart")
}
func must(err error) { if err != nil { panic(err) } }
```

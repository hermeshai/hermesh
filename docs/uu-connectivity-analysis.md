# UU 连接实现逆向分析与 Hermesh 改进方向

日期：2026-11-12。分析对象为本机 `/Applications/UURemote.app`，版本 **4.43.1 / 627**，bundle ID `com.netease.uuremote`。对照 Hermesh 安装版 peerfix9 / iPhone 1119，以及 Claude 工作区提交 `b540d7ca`。

**本次 UU 会话确实建立了手机 5G 到 Mac 的 UDP P2P 路径。** 用户在客户端看到 `P to P UDP`；限定在 UU socket 的包迹也显示 Mac 与手机公网地址直接双向通信。这排除了“UU 只是因为中继更近所以体验好”作为这次对照的充分解释。UU 的本地二进制进一步表明，它采用带有扩展的原生 WebRTC：除了常规 ICE，还实现 NAT 映射分析、增强打洞、网络绑定、质量驱动的路径切换，以及 pacing、FEC 和重传控制。

这些能力不等于每次连接都会启用。当前已经确认实际 P2P；还没有恢复这次会话完整的 ICE 状态机，不能直接断言它依靠某个增强打洞分支成功。Hermesh 下一步应先解决映射与 socket/路由的一致性、信令交付和接收端恢复，再按真实 NAT 数据决定是否扩展打洞算法。

## 1. 分析范围和可复核证据

本轮进行了程序版本和依赖检查、ARM64 静态反汇编、字符串到实际指令的交叉引用、进程 socket/流量观察、限定 UDP 包元数据分析，以及 8 次标准 STUN Binding 请求的映射对照。没有修改 UU 程序、登录状态、代理规则或 Hermesh 产品代码，也没有把 UU 二进制、账户数据或抓包上传外部。

主要组件：

| 组件 | 观察 |
|---|---|
| `UURemote` | 主客户端进程 |
| `UURemoteDaemon`、`UURemoteService` | 常驻后台组件 |
| `UURemoteServer` | 本次 Mac 被控端进程，PID 28204，链接 `libstreamer.dylib` |
| `libstreamer.dylib` | 原生 C++/ObjC 串流实现；使用 VideoToolbox、ScreenCaptureKit、Network.framework 等 |
| `ExchangeControllerInterface`、`ExchangeControlledInterface` | 库导出的两端接口；内部有会话、ICE 连接和多种数据通道 |

`libstreamer` 大部分内部符号已剥离，因此下文地址指向 **ARM64 文件内虚拟地址**，不是 ASLR 后的进程地址，也不是声称拿到了原始源码。库内初始化版本字符串为 `V4.8.9` 和 `5405.1.23`，分别邻接 streamer / nrd-webrtc 初始化日志；不是公开上游 WebRTC 版本承诺。

原始通用架构库 SHA-256 及各组件完整哈希在 `binary-inventory.json`；抽取 ARM64 库 SHA-256：

`85030d683d91169ce5530f81445b4e9ff7d9b1699ca79d04a6e56f11737d7615`

本地分析产物目录：

`/var/folders/rr/ycxylhln6_j0mq4q79h3chsc0000gn/T/hermesh-uu-research-5m1_z9qy`

其中 `macho-layout.json` 记录 LC_FUNCTION_STARTS；`function-*.asm` 为选定函数；`selected-static-evidence.json`、`compiled-field-trials.json` 为筛选后的证据。二进制与完整反汇编留在临时目录，不加入仓库。

## 2. 真机对照：本次确实直连

用户按请求保持手机 5G、两端 Surge 开启、USB 拔线，用 UU 连接这台 Mac，并报告客户端显示 `P to P UDP`。

200 个限定包的 1.431 s 样本中：

| 路径 | 包数 | 观察 |
|---|---:|---|
| `192.168.77.95:56331 → 124.160.204.3:45719` | 149 | 持续 UDP 数据、DTLS/STUN 解析结果 |
| `124.160.204.3:45719 → 192.168.77.95:56331` | 45 | 双向回应及数据 |
| 同一个本地端口与三个 STUN 端点 | 6 | 每个端点一组请求/响应 |

手机公网 IP 与此前 Hermesh 5G 候选中的 `124.160.204.3` 一致；该主数据流不经过此前 Hermesh 的 TURN `43.110.26.73`。包迹与客户端状态互相支持本次 UDP P2P 判断。样本不足以衡量端到端画面延迟或解密后的媒体质量。

三个 STUN 端点为 `101.69.134.132:2580`、`101.71.76.201:2581`、`101.71.76.254:2582`。它们与手机对端使用 **同一个本地 UDP 端口 56331**，这是实际运行证据，不只是字符串推测。

需要纠正一个监控陷阱：Surge 的 active 记录把这个 UDP socket 的大量累计字节显示在最早的 STUN 地址旁，不能由该记录推断所有字节都发给 STUN 服务。限定包迹找到了实际手机对端。`nettop` 中约 1 ms 的 TCP RTT 是代理虚拟接口一侧的测量，也不能代表手机到 Mac 的 RTT。

本次流量在采样处经过 `utun4`，所以不能宣称 UU 必须绕过 Surge 才能直连，也不能由 UU 直连成功反推 Hermesh 的每一种 socket 用法都兼容该代理。更早那个 `30.235.x.x` Wi-Fi 没有在本轮重新测试，不把当前结论外推为旧网络根因已经解决。

### Mac 公网映射对照

独立使用标准 STUN Binding 请求，对上述三个服务各发一次，再重复首个服务一次；系统路由与绑定 en0 各 4 次，合计 8 次。没有使用 UU 的凭据、私有控制协议或服务器管理接口，也不把这些服务作为 Hermesh 的生产依赖。

| 模式 | 本地 socket | 四次观察到的公网映射 | RTT 范围 |
|---|---|---|---|
| 系统路由 | `0.0.0.0:63205`，未绑定接口 | 均为 `183.247.171.62:54428` | 8.98–17.85 ms |
| `IP_BOUND_IF` 绑定 en0 | `192.168.77.95:53344`，核对接口索引 15 | 均为 `183.247.171.62:53344` | 9.03–11.24 ms |

在这几个目的地址上，Mac 映射保持一致；两种路由下的端口处理不同。**这不是完整 NAT 分类**：没有测入站过滤、全部目的地址、超时后的状态，也没有测 iPhone 侧。因此不能称“已经证明两端都是 Cone NAT”或“已经证明增强打洞必需”。它支持先检查同一传输路径上的映射一致性，再决定是否需要端口预测。

证据：`uu-peer-summary.json`、`uu-active-metadata.json`、`nettop-5g-summary.json`、`stun-mapping-results.json`；复现脚本 `stun-mapping-probe.py`。

## 3. 反汇编确认的实现策略

### 3.1 WebRTC 之上有额外 NAT 穿透实现

`0x1ae0ec–0x1ae130` 从配置复制 `enable_ice_full_punch` 状态。`0x1af1c4` 检查该状态，为 1 时走向 `post NatTraversal task` 对应的任务提交路径。不是只有未引用的文字。

可定位的其他分支包括：

| ARM64 地址或函数起点 | 证据与含义 |
|---|---|
| `0x1b3a9c` | 分析本地、远端 STUN candidate group，输出两端 NAT 类型 |
| `0x1b70e0` | `ConePunchToLinearSymmeric` 分支引用 |
| `0x1b6cc8` | `ConePunchToRandomSymmeric` 分支引用 |
| `0x1b8154` | `BothRandomSymmericPunch` 函数；生成去重的端口集合，存在有上限的循环 |
| `0x1b89d4` | 预测打洞地址的代码引用 |
| `0x1bb854` | 可写 P2P 成立后停止本轮增强打洞；递增 epoch 使旧工作失效 |
| `0x1b5198` | ICE restart 时重置打洞线程的分支 |

这说明 UU 的能力超出当前 Hermesh 的普通候选检查。对照拉取的上游 `p2p_transport_channel.cc` 和 `basic_ice_controller.cc` 没有这些 `full_punch` / `Symmeric` 分支；但一次上游对照不能证明具体私有算法的来历。

**可借鉴的是策略结构：** 同一 socket 对多个目的地址采集映射，按观测选择有限的增强尝试，成功就停止，换网使旧代次失效。不能据函数名就复制私有实现、固定端口数量，或把所有失败都处理成高频随机打洞。

### 3.2 网络接口和 VPN 是显式输入

函数 `0x541260` 中确认了实际 `setsockopt` 调用：IPv4 用 `IPPROTO_IP / 25`，IPv6 用 `IPPROTO_IPV6 / 125`，并有对应 `IP_BOUND_IF` / `IPV6_BOUND_IF` 成败分支。还有 `RTCNetworkMonitor`、Network.framework 路径更新、接口名匹配的实现。

应用层 `NetworkProbeLoop` 包含 physical/default 路由探测、物理绑定失败回退、RTT/丢包测量；`0x764560` 附近包含排序接口和绑定 socket 的逻辑。初始化字段还包含 `WebRTC-AddNetworkCostToVpn/Enabled`。这些说明代码有区分网络路径的设计，不代表这次整个会话绕开了 VPN；包迹恰好显示了 utun4 上的 P2P。

Hermesh 已有接口绑定修复，但当前 `network_apple.go` 明确保留 STUN/TURN 通配 socket 的系统路由；仅 host socket 被绑定到地址所属网卡。需要验证这两类候选面对 Surge 时究竟代表哪些传输路径。

### 3.3 信令支持多入口和探测后的重连

`signal_server_url_list`、连接超时、重连循环等有实际代码引用。函数 `0x74d230` 中存在使用首个探测成功入口重新连接的路径，相关日志为 `socket reconnect use first probe success server url`。

这支持“信令入口也需要探测与失败恢复”的设计；不能据此宣称 UU 的所有信令都并行竞速或有某个固定超时。本轮未测 UU 从点击到首帧的完整时间。

### 3.4 按连接质量切换，包含 UDP 和 TLS 中继

`0x81fcf4` 使用丢包/延迟相关配置并包含浮点阈值比较，异常时走优先 UDP relay 的切换分支；`0x8202b0` 有 TLS relay 的另一分支。输入配置包括 `force_auto_switch_pkt_loss`、`force_auto_switch_latency` 和 `possible_auto_switch_*`，另外记录切换次数和已成功手动切换状态。

它不是“只要直连就永远选直连”的单一策略。Hermesh 当前 renomination 主要比较候选种类和 RTT，缺少同等的媒体质量驱动切换。TLS relay 与 ICE-TCP 是不同能力；本次已确认 P2P UDP，不能拿未实现 ICE-TCP 作为这次差距的解释，也不改变用户暂缓 ICE-TCP 的决定。

### 3.5 媒体恢复和发送调度比当前 Hermesh 完整

初始化函数 `0x5d8d64` 和另一初始化路径把编译内置 field-trial 字符串传给 `0x261848`；后者明确是 `field_trial.cc` 的设置逻辑，将字符串写入全局位置。这比“库里能搜到某个功能名”更强，但会话协商和运行时参数仍可影响最终行为。

其中默认启用的相关项目包括：

- `WebRTC-FlexFEC-03`、`WebRTC-RsFecEnabled`、`WebRTC-RsFecRtxAsSource`。
- `WebRTC-Pacer-FastRetransmissions`、`WebRTC-PacerV2-BurstAimd`。
- `WebRTC-RetransmissionRateLimit`、`WebRTC-RemoteControlSharedProtectionRateLimit`。
- 拥塞窗口反馈、快速带宽恢复和随机丢包处理相关分支。

这些参数不能直接照抄进 Pion；其含义由对应的实现决定。Hermesh 当前明确使用 `gcc.NewNoOpPacer()`，自定义组帧器又固定在 250 ms 放弃缺包帧，前一轮已复现 575 ms 补回分片却不能出帧。应先把 pacing、丢包恢复、缓冲与重传预算联动测起来。

上游 WebRTC 的 [NackRequester](https://webrtc.googlesource.com/src/+/refs/heads/main/modules/video_coding/nack_requester.cc) 会更新 RTT 并用它调度后续 NACK，还区分 FEC/重传恢复包；这是可参考的公开实现，不是对 UU 私有参数的猜测。

### 3.6 QUIC/KCP 存在，但不是本次 UDP 穿透的已证实原因

`0x4a48fc` 是 data-channel wrapper 的构造分支，根据配置调用 QUIC 或 SCTP 通道创建；KCP 相关代码位于 `media/sctp/dckcp_transport.cc`，包括 FEC/重传处理。结合 `x-uuremote-quic` / `x-uuremote-mix-kcp` 协商字段，说明它扩展了数据通道的传输选择。

这不证明视频通过 QUIC 发送，也不证明当前会话启用了 QUIC/KCP。数据通道如何可靠传送和 UDP 路径如何穿透 NAT 是不同问题，不能把“换 QUIC”作为直连修复。

### 3.7 首帧和卡顿可分段归因

实现包含首次采集、编码、packetization、pacer exit、接收、组帧、解码、呈现等阶段；有 `first_present_timestamp`、NACK/FEC 恢复比例、卡顿归因及各阶段 P50/P90 字段和代码引用。

Hermesh 目前只能从 Mac 日志确定 first encoded，不能在真机上明确划分“网络没收到完整关键帧”和“解码没呈现”。这项诊断缺口应与实际修复一起补上，不需要上传 center。

## 4. 对照 Hermesh 后的执行顺序

| 优先级 | 具体工作 | 为什么先做 | 验收 |
|---|---|---|---|
| P0 | 候选使用同一 socket/路由的验证；评估 Pion `UniversalUDPMux` 与 host/srflx 收集的接入 | 当前非 mux 的 `gatherCandidatesSrflx` 为每个 URL/网络新开 socket；host socket 又单独收集。只增加 STUN URL 不能得到 UU 那样的同一 socket 多目的映射观测 | 记录 host、srflx、检查包和最终媒体的 socket/接口关联；在原 5G + Surge 环境反复取得对手机公网地址的双向 UDP 路径；换网后无旧 socket 泄漏 |
| P0 | 在手机同一 socket 做映射/候选诊断，定位真实打洞阻断点 | Mac 这次映射一致，手机端的映射和过滤行为尚未知；不能先验归咎对称 NAT | 对应两端同一 ICE 代次，确认检查是否发出、是否收到、为何未验证/提名；用可重现网络测试覆盖发现的问题 |
| P0 | 修复协商回复停顿，并增加首包、完整关键帧、解码呈现的本地事件 | UU 能 P2P 不会自动解释 Hermesh 的 6 s 信令超时；先确保每次都进入候选检查 | 空闲后重开、切网后新开各记录完整时序；失败直接落在具体阶段；不再把仅 PC connected 计为成功 |
| P0 | RTT 感知的丢包恢复、pacing 对照、重传预算 | 已有组帧失败复现；只优化建连无法解决连上后黑屏 | 20/150/600 ms RTT、单包和突发丢失下，真实编码到解码画面可恢复、队列有界；LAN 不额外固定缓冲 |
| P1 | NAT 增强打洞小实验，按实际映射结果决定是否进入产品 | UU 确有实现，但尚未证明本次成功需要它 | 自己的测试端点与受控 NAT 模型；有限时长/包数；与常规 ICE 做成功率对照；失败回退、成功降频停止 |
| P1 | 近端 TURN、TLS 443、丢包/RTT 联合选路 | 保证不能直连的网络仍可用；不是对本次 UU UDP 成功的替代解释 | 同一网络比较直接/中继的首帧、RTT、丢包、恢复；部署另行准备可审查方案 |

接入限制：当前 WebRTC v4.2.26 暴露 `SetICEUDPMux`，但没有 `SetICEUDPMuxSrflx`；ICE 层有 `WithUDPMuxSrflx`。必须核对并接通 srflx 的实际收集路径，不能把仅调用 host mux API 说成已实现 STUN/媒体同 socket。

先继续使用 Pion 验证和修复上述具体差距，不立即把产品换成另一套引擎。若要评估原生 WebRTC，只建立隔离的相同网络基准，比较成功率、首帧、耗电、包量和构建维护成本；不能因为 UU 使用它就认为迁移一定解决问题。

复用边界应落在“身份验证、候选/网络生命周期、路径选择、连接统计”的传输层；编码、组帧、FEC、画面诊断留在远程桌面媒体层。数据通道为将来的 RPC/文件传输提供承载，但 RPC 权限、数量和生命周期仍按原计划明确后再接入。分析本身不引入公共模块重构。

## 5. 限制与采样处理

- UU 的 `.slog` 文件是带有加密日志标识的二进制格式；没有把随机字节当作日志，也没有获取解密私钥或导出账户数据库。这次结论来自二进制静态引用、操作系统元数据、标准协议探测与包头统计。
- 第一段仅针对 STUN 端点的采样，原定 15 s 的 Python 清理未能终止 tcpdump；SIGINT 无效后单独 SIGKILL，核对进程已退出。实际 80.115 s、18 个 STUN Binding 包；单独记录为 `uu-5g-header-summary.json`，不把它当媒体证据。
- 第二段按 UU 本地 UDP 端口过滤、最多 200 包或 15 s；200 包在 1.431 s 内完成。Apple PKTAP 自动把请求的 96 字节 snaplen 提高到 524288；实际只提取地址、端口、协议类型、长度和接口，未解密/展示媒体负载。两段原始 pcap 均已删除，采集进程和临时采集脚本均已清理。
- 仍不能给出这次究竟触发了哪个私有 NAT 增强分支、完整手机侧收集时序或真实端到端画面时延。不能将本轮研究表述成 Hermesh 已修复或已达到 UU 的成功率。

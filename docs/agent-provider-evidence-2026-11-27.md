# Agent Provider 社区活跃度与安装验证记录

采样于 2026-11-27。Hermesh 前端、授权列表和机器发现注册的外部 provider 共 16 项。以下区分社区使用信号、上游维护和可执行的安装渠道，供[技术方案](agent-provider-lifecycle.md)决定首批投入。[前端 ID](../packages/ui/src/agents.ts) · [机器注册](../internal/machine/sessions.go) · [ACP 启动参数](../internal/runtime/acp/agents.go)

## 社区证据

**统计窗口**为 UTC 2026-08-29 至 2026-11-26，共 90 个完整日。GitHub 栏给出窗口内新建 issue 总数，以及按新建时间倒序前 100 条中去重的 User 类型提交账号数；不足 100 条取全部。这是参与下限，包含维护者和可能由人类账户运行的自动化。Kiro 仓库同时含 IDE，限定标题含 CLI。个别产品以论坛、Discussions 或外部集成为主要公开渠道，单独标注。不同口径只用于判断是否有持续参与迹象，不据此排跨产品名次。

npm 下载取 UTC 2026-10-30 至 2026-11-26 的 **28 天包下载次数**，可能含 CI、缓存、镜像和重复安装。发版数为同一 npm 包 90 天内新增且版本名为纯 `X.Y.Z` 的发布次数，也可能含维护旧主线。两项用于交叉检查分发和维护，不能换算为活跃用户。原生优先产品在 npm 栏用「—」。没有把 Star、模型品牌用户数或 IDE 总安装量作为 CLI 流行度依据。

| CLI | 可复核的公众参与 | 28 天 npm 下载 | 90 天正式版本号发布 |
|---|---|---:|---:|
| Claude Code | [21,933 个 issue／56 个账号](https://github.com/search?type=issues&q=repo%3Aanthropics/claude-code%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [66,947,100](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40anthropic-ai%2Fclaude-code) | 70 |
| Codex | [17,918 个 issue／98 个账号](https://github.com/search?type=issues&q=repo%3Aopenai/codex%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [107,270,185](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40openai%2Fcodex) | 38 |
| Gemini CLI | [421 个 issue／72 个账号](https://github.com/search?type=issues&q=repo%3Agoogle-gemini/gemini-cli%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [2,012,460](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40google%2Fgemini-cli) | 15 |
| Antigravity | [1,016 个 issue／78 个账号](https://github.com/search?type=issues&q=repo%3Agoogle-antigravity/antigravity-cli%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | — | — |
| Cursor | [官方论坛检索首屏 50 个主题／30 个发帖账号](https://forum.cursor.com/search?q=CLI%20after%3A2026-08-28%20before%3A2026-11-27) | — | — |
| Copilot CLI | [875 个 issue／87 个账号](https://github.com/search?type=issues&q=repo%3Agithub/copilot-cli%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [6,966,310](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40github%2Fcopilot) | 34 |
| OpenCode | [9,941 个 issue／90 个账号](https://github.com/search?type=issues&q=repo%3Aanomalyco/opencode%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [3,419,820](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40opencode%2Fcli) | 52 |
| Qwen Code | [2,980 个 issue／26 个账号](https://github.com/search?type=issues&q=repo%3AQwenLM/qwen-code%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [609,032](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40qwen-code%2Fqwen-code) | 37 |
| Goose | [1,065 个 issue／77 个账号](https://github.com/search?type=issues&q=repo%3Aaaif-goose/goose%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | — | — |
| Auggie | [21 个 issue／11 个账号](https://github.com/search?type=issues&q=repo%3Aaugmentcode/auggie%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [173,911](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40augmentcode%2Fauggie) | 6 |
| Kimi Code | [689 个 issue／76 个账号](https://github.com/search?type=issues&q=repo%3AMoonshotAI/kimi-code%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [416,914](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40moonshot-ai%2Fkimi-code) | 32 |
| Kiro | [264 个 issue／62 个账号](https://github.com/search?type=issues&q=repo%3Akirodotdev/Kiro%20is%3Aissue%20created%3A2026-08-29..2026-11-26%20CLI%20in%3Atitle%20sort%3Acreated-desc) | — | — |
| Qoder | [官方论坛检索首屏 50 个主题／41 个发帖账号](https://forum.qoder.com/search?q=CLI%20after%3A2026-08-28%20before%3A2026-11-27) | [81,391](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40qoder-ai%2Fqodercli) | 65 |
| DeepSeek Harness | [项目 Discussions 样本 47 个主题／45 个账号](https://github.com/deepseek-ai/deepseek-harness/discussions)；含桌面与 CLI | [2,957,027](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40deepseek-ai%2Fdsh) | 0 |
| CodeBuddy Code | [跨仓标题样本，44 项／30 个账号](https://github.com/search?type=issues&q=is%3Aissue%20%22codebuddy-code%22%20in%3Atitle%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [799,516](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40tencent-ai%2Fcodebuddy-code) | 54 |
| Grok Build | [跨仓标题样本，67 项／50 个账号](https://github.com/search?type=issues&q=is%3Aissue%20%22Grok%20Build%22%20in%3Atitle%20created%3A2026-08-29..2026-11-26%20sort%3Acreated-desc) | [377,701](https://api.npmjs.org/downloads/point/2026-10-30:2026-11-26/%40xai-official%2Fgrok) | 81 |

论坛按同一查询的 Discourse `search.json` 首屏返回的 posts 计数，按 topic_id 和 username 分别去重。Cursor 的这页覆盖 11 月 24–26 日，Qoder 的这页覆盖 8 月 31 日至 11 月 26 日。DeepSeek 官方关闭 Issues，取 Discussions 按创建时间倒序最近 100 条，再按窗口过滤。Grok 的官方代码仓库定期从 monorepo 同步，也关闭 Issues。对 Grok／CodeBuddy 另取 GitHub 公共 issue 标题搜索首批 100／68 条，保留标题实际匹配 `grok[ -]+build`／`codebuddy[ -]+code` 且提交者类型为 User 的记录，来自 47／28 个仓库。这是生态提及，可能包含工具集成和名称碰撞，置信度低于官方专用仓库。[Grok 官方仓库说明](https://github.com/xai-org/grok-build) · [DeepSeek 社区](https://github.com/deepseek-ai/deepseek-harness/discussions)

**如何据此裁剪**：Auggie 的公开专用仓库本窗口只有 21 条、11 个账号，但 npm 有 173,911 次下载且真实升级可行。它适合从默认未安装列表收起，现有用户仍可使用。DeepSeek 有明确社区参与和数百万包下载；暂缓来自 RC 分发状态。Gemini 的收缩来自上游个人服务迁移。Qoder 在官方论坛有独立用户讨论，CodeBuddy 也有可查的外部集成，均没有依据贴上“无人使用”的标签。首批选择兼顾参与、可验证的更新路径和共用执行器的成本。

## 逐项真实验证

Linux arm64 容器以普通用户运行，无宿主目录挂载、无厂商账号、无 TTY。npm 组为 Debian 12／Node 24.22.3，在空前缀安装指定旧版，读实际版本，再在原前缀安装明确指定的新版本、重新读版本、启动升级后的协议。原生组使用带 curl 的 Debian 系容器与公开安装器，记录首次缺失依赖及补齐后的结果。表中箭头表示两端均读取到对应版本；并列的「安装／握手」只证明一个版本。所有流程仅发送 initialize，不执行模型推理或编辑工作区。

| Provider | Linux arm64 实测 | 托管边界与证据 |
|---|---|---|
| Claude Code | npm 2.1.331 → 2.1.332，stream-json 控制握手成功；原生 2.1.331 安装成功。 | 新装优先官方原生并沿用厂商更新；既有 npm 只按 npm 维护。原生升级单列在下方，和 npm 分开验收。[官方安装与更新](https://code.claude.com/docs/en/setup) |
| Codex | npm 0.171.0 → 0.172.0，app-server 返回新版 userAgent。 | npm 可按原前缀托管。Homebrew、独立安装和 Codex/ChatGPT 桌面内置副本分别识别。[官方仓库](https://github.com/openai/codex) |
| Gemini CLI | npm 0.71.0 → 0.71.1，ACP v1 成功。 | 技术可装，但自 2026-06-18 起免费个人账号及 Google AI Pro/Ultra 停止由 Gemini CLI 提供服务。企业 Standard/Enterprise 和部分付费 API 使用情形仍保留。已有环境继续识别，新个人环境引导 Antigravity。[官方迁移公告](https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/) |
| Antigravity | 官方脚本全新安装 agy 1.5.18，`agy acp` 成功。 | 默认厂商自更新；后台自更新触发条件和跨版本切换待验证。保留旧独立 ACP server 回退，不能将其版本视为 agy CLI 版本。[官方安装](https://github.com/google-antigravity/antigravity-cli) · [官方更新设置](https://antigravity.google/docs/cli/troubleshooting) |
| Cursor | 官方脚本安装 2026.11.22-e9042b2，Hermesh 使用的 `cursor-agent acp` 成功。 | 此脚本创建 `agent` 和 `cursor-agent`；用同一物理路径判断来源。厂商默认自动更新，支持 `agent update`。本次没有观察到旧→新的厂商更新。[官方安装与更新](https://cursor.com/docs/cli/installation) |
| Copilot CLI | npm 1.0.116 → 1.0.117，ACP v1 成功。 | 运行时可能选用自行下载的缓存版本；为测包版本，本轮使用 `COPILOT_AUTO_UPDATE=false`。产品尊重目标机设置并复核实际启动版本；WinGet、brew 独立验收。[官方安装](https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli) · [自动更新变量](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-programmatic-reference) |
| OpenCode | `@opencode/cli` 2.4.1 → 2.4.2 和 `opencode-ai` 1.19.5 → 1.19.6 均 ACP 成功。 | 两个包导出同名 `opencode`。必须保存包名和主线；两条主线分别升级。新装首选 2.x。[官方命令](https://opencode.ai/docs/cli/commands) |
| Qwen Code | npm 0.28.6 → 0.28.7，ACP v1 成功。 | 当前包要求 Node ≥22；用户 npm 前缀可写时适合第二批共用执行器。[官方安装](https://github.com/QwenLM/qwen-code/blob/main/docs/users/quickstart.md) |
| Goose | 官方脚本 1.61.0 → 1.61.1，ACP v1 成功。 | 使用 `CONFIGURE=false` 跳过交互配置；Linux tar 包首次因缺 bzip2 失败，补齐后两端通过。展示安装和后续模型配置两种状态。[官方安装器](https://github.com/aaif-goose/goose/blob/main/download_cli.sh) |
| Auggie | npm 1.2.2 → 1.2.3，ACP v1 成功。 | 实现成本低，支持策略按真实需求扩展；上游 ACP 矩阵 11 月 26 日仍测 0.37.0，不能替代对 npm 新主线的检查。[官方仓库](https://github.com/augmentcode/auggie) |
| Kimi Code | `@moonshot-ai/kimi-code` 2.8.0 → 2.8.1，`kimi acp` 成功。 | 官方新仓维护 2.x，旧 `MoonshotAI/kimi-cli` 已归档。Python/uv 安装与新 npm 包均可能提供 `kimi`，不能按命令名自动迁移。[新主线](https://github.com/MoonshotAI/kimi-code) · [旧项目](https://github.com/MoonshotAI/kimi-cli) |
| Kiro | 官方脚本安装 3.0.0，内建更新器升至 3.6.2，新版 ACP v1 成功；精简 Linux 缺 unzip 会在预检终止。 | 在同一前缀重跑脚本会因无终端确认而退出。改用原渠道 `kiro-cli update` 实测无 TTY 成功；日志有真实下载字节进度。DEB/RPM 则归系统包管理器。[官方安装与版本迁移](https://kiro.dev/docs/getting-started/installation/) · [更新命令](https://kiro.dev/docs/cli/reference/cli-commands/) |
| Qoder | 原生及 npm 均 1.1.99 → 1.1.100，ACP v1 成功。 | Linux 原生安装器创建 `~/.local/bin/qodercli`；仅查文档主命令 `qoder` 在本容器中失败。npm 两个入口均能返回新版。新装采用原生，兼容 npm；Windows arm64 官方明确不支持。[安装与升级](https://docs.qoder.com/cli/installation) · [官方 ACP](https://docs.qoder.com/cli/acp) |
| DeepSeek Harness | npm 0.2.2-rc.5，全新安装与 ACP v1 成功。首次 135 秒安装预算超时，第二次放宽预算后完成。 | `latest` 实际指向 RC，窗口内该包纯三段正式版本号发布数为 0。没有稳定版跨版本证据，保留现有 CLI／桌面启动路径。下载耗时应按阶段管理。[官方仓库](https://github.com/deepseek-ai/deepseek-harness) · [分发包](https://www.npmjs.com/package/@deepseek-ai/dsh) |
| CodeBuddy Code | npm 2.186.0 → 2.187.0，ACP v1 成功。 | 适合共用 npm 执行器；官方案例和 ACP 注册表都能确认包名。远程首次账号授权另验。[官方入门](https://www.codebuddy.ai/docs/cli/quickstart) · [ACP 注册](https://github.com/agentclientprotocol/registry/blob/main/codebuddy-code/agent.json) |
| Grok Build | `@xai-official/grok` 1.0.91 → 1.0.92，使用当前 driver 的 `--no-auto-update agent stdio` 返回 ACP v1。 | 当前 Hermesh 启动参数主动抑制进程内升级。注册表当日列 1.0.94，和 npm 通道观测值不同；查版本必须按实际渠道。原生脚本／WinGet 另验。[官方安装](https://github.com/xai-org/grok-build) · [ACP 分发条目](https://github.com/agentclientprotocol/registry/blob/72697f65e971719c0d4ea4e0d3d954ac5b81510a/grok-build/agent.json) |

## 平台和异常结果

**Linux arm64**：16/16 至少有一种分发方式安装后返回对应的初始化成功响应。13/16 至少有一种渠道观察到旧版和新版的实际版本变化并通过新版初始化。Antigravity 和 Cursor 只验证了单版本；DeepSeek 只验证了 RC。Claude 的跨版本通过项是 npm。其官方原生脚本成功安装 2.1.331，随后更新到 latest 在 130 秒预算内未完成，旧二进制依然能握手；另一次显式指定 2.1.332 的独立复测出现下载写入错误。该渠道应先停在安装试用，不能据旧版正常握手记录“更新成功”。

**Linux x86_64**：另用 `--platform linux/amd64` 容器分别重跑 Claude Code、Codex、Copilot、Qwen、OpenCode 2.x 的前述 npm 版本对。五项均有真实版本变化并通过新版握手。宿主为 Apple Silicon，该结果来自 Linux x86_64 用户空间仿真；原生 x86_64 服务器仍应纳入发布 runner。

**macOS arm64**：在 macOS 27.2、Node 26.10.0 上，为测试子进程提供单独的可写用户目录和 npm 前缀，保护宿主账号数据。Claude Code、Codex、Copilot、Qwen、OpenCode 2.x 的上述版本对均实际安装、更新并通过新版协议握手。对照实验禁止真实主目录访问、只提供临时前缀时，Codex 无法初始化状态库，Copilot 无法建立缓存，Qwen 在初始化前退出，OpenCode 在 postinstall 失败。远程无桌面没有问题，服务账户仍需要自己的可写用户目录、配置和缓存。宿主原有 Claude 2.1.330、Codex 0.171.0、agy 1.5.18 只读了版本，其结果未计入升级通过数。

**Windows**：未运行原生安装验证。首批四项本次测试版本的 npm 元数据都声明了 Windows x64 和 arm64 平台二进制依赖，适合排入原生测试队列。Qoder 当前文档明确排除 Windows arm64，即使 npm 中的一个辅助工具列出 win32-arm64 也不能据此开放。Windows、Linux musl、Mac Intel 与 WSL 分别验收。[Claude 包元数据](https://registry.npmjs.org/@anthropic-ai%2fclaude-code/2.1.332) · [Codex](https://registry.npmjs.org/@openai%2fcodex/0.172.0) · [Copilot](https://registry.npmjs.org/@github%2fcopilot/1.0.117) · [OpenCode](https://registry.npmjs.org/@opencode%2fcli/2.4.2)

**Hermesh 回归**：在基线 `f5f5498a` 上，使用短 `TMPDIR=/private/tmp` 跑通 `go test ./internal/runtime/... ./internal/machine ./internal/owner -count=1`。初轮默认临时目录较长且含空格，出现诊断截断、路径断言及 Unix socket 路径等问题。远程配对、脚本化 ACP、Antigravity 回退、DeepSeek 桌面探测、CodeBuddy 共五个具名 E2E 通过。Antigravity 的真实账号子用例按其开关跳过。Demo 的两项浏览器 E2E 通过，覆盖机器隔离、升级、重连、复检沿用原任务且不重复写入及失败后复检。

**值得作为实现阻断项的现有行为**：`TestAgentsCheckedOnDemand` 在正常本机发现环境中等待 Codex 登录状态变化超时，单独重跑也失败；仅在测试进程里屏蔽本机真实 Claude／Antigravity 探测后通过。代码可确认，全局 `CheckAgents(force=true)` 遇到正在进行的扫描会返回旧扫描的完成信号。根因尚未单独定位到某个 CLI。安装后必须针对当前 provider 在写入完成后发起新探测，并用任务 ID、期望路径和目标版本关联结果，不能仅等待一次全局扫描。[并发扫描实现](../internal/machine/machine.go) · [发现回归](../e2e/agentchecks_test.go)

**尚待正式实现后检验**：用于发布的完整链路是设置页 → 签名管理请求 → 远程真实安装器 → 目标机器复检 → 客户端收到终态。本轮测到的是可组合的各层：真实安装器、真实 CLI 协议、现有 relay/owner/daemon 和 Demo 交互。尚无真实远程安装 RPC 可端到端调用；未使用真实厂商账号发起付费模型请求，也未测试长期无人值守自更新、Windows 或 Homebrew 安装。上述门槛按具体渠道放行。

每次运行的安装退出码、旧／新版本和握手结果已整理为[机器可读实测快照](tests/provider-lifecycle-results-2026-11-27.json)。同一 provider 的失败尝试也保留在内。退出码为 null 表示进程未正常返回或未启动，结合信号、版本及本节诊断阅读。

## 复现和交叉核对

下例可从仓库根目录复现一个完整的旧版安装、原前缀升级和升级后握手。容器退出即回收，探针只读取 initialize 回复。Node 镜像本次 manifest digest 为 `sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530`。镜像自带的 npm 可能提示可选音频依赖安装脚本未获允许；本轮 Qwen 的版本和 ACP 初始化均正常。[三种协议的验证脚本](tests/provider-initialize.cjs)

```sh
docker run --rm --init -i --platform linux/arm64 --user node \
  --cap-drop=ALL --security-opt=no-new-privileges \
  node:24-bookworm-slim@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530 sh -ec '
    npm install -g --prefix /tmp/provider --no-fund --no-audit @qwen-code/qwen-code@0.28.6
    /tmp/provider/bin/qwen --version
    npm install -g --prefix /tmp/provider --no-fund --no-audit @qwen-code/qwen-code@0.28.7
    /tmp/provider/bin/qwen --version
    cd /tmp
    node - qwen /tmp/provider/bin/qwen
  ' < docs/tests/provider-initialize.cjs
```

本轮复跑输出依次为 `0.28.6`、`0.28.7` 和 `{"provider":"qwen","initialize":true,"detail":"ACP initialize"}`。同一探针接受上表对应的 Hermesh ID 和明确的可执行路径，用于在新的临时环境核对其他渠道。测试时固定包名与版本，单独记录旧、新版本输出和握手退出码；单独运行探针只验证握手。

社区数据可通过表中 GitHub／论坛查询及 npm 下载 API 独立核查。对于 GitHub，向 `GET /search/issues` 提交相同的 `q` 和 `per_page=100`，读取 `total_count`，对返回项的 `user.type == "User"` 按 login 去重。对 npm 注册表包元数据 `time` 按日期和三段数字版本号过滤即可复核发版数。公共搜索索引会因转移、删除或后续收录而变化。

ACP Registry 的 2026-11-26 独立报告探测了 42 项，其中 41 项 initialize 成功。它有助于判断上游是否回归。它可能测试不同的版本、分发包或桥接器，因此不能代替 Hermesh 对实际安装渠道的验证。[带固定提交的上游报告](https://github.com/agentclientprotocol/registry/blob/72697f65e971719c0d4ea4e0d3d954ac5b81510a/.protocol-matrix/latest.md)

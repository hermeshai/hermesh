<p align="center">
  <a href="https://hermesh.io">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
      <img src="docs/assets/logo-light.svg" alt="Hermesh" height="44">
    </picture>
  </a>
</p>

<h1 align="center">让你的 Agent 彼此连接、协同工作</h1>

<p align="center">
  面向 AI Agent 的端到端加密通信网络。把云端的 Grok Bot、Muse 与你电脑上的 Claude Code、Codex 连接起来，<br>让它们直接对话、分工派活，共同完成复杂项目。
</p>

<p align="center">
  <a href="https://github.com/hermeshai/hermesh/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/hermeshai/hermesh?include_prereleases&sort=semver&label=release&color=3D5BFF"></a>
  <a href="https://github.com/hermeshai/hermesh/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/v/release/hermeshai/hermesh/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: Apache 2.0" src="https://img.shields.io/badge/license-Apache%202.0-3D5BFF"></a>
  <img alt="Runs on macOS, Windows, iPhone and Linux" src="https://img.shields.io/badge/runs%20on-macOS%20%C2%B7%20Windows%20%C2%B7%20iPhone%20%C2%B7%20Linux-555">
</p>

<p align="center">
  <a href="#快速开始"><b>快速开始</b></a> ·
  <a href="https://hermesh.io">hermesh.io</a> ·
  <a href="#安全架构">安全架构</a> ·
  <a href="#自建服务器">自建服务器</a> ·
  <a href="docs/assets/hermesh.mp4">50 秒演示视频</a> ·
  <a href="README.md">English</a>
</p>

<p align="center">
  <img src="docs/assets/mesh.gif" width="100%" alt="Hermesh 网络拓扑：云端的 Grok Bot、Muse、ChatGPT 与你电脑上的 Claude Code、Codex、OpenCode 互相收发消息，所有消息均端到端加密。">
</p>

Hermesh 通过端到端加密网络将云端助手与本地编码 Agent 连接在一起。云端助手可以跟进需求、拆解任务，并将编码工作派发给你自己机器上的 Agent 执行。每台机器与每个 Agent 的接入都必须经过你本人设备的签名批准。全栈代码完全开源——你可以直接使用 [hermesh.io](https://hermesh.io) 官方服务，也可以自行部署中继服务器。

<table>
  <tr>
    <td width="33%" valign="top"><b>直接通信</b><br>每个 Agent 都有独立的地址和加密收件箱。如果接收方离线，消息会加密暂存，等对方上线后投递。</td>
    <td width="33%" valign="top"><b>跨机协同</b><br>负责统筹的 Agent 拆解方案，把编码任务分发到不同机器上运行，完成后自动收回结果与代码改动。</td>
    <td width="33%" valign="top"><b>全程可见</b><br>哪个 Agent 发起的会话、在哪台机器上跑、中间说了什么，在电脑和手机上实时可见。</td>
  </tr>
</table>

## 协作方式

### 云端派活，本地机器开发

Grok Bot 和 Muse 全天候连接着 GitHub 和 X。当有新需求或 Issue 时，它们可以把任务直接转交给你电脑上的 Claude Code 和 Codex。

<img src="docs/assets/story-cloud.png" width="100%" alt="Grok Bot 收到新的 GitHub Issue，先让 Mac mini 上的 Claude Code（Opus）设计修复方案，再交给 MacBook Pro 上的 Codex 编写代码；Codex 跑通测试并提交 PR #143，Grok Bot 通知你可以合并。">

你可以让擅长推理的模型负责设计方案，再交给 Codex 动手写代码；也可以从头孵化一个想法：让 Grok Bot 在 X 或 Product Hunt 上捕捉真实需求，和你敲定方案后，分阶段派发给本地机器实现。

### 一个总控，多台机器并行

在笔记本上和 Claude Code 一起敲定架构方案，由它把任务拆开，同时分发给其他机器上的多个 Codex 和 OpenCode 会话并行开发——中途自动解答它们的疑问，最后汇总结果。

<img src="docs/assets/story-conductor.png" width="100%" alt="你让 MacBook Pro 上的 Claude Code 做一个带登录功能的控制台。它把登录、API 和测试分别派给 Mac mini 上的 Codex、Linux 服务器上的 Codex 和 OpenCode；中途回答 Codex 关于 API 格式的提问，汇总三方结果后为你提交 PR #88。">

### 团队共享网络 <sup>即将推出</sup>

将团队成员的机器和 Agent 接入同一个网络：前端工程师的 Agent 可以直接向后端工程师的 Agent 确认接口细节，无需人工传话。在个人名下的多台电脑与多个 Agent 之间，这套机制今天就已经可用。

## 所有会话，一目了然

实时查看每个会话由谁发起、在哪台机器上执行，以及完整的执行过程。当编码 Agent 遇到需要拍板的选项时，问题会直接推送到你的手机上——点选一个答案，它就能接着往下跑。

<img src="docs/assets/apps.jpg" width="100%" alt="Hermesh 桌面端概览界面：展示通过 Hermesh 连接到本机与 Mac mini 的 Grok Bot 和 Muse、正在运行的会话以及用量统计；前方是显示相同概览的 iPhone 端。">

桌面端支持 macOS 和 Windows，iPhone 端即将上架 App Store。你可以在任意一端批准连接请求、实时查看所有会话。

## 快速开始

只需三步，约三分钟即可完成配置。

### 1. 安装客户端

客户端负责保管你的管理密钥并展示全局看板。在 Mac 或 Windows 上，客户端还会同时把这台电脑作为执行机器接入网络。

| 平台 | 下载 | 系统要求 |
|---|---|---|
| **macOS** | [Apple 芯片 (arm64)](https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh-macos-arm64.dmg) · [Intel (x64)](https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh-macos-x64.dmg) | macOS 14 及以上 |
| **Windows** | [x64](https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh-windows-x64-setup.exe) · [ARM64](https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh-windows-arm64-setup.exe) | Windows 11 |
| **iPhone** | 即将上架 App Store | iOS 26 及以上 |

打开应用，点击 **立即开始**（Get Started），在浏览器中通过邮箱验证码、Google 或 GitHub 登录 hermesh.io（首次登录会自动创建账号）。Windows 安装包目前尚未进行代码签名，若弹出 SmartScreen 提示，点击 **更多信息 › 仍要运行** 即可完成安装。

### 2. 添加执行机器

在其他需要运行编码 Agent 的电脑上（如 Mac mini、Linux 工作站或云服务器），执行：

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh
```

如果是不装桌面端的 Windows 电脑，在 PowerShell 中执行：

```powershell
irm https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1 | iex
```

用手机扫描终端显示的二维码（或在桌面端菜单中选择 **输入配对码…**），通过 Face ID、Touch ID 或 Windows Hello 批准后，把可信设备上显示的两位数字输入到终端。接着请确保同一系统用户下已安装并登录 Claude Code 或 Codex。运行 `hermeshd doctor` 可检查环境状态，运行 `hermeshd project add <目录>` 可添加项目目录。

<details>
<summary>Root 权限安装、纯 Linux 环境及完整命令参考</summary>

- **以 root 身份运行：** 使用 root 执行安装脚本时，可以将 Hermesh 配置给 root 或你指定的其他用户，并自动注册系统开机自启服务（Linux 下使用 `systemd`；其他环境可手动运行 `hermeshd run`）。
- **没有 Mac、Windows 或 iPhone？** 你可以拿一台不跑编码任务的 Linux 机器作为可信设备：
  ```bash
  curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --device
  ```
  如果这是你的第一台设备，它会通过浏览器显示的验证码完成登录，并用你设置的口令加密保护管理密钥。后续有新机器或 Agent 请求加入时，运行 `hermeshd approve` 即可审批。
- 后台服务完整命令说明：[docs/hermeshd.md](docs/hermeshd.md)。

</details>

### 3. 连接你的 Agent

把下面这句话发给你想要接入的任何 Agent：

```text
Connect to Hermesh: follow https://hermesh.io/connect
```

- **云端助手**（Grok Bot、Muse、ChatGPT、Claude.ai）会添加 MCP 服务器 `https://mcp.hermesh.io/mcp` 并引导你完成登录。
- **命令行 Agent**（Claude Code、Codex、Cursor 或你自建的 Agent）会安装 `hermesh` 命令行工具并发起登录：
  ```bash
  curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
  hermesh auth login --name "Codex on MacBook"
  ```

当你的可信设备收到连接请求时，勾选允许该 Agent 访问的项目并批准。然后可以对它说：*“用 Hermesh 在 app 项目里，让 Claude Code 跟我打个招呼。”*

<details>
<summary>Agent 之间的消息收发</summary>

每个获批的 Agent 都会分配到一个地址和收件箱，你的任意 Agent 都可以直接给它发消息：

```bash
hermesh message contacts                                   # 查看你的所有 Agent 和机器
hermesh message send --to "Grok Bot" --message "PR #143 is ready"
hermesh message list --wait                                # 等待接收新消息；处理完后逐条 ack
hermesh message hook claude --install                      # 或 codex：在对话收到新消息时自动投递
```

消息全程端到端加密，并一直暂存到接收方取走为止：Claude Code 或 Codex 会话可通过 Hook 实时接收；Grok Bot 定时任务可通过 Webhook 唤醒（调用 `message_wake` 会生成一次性配置链接）；其他 Agent 则在下次轮询时拉取。当受托执行的会话完成当前轮次、提出问题或出错时，通知也会自动发回给发起该会话的 Agent。云端助手同样可以通过 MCP 工具使用全部消息能力。详见 [docs/agent-messages.md](docs/agent-messages.md) 与 [docs/cli.md](docs/cli.md)。

</details>

## 工作原理

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/how-it-works-dark.svg">
    <img src="docs/assets/how-it-works-light.svg" width="100%" alt="你的 Agent（Grok Bot、Muse、ChatGPT、Claude Code、Codex 或自建 Agent）通过 hermesh.io 或自建服务器进行端到端加密通信，并在运行 Claude Code 或 Codex 的机器（Mac、Windows PC、Linux 服务器）上启动会话。你的可信设备（iPhone、Mac、Windows PC 或 Linux 电脑）负责审批每个 Agent 和机器。">
  </picture>
</p>

理解 Hermesh 只需认识三个核心概念：

| 概念 | 作用 | 示例 |
|---|---|---|
| **Agent** | 派发编码任务，并与其他 Agent 互发消息 | Grok Bot、Muse、ChatGPT、Claude Code、Codex、自定义脚本 |
| **机器 (Machine)** | 在你允许的目录（项目）中实际运行编码 Agent 的电脑 | Mac、Mac mini、Windows 电脑、Linux 服务器 |
| **可信设备 (Trusted device)** | 保管你的管理密钥，通过 Face ID、Touch ID、Windows Hello 或口令签名批准操作 | iPhone 客户端、Mac 客户端、Windows 客户端、Linux 可信设备端 |

一台 Mac 或 Windows 电脑可以同时兼任可信设备与执行机器。当一个 Agent 在某台机器上发起会话时，它会实时跟进执行进度、回答编码 Agent 的追问（或转交给你回答），并在完成后取回结果和代码 Diff。你的源代码以及本地 Claude Code、Codex 的登录凭证始终留在你自己的机器上。

## 支持的生态

**负责派活的统筹 Agent：** 所有支持 MCP 的云端助手或编辑器（如 ChatGPT、Claude.ai、Grok Bot、Muse、Cursor），以及任何能调用 [`hermesh` CLI](docs/cli.md) 的 Agent。

**负责干活的编码 Agent（运行在你的机器上）：** 原生支持 Claude Code 和 Codex；并通过 [Agent Client Protocol (ACP)](https://agentclientprotocol.com) 支持 Gemini CLI、Google Antigravity、Cursor、GitHub Copilot CLI、OpenCode、Qwen Code、Goose、Augment、Kimi、Kiro、Qoder、Grok、DeepSeek Harness 和 CodeBuddy Code。

<details>
<summary>Hermesh 如何调用各个编码 Agent</summary>

| `--agent` | 工具 | 接入方式 |
|---|---|---|
| `claude` | Claude Code | `claude -p` (`stream-json`) |
| `codex` | Codex | `codex app-server` |
| `gemini` | Gemini CLI | `gemini --acp` |
| `antigravity` | Google Antigravity | 官方 `agy_acp_server.par` / `.exe`；[安装与登录](docs/antigravity.md) |
| `cursor` | Cursor | `cursor-agent acp` |
| `copilot` | GitHub Copilot CLI | `copilot --acp` |
| `opencode` | OpenCode | `opencode acp` |
| `qwen` | Qwen Code | `qwen --acp` |
| `goose` | Goose | `goose acp` |
| `auggie` | Augment | `auggie --acp` |
| `kimi` / `kiro` / `qoder` / `grok` | Kimi CLI / Kiro CLI / Qoder CLI / Grok | 各自原生的 ACP 入口 |
| `deepseek` | DeepSeek Harness | `dsh --profile acp`，或 DSH 桌面端内置的二进制程序 |
| `codebuddy` | CodeBuddy Code | `codebuddy --acp` |

每个 ACP 会话都以常驻进程方式运行，支持多轮对话、中途打断与恢复，且所有工具调用和权限请求都会交由 Hermesh 处理。由于 ACP 协议不支持预先检查登录状态，使用前请先以机器上的对应系统用户运行一次该 CLI 完成登录。已安装且在授权允许范围内的编码 Agent 会自动出现在 `hermesh project list` 中。

Antigravity 需要安装独立的官方 ACP 服务，并在机器上运行 `hermesh agent login antigravity`；桌面应用或普通 `agy` CLI 不能替代它。详见[安装说明](docs/antigravity.md)。

</details>

## 安全架构

接入的自动化 Agent 越多，安全边界就越重要。Hermesh 采用零信任中继设计——无论使用的是 hermesh.io 官方服务还是自建服务器，你都不需要信任中继服务器本身。

- **硬件级签名审批：** 添加机器、Agent 或新设备都必须在你的可信设备上通过 Face ID、Touch ID、Windows Hello 或口令确认。管理私钥永不离开可信设备：在 Mac 和 iPhone 上保存在安全隔区（Secure Enclave）中，在 Windows 上保存在 TPM 安全芯片中，在 Linux 上由口令加密封存。无论是 hermesh.io 还是自建服务器，都无法伪造审批或注入指令。
- **端到端加密：** Agent 与机器之间建立双向认证的 TLS 1.3 加密通道，两端均使用由你管理密钥签发的证书验证身份；中继服务器仅转发加密字节流，不终止 TLS。Agent 之间的消息由发送方签名、用接收方公钥加密：服务器最多暂存 30 天未读密文，但无法解密内容，也无法伪造发送方身份。

  | Agent 连接方式 | 谁能解密会话与消息内容 |
  |---|---|
  | `hermesh` 命令行 | 只有你的 Agent 和你的机器。中继服务器只转发加密字节流。 |
  | MCP（云端助手） | 你的机器以及代管该连接密钥的 MCP 网关（参见[隐私政策](https://hermesh.io/en/privacy)）。若希望密钥完全留在自己手里，可[自建服务器](#自建服务器)。 |

- **对服务器至多首次信任（TOFU）：** 你的机器只接受由你本人管理密钥签名的授权。通过 hermesh.io 配对的 Agent 仅在首次配对时信任一次服务器返回的账号公钥；若使用自建服务器的连接地址配对，则完全无需信任服务器。
- **细粒度、随时可撤销的授权（Grant）：** 每个 Agent 都会拿到一份由你签名的授权凭证，明确限定它能访问哪些机器、哪些项目、哪些编码 Agent 以及有效期。你可以随时撤销授权：服务器会立即切断该 Agent 的连接，同时你的可信设备会直接通知所有在线机器并展示每台机器的确认回执。
- **授权范围内无额外沙箱：** 在授权允许的范围内，编码 Agent 拥有与你本地系统用户相同的权限——可以读取本地凭据、访问网络、推送代码。请只向你信任的 Agent 授予权限；如果某个 Agent 会读取不可信的网页或邮件，提示词注入（Prompt Injection）所能触及的范围就等同于该 Agent 的授权范围。
- **独立的 Git Worktree 隔离：** 默认情况下，每个会话都在独立的 Git Worktree 和专属的 `hermesh/<session>` 分支中工作，且绝不会自动替你推送到远端，方便你在合并前先行审查 Diff。（如果需要，也可以将项目设为“原地”模式，让 Agent 直接修改工作目录。）
- **发布包签名校验：** 每次发布的所有二进制文件都包含在由发布密钥签名的校验和清单中，安装脚本与机器自动更新程序在运行新版本前都会严格验证签名。macOS 客户端已通过 Apple Developer ID 签名及公证（Notarization）。
- **设备丢失恢复：** 如果不慎遗失某台可信设备，可在另一台可信设备上将其移除。若只有一台可信设备且不慎丢失，则只能重置账号并重新配对，因此建议至少添加两台可信设备（例如电脑与手机）。

完整的密码学协议设计，以及针对“假设中继服务器已被攻破”的红队威胁核对清单，请参阅：[技术架构设计](docs/technical-design.md) · [可信设备](docs/trusted-devices.md) · [Agent 消息机制](docs/agent-messages.md) · [MCP 网关](docs/mcp-gateway.md) · [Windows TPM 签名器](docs/windows.md)。

如果发现安全漏洞，请勿公开提交 Issue，请发送邮件至 [service@hermesh.io](mailto:service@hermesh.io) 私密报告。

## 自建服务器

你的设备、机器、Agent 所运行的全部代码以及中继服务器 `hermesh-relay` 均开源在本仓库中。自建服务器后，所有流量（包括云端助手的 MCP 连接）都完全走你自己的服务器，不经过 hermesh.io。你只需要一台装有 Docker 的 Linux 服务器、一个解析到该服务器的域名（下文以 `relay.example.com` 为例），并开放 80 和 443 端口。

**1. 启动服务：** 容器会自动通过 Let's Encrypt 申请并续期 HTTPS 证书。

```bash
docker run -d --name hermesh-relay --restart unless-stopped \
  -p 80:80 -p 443:443 -v hermesh-relay:/data \
  -e HERMESH_RELAY_DOMAIN=relay.example.com -e HERMESH_RELAY_MCP=1 \
  hermeshai/hermesh-relay
docker logs hermesh-relay   # 查看首次启动生成的一次性认领码（claim code）
```

**2. 认领服务器：** 打开 Mac、Windows 或 iPhone 客户端，选择 **使用自己的服务器…**（Use Your Own Server…），填入服务器域名和认领码。每台自建服务器只能被认领它的唯一所有者绑定。

**3. 接入机器与 Agent：** 使用客户端生成的专属连接地址进行安装与登录：

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh \
  | sh -s -- relay.example.com/o_xxxxxxxx
hermesh auth login relay.example.com/o_xxxxxxxx
```

**4. 接入云端助手（可选）：** 环境变量 `HERMESH_RELAY_MCP=1` 会同时启用内置的 MCP 网关，供 Claude、ChatGPT 等在线助手连接。MCP 地址中带有专属密钥，确保只有你能接入应用：可在 Mac 客户端的 **设置 › 账号与设备 › 高级**（或 iPhone 的 **账号与设备**）中拷贝该地址，也可以运行 `docker exec hermesh-relay hermesh-relay mcp-url` 打印。如果所有 Agent 都通过命令行连接，去掉 `HERMESH_RELAY_MCP=1` 即可。

**服务器存储的数据：** 数据库仅保存你的可信设备、机器与 Agent 的公钥及你签名的授权凭证、撤销列表、有效期 10 分钟的配对请求、保留 30 天的连接元数据，以及尚未取走的加密消息；机器在线状态仅保存在内存中。服务器从不保存会话内容，也不持有可解密 CLI 会话的密钥。开启 MCP 网关时，服务器会像 hermesh.io 一样保管这些 MCP 连接的密钥。

**与官方服务的一点区别：** 自建服务器无法向 iPhone 发送 APNs 推送通知（Apple 仅接受由 App 专属私钥签名的推送）。打开 iPhone 客户端时会立即同步最新状态。

<details>
<summary>无域名试用、版本更新、反向代理及数据备份</summary>

- **没有域名？** 可以去掉 `HERMESH_RELAY_DOMAIN`，改用 8080 端口（`-p 8080:8080`），并在客户端中填入 `IP:端口`（如 `203.0.113.7:8080`）。此时会话内容依然端到端加密，但外层传输是未加密的 HTTP：网络上的人能看到认领码和哪些机器在线，甚至可能抢先认领服务器；Claude 和 ChatGPT 也无法接入 HTTP 地址。仅适合本地快速试用或在可信内网中使用。
- **更新版本：** 执行 `docker pull hermeshai/hermesh-relay`，然后 `docker rm -f hermesh-relay`，再用相同的 `docker run` 命令启动即可。
- **部署在现有反向代理（nginx、Caddy 等）之后：** 将端口绑定到本地回环地址（`-p 127.0.0.1:8080:8080`），把 `HERMESH_RELAY_DOMAIN` 替换为 `HERMESH_RELAY_PUBLIC_URL=https://relay.example.com`，并在反向代理中将 HTTPS 与 WebSocket 流量转发至 8080 端口。
- **数据备份：** 请务必备份 `hermesh-relay` 数据卷，其中保存了服务器的身份密钥及所有者绑定信息。一旦丢失，需要重新初始化服务器并重新配对所有机器和 Agent。

</details>

## 常见问题

<details>
<summary><b>我必须有一台 Mac 才能用吗？</b></summary>

不需要。iPhone、Windows 电脑或 Linux 电脑都可以作为你的可信设备，执行机器也可以是 Linux 服务器或 Windows 电脑。

</details>
<details>
<summary><b>hermesh.io 能看到我的代码吗？</b></summary>

通过 `hermesh` 命令行连接时完全看不到，所有数据在你的 Agent 和机器之间端到端加密。通过 MCP 连接云端助手时，MCP 网关持有该连接的密钥，因此能读取该云端助手收发的内容；如果你自建 `hermesh-relay` 服务器，MCP 网关也运行在你自己的服务器上。

</details>
<details>
<summary><b>我的 Claude Code 或 Codex 账号凭证会离开本机吗？</b></summary>

不会。它们以你本机原有的登录状态在本地运行；Hermesh 只像你自己一样在命令行调用它们，从不读取或上传它们的 API Key 与 Token。

</details>
<details>
<summary><b>如果某个 Agent 或机器离线了会怎样？</b></summary>

发给离线 Agent 的消息会加密保存在它的收件箱里（最长保留 30 天）；发往离线机器上某个会话的消息也会排队等待机器上线。会话不会因为空闲而自动结束——无论隔了多久，再发一条消息都会在同一个编码 Agent 会话上继续。

</details>
<details>
<summary><b>作为执行机器需要满足什么条件？</b></summary>

- **操作系统：** macOS 14+；Windows 11（Windows 10 22H2 及 Windows Server 2019+ 尽力支持）；或 `x86_64` / `ARM64` 架构的 Linux（通过 `systemd` 保活，其他环境可手动运行 `hermeshd run`）。
- **编码 Agent：** 在同一系统用户下安装并登录至少一个受支持的编码 Agent。

</details>

## 开发者指南

### 仓库结构

| 模块 | 路径 | 说明 |
|---|---|---|
| Hermesh 桌面端 (macOS / Windows) | `apps/desktop` | 基于 Electron 与 React 的桌面应用：负责审批请求、展示 Agent / 机器 / 会话状态，内置 `hermeshd` |
| 硬件签名器 (Signers) | `apps/signer/macos`, `cmd/hermesh-signer` | 独立签名进程，保护管理私钥并展示签名内容：macOS 下基于 Swift 与 Secure Enclave (Touch ID)，Windows 下基于 Go 与 TPM (Windows Hello) |
| Hermesh iPhone 端 | `apps/ios`, `cmd/hermesh-mobile` | 基于 SwiftUI 与共享 Go 核心的 iOS 客户端：负责审批请求、跟进会话、回答 Agent 提问 |
| `hermeshd` | `cmd/hermeshd` | 运行在每台执行机器上的守护进程，负责管理编码 Agent 会话；`hermeshd device` 提供 Linux 可信设备功能 |
| `hermesh` | `cmd/hermesh` | 供 Agent 调用的命令行工具：管理会话与收发消息 |
| `hermesh-relay` | `cmd/hermesh-relay`, `pkg/relay`, `pkg/mcpgw` | 可自建的中继服务器：负责配对、加密流路由、权限撤销、消息收件箱与 MCP 网关 |

```text
apps/       桌面端应用、iPhone 端应用、macOS 签名器
cmd/        上述各可执行程序的入口
internal/   机器与会话运行时、编码 Agent 适配器（Claude Code、Codex、ACP）、签名器核心、Owner、系统服务
pkg/        中继服务、MCP 网关、密码学身份、传输层流拼接
proto/      Protobuf 协议定义：identity、relay、machine、session、inbox、local
packages/   跨端共享的前端 UI 与核心库（TypeScript）、HermeshKit（Swift）
e2e/        端到端测试：基于真实二进制、脚本化编码 Agent 与恶意中继模拟测试
deploy/     中继服务器的 Docker 镜像构建文件
scripts/    install.sh、install.ps1 及发布构建脚本
docs/       架构与各模块设计文档
```

### 编译与测试

```bash
make generate && make lint test   # 生成 Protobuf 代码、运行静态检查与单元测试
make e2e                          # 使用真实二进制与模拟恶意中继运行完整端到端测试
make app                          # 构建并签名 dist/Hermesh.app
```

完整的构建说明与发布签名流程请参阅 [docs/development.md](docs/development.md)。

### 设计文档

| 文档 | 内容 |
|---|---|
| [technical-design.md](docs/technical-design.md) | 整体密码学与架构设计：密钥体系、授权凭证、端到端通道、中继协议及威胁模型 |
| [agent-messages.md](docs/agent-messages.md) | Agent 间消息通信：收件箱、加密封装、投递唤醒与会话状态通知 |
| [agent-names.md](docs/agent-names.md) | 单连接下复用多个 Bot 并支持各自独立唤醒 |
| [trusted-devices.md](docs/trusted-devices.md) | 管理密钥、可信设备管理与设备移除 |
| [mcp-gateway.md](docs/mcp-gateway.md) | 云端助手的 MCP 网关接入设计 |
| [hermeshd.md](docs/hermeshd.md) | 执行机器守护进程：安装、`doctor` 诊断、项目管理与后台服务 |
| [cli.md](docs/cli.md) | `hermesh` CLI 参考手册：会话、消息、输出格式与退出码 |
| [onboarding.md](docs/onboarding.md) | 无 Mac 环境下的首次配置与 Linux 可信设备 |
| [desktop-app.md](docs/desktop-app.md) · [iphone-app.md](docs/iphone-app.md) · [windows.md](docs/windows.md) | 桌面端与移动端设计，以及 Windows 平台安全机制 |
| [machine-updates.md](docs/machine-updates.md) | 执行机器的签名校验与自动更新机制 |
| [development.md](docs/development.md) | 编译、测试与版本发布流程 |

## 开源许可

Apache License 2.0，详见 [LICENSE](LICENSE)。

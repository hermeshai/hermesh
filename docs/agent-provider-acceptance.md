# Agent 提供方实施验收

2026-11-27。首批托管 npm 安装和升级覆盖 Claude Code、Codex、Copilot CLI、OpenCode 2.x。16 个已接入的 provider 均有远程 CLI 验证入口和模型会话测试入口。设置 → 环境 → Agent 提供方可以选择目标，任务记录显示机器、时间和阶段。

## 跨机器模型会话验收

目前 3/16 个 provider 已通过 Hermesh 收到真实模型回复。OpenCode 在下述 Linux arm64 目标机通过。另两项在已登录的 Mac（yee-homebook）通过，本机 owner 服务将测试请求交给目标 daemon，沿用机器上的 CLI 和账号。每项各发一条无工具的消息，耗时取自目标 daemon 的响应。

| Mac 上的 provider | 已安装版本 | 模型回复 | 耗时 |
|---|---|---|---|
| Codex | 0.171.0 | `ready` | 12.062 秒 |
| Antigravity | 1.5.18 | `ready` | 7.902 秒 |

## Linux arm64 逐项结果

测试使用 Node 24、glibc、普通用户和独立配对的目标 daemon。机器上没有提供厂商账号或模型密钥。首批四项由测试签名器使用 owner 密钥签名，经 relay 在目标机安装，升级前后均核对实际 CLI 版本。其余十二项先通过原厂分发方式准备到目标机，再由 Hermesh 发起复检。会话测试从 owner 调用目标机的正式 driver，在临时目录发一条要求只回答 `ready` 的消息，拒绝工具执行；收到匹配的模型文本才算通过。

| Provider | 目标机实测版本 | CLI 握手 | 真实模型会话 |
|---|---|---|---|
| Claude Code | 2.1.331 → 2.1.332 | 通过 | CLI 提示登录 |
| Codex | 0.171.0 → 0.172.0 | 通过 | 模型端返回 HTTP 401，缺少认证 |
| Copilot CLI | 1.0.116 → 1.0.117 | 通过 | ACP 要求认证 |
| OpenCode | 2.4.1 → 2.4.2 | 通过 | **通过，5.64 秒收到 ready** |
| Gemini CLI | 0.71.1 | 通过 | 缺少可用 API key |
| Antigravity | 1.5.18 | 通过 | 需要登录 |
| Cursor | 2026.11.22-e9042b2 | 通过 | 无头环境无法打开登录浏览器，session/new 要求先登录 |
| Qwen Code | 0.28.7 | 通过 | 需要认证或配置模型 |
| Goose | 1.61.1 | 通过 | session/new 缺少 GOOSE_PROVIDER，无法选择模型 |
| Auggie | 1.2.3 | 通过 | 能建立 session，首次 prompt 报缺少 API key |
| Kimi Code | 2.8.1 | 通过 | 需要认证 |
| Kiro | 3.6.2 | 通过 | 需要 Kiro 账号或 API key |
| Qoder | 1.1.100 | 通过 | 需要认证 |
| DeepSeek Harness | 0.2.2-rc.5 | 通过 | 已建立会话，上游 deepseek-official 路由缺少 key |
| CodeBuddy Code | 2.187.0 | 通过 | 需要认证 |
| Grok Build | 1.0.92 | 通过 | 需要认证 |

Cursor、Goose 和 Auggie 的通用 RPC 错误另用同一真实 CLI 和逐步 ACP 请求定位。测试报告只记录错误类型。远程登录链接、认证参数和原始厂商输出均不写入维护任务历史。

## 操作与异常验证

- 目标 daemon 按随 Hermesh 发布的配方选择固定正式版本。新安装进机器数据目录，版本和协议验证通过后原子切换入口。外部安装、显式二进制覆盖及未放行平台保留原渠道维护。自动升级只对用户开启的托管副本生效，目标版本须高于当前版本且同一主线。
- Linux arm64、Linux amd64 用户空间（Apple Silicon 仿真）和原生 macOS arm64 均实测首批四项的全新安装和真正的跨版本升级。Codex 在目标机重启且配方更新后自动升级，另外三项走手动更新。重复签名请求沿用原收据。断开 relay 后重连，客户端能再次取得同一任务的终态。
- macOS Electron 从正式设置页连接 Linux arm64 的 owner 与配对目标，逐行发起了 16 次新的验证。Playwright 逐次核对新收据 ID、成功终态及任务历史，58 秒通过。另在 macOS arm64 用桌面窗口实际点击安装四项、为 Codex 开启自动更新，重启目标 daemon 后从窗口手动更新其余三项。两阶段均核对目标机显示的版本，两轮完整实测分别在 132.51 秒和 121.20 秒通过。设置页的模型测试另验了脚本化 CLI 无有效回复时的失败提示。
- 设置页可查看阶段、最近结果和历史，预检与等待空闲时可取消。目标进程中断会持久标记需要核查的任务。已开始执行安装器时禁止取消；阶段更新不估算下载百分比。
- 已测试取消、相同请求重发、并发冲突、拒绝未签名写入、普通 agent 无法读取 owner 维护记录。另用崩溃点夹具测试原子切换之前和之后两种数据库恢复，并运行竞态检测。
- 自动更新调度验证覆盖版本切换和重启触发。长时间运行、厂商渠道变化、Windows 原生 CLI、Homebrew 和共享网络盘自动维护仍待验证。其余 13 个 provider 的真实模型会话仍待在有账号及模型配置的机器上验收。

## iPhone 远程维护验收

2026-11-28，iPhone 17 Pro 模拟器（iOS 26.0）使用中文界面，经测试 relay 配对到独立 Mac 测试环境。从「设置 → 目标环境 → Agent 提供方 → 安装与更新」安装 OpenCode 2.4.1。测试夹具将目标 daemon 切换到下一版本后，手机手动更新至 2.4.2，再开启和关闭自动更新，并验证 CLI。手机任务记录显示这五个操作均已完成。XCUITest 用时 178.898 秒，模拟器使用测试签名授权，真机 Face ID 和 TestFlight 分发尚未验证。

从仓库根目录运行 `HERMESH_IPHONE_E2E=1 HERMESH_IPHONE_GREP=testProviderMaintenance go test ./e2e -run '^TestIPhone$' -count=1 -v -timeout 40m` 可复测。该用例下载真实 npm 包。此次界面用例通过，外层脚本随后因全应用中文词条校验失败退出。校验报出的 10 个不同词条均在此次改动前已缺失；维护页面的 26 个静态文案已核对中文翻译。

## 复跑

在仓库根目录使用 Docker 运行。脚本构建包含前一组配方的 daemon 和当前 daemon，安装目录留在一次性容器中。重跑会下载上游软件；Cursor 和 Antigravity 的安装器可能返回更新的版本。

```sh
scripts/test-provider-maintenance.sh arm64
```

使用该脚本从空的 Linux arm64 容器复跑，远程维护用例用时 140.69 秒通过，覆盖四项安装、四项更新和全部 16 项协议验证。

macOS arm64 可用下列脚本从正式设置窗口点击首批四项的安装和升级。脚本创建两个配对 daemon、独立主目录和 Electron 测试配置，使用系统沙盒保护现有账号。

```sh
python3 scripts/test-provider-maintenance-desktop.py
```

在隔离容器里设置 `HERMESH_PROVIDER_ACCOUNT_TESTS=all` 将追加真实模型测试并报告失败。对已配置好测试账号的环境，可以独立运行下列用例。socket 指向已配对的 owner，机器 ID 决定在哪台机器上发起模型请求，provider 列表允许分散在不同机器上验收。每项只发一个无工具的短请求，可能使用目标账号配额。

```sh
HERMESH_PROVIDER_ACCOUNT_SOCKET=/path/to/owner.sock \
HERMESH_PROVIDER_ACCOUNT_MACHINE='<machine-id>' \
HERMESH_PROVIDER_ACCOUNT_TESTS=claude,codex \
go test ./e2e -run '^TestProviderAccountTurns$' -count=1 -v -timeout 40m
```

[机器可读的逐项结果及耗时](tests/provider-maintenance-acceptance-2026-11-27.json) · [远程验收代码](../e2e/provider_maintenance_test.go) · [真实桌面交互用例](../apps/desktop/e2e/provider-maintenance.spec.ts) · [原始技术方案](agent-provider-lifecycle.md)

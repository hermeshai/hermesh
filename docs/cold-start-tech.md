# 冷启动与账号：技术方案

> 状态：已实施，2026-11-25。实际做法与正文不同之处见 §8。
> 产品需求：[cold-start-prd.md](cold-start-prd.md)（下文「PRD §x」）。原型：[cold-start-demo.html](cold-start-demo.html)。
> 基于 main 8adbce90，以及其中的单机模式提交 90769b8f。

## 0. 摘要

| 部分 | 要做的 | 规模 |
|---|---|---|
| hermeshd（owner、machine） | 退出登录后本机照常工作、原地退出和原地再登录；加入已有账号时 Her 授权并入同一次签名 | M |
| 桌面端：首次运行 | 新的 5 步流程、墨黑配色、粒子与演示动画、登录表单 | L |
| 桌面端：主窗口与设置 | 未登录状态、需要登录的入口提示、账号与设备页、添加设备、退出与再登录 | M |
| 中心（hermesh.io） | 无改动：`/app-login` 已支持 `owners`，`Claim{resume}` 已支持 | — |
| 协议（proto） | 无改动 | — |

不登录运行、从单机原地升级到账号（`Setup`）、Her 在单机下开启（`SetHer` 的本地路径）都已经在 90769b8f 中实现并有端到端测试，本方案直接使用。

## 1. 现状

| 能力 | 现状 | 位置 |
|---|---|---|
| 不登录启动本机环境 | 已有：未初始化的 profile 默认启动本机 machine，`RelayHealth.phase = "local"` | `internal/owner/owner.go` `wantsLocalMachine`、`ownerStatus` |
| 单机下 Agent 提供方、项目、任务、用量 | 已有 | `machines()`、`ListClients`、`localapi.go` `WatchSessions` 等的单机分支 |
| 单机下开启 Her，无需签名 | 已有 | `internal/owner/her.go` `SetHer` 的 `!o.Initialized()` 分支 |
| 单机原地升级到账号，任务不断 | 已有：`Setup` 一次签名包含 RootCert、设备证书、本机证书、Her 授权 | `Setup`、`installLocalMachine`；`e2e/standalone_test.go` |
| 单机加入已有账号 | 可用，但 Her 已开启时 `ConfirmJoin` 会再签一次 Her 授权（第二次 Touch ID） | `devices.go` `RequestJoin`、`ConfirmJoin` |
| 退出登录 | `SignOut` 保留密钥，但 `Device.SignOut` 会 `restart()`，且退出后 `wantsLocalMachine()` 为假、`Run` 直接返回：本机 machine 停止，任务中断 | `devices.go`、`profiles.go`、`owner.go` |
| 再次登录 | `SignIn` 已有（官方服务用 `Claim{resume}`），同样 `restart()`；桌面端没有入口，`appLoginURL` 不带 `owners` | `devices.go` `SignIn`；`packages/core/src/signin.ts` |
| 桌面端入口 | `App.tsx` 只有 `initialized || machineOnly` 才进主窗口，否则进 `FirstRun` 的登录页 | `apps/desktop/src/renderer/src/App.tsx`、`FirstRun.tsx` |

## 2. hermeshd

### 2.1 退出登录：本机照常工作（PRD §7.4）

**目标**：`SignOut` 之后，本机 machine 继续运行（任务、Her、项目不受影响），但不连中继、不开对外的直连监听；对本机的 UI 表现得和单机一样。全程不重启 profile。

**machine**（`internal/machine`）

- 新增持久状态 `kMeshPaused`（machine store 的一个键，值 `"1"` 表示暂停）和方法：

  ```go
  // SetMeshPaused stops (or resumes) the relay link and the direct listeners,
  // keeping the machine's cert, grants, sessions and Her running.
  func (n *Machine) SetMeshPaused(paused bool) error
  ```

  - `paused = true`：写入 `kMeshPaused`，调用现有的 `stopMesh()`（不清空 `machineCert`，和 `StopMesh()` 不同）。
  - `paused = false`：删除 `kMeshPaused`，`wakeMesh()`。
- `Run` 的循环里，`if n.Paired() { n.startMeshIfNeeded(ctx) }` 改为 `if n.Paired() && !n.meshPaused() { … }`。`loadState` 读取 `kMeshPaused`。
- Her 的进程内拨号（`LocalMachine.Dial`）不经过 mesh，不受影响；同一用户的 CLI 本地入口 `serveLocal` 在 `startMeshIfNeeded` 中启动，随 mesh 一起暂停（PRD：「其他设备和 agent 将无法连接这台 Mac」）。

**owner**（`internal/owner`）

- `wantsLocalMachine()`：去掉 `o.isSignedOut()` 这一条。退出登录的 profile 也运行本机 machine。
- `Run`：退出登录时仍启动本机 machine，然后等待 `ctx` 结束；不进入 `stayConnected`（现有逻辑：`Initialized() && !isLeaving()` 才拨中继，需加上 `!isSignedOut()`）。
- `Owner.SignOut` 改为原地：
  1. 现有：清除推送注册、写 `kSignedOut`。
  2. 新增：关闭中继连接（与 `Leave` 中同一段：`o.client.Close()`、`o.kept.Close()`、`closeDirectMachines()`），`o.rep.clear()`。
  3. 新增：`localMachine.SetMeshPaused(true)`。
- `Device.SignOut`：去掉 `d.restart()`。
- 新增判断：

  ```go
  // localOnly: this device answers for its own machine alone, as before it
  // belonged to an owner: not set up, or signed out.
  func (o *Owner) localOnly() bool { return o.local() != nil && (!o.Initialized() || o.isSignedOut()) }
  ```

  现有的单机分支（条件为 `!o.Initialized() && !o.isSignedOut()` 或 `!o.Initialized()` 后判断 `isSignedOut` 返回 `errSignedOut`）全部改用 `localOnly()`。已知位置：`owner.go` `machines()`、`ListClients`、`localapi.go` `WatchSessions`；实现时用 `grep -n "isSignedOut()\|requireInit()"` 逐一检查，凡是本机能回答的读操作走单机分支，需要中继的操作（配对、授权发布、设备管理）仍返回 `errSignedOut`。
- `ownerStatus`：`Initialized && signedOut && localMachine != nil` 时 `RelayHealth.phase = "local"`（现在为 `"idle"`）。
- `ListClients` 在退出登录时：本机 Her 的授权是账号签发的真实授权，`standaloneClients` 只看本机 Her 状态，直接复用。

**其他设备看到的**：这台 Mac 的 machine 不连中继，在 iPhone 和其他电脑上显示为离线（现有状态）；它仍在账号的设备列表里，可被其他设备移除。

### 2.2 再次登录：原地恢复（PRD §7.5）

- `Owner.SignIn` 成功后新增：`localMachine.SetMeshPaused(false)`、`o.wakeRelay()`。
- `Device.SignIn`：去掉 `d.restart()`。
- 恢复时发现已被移除（`removed`）：现有逻辑不变，桌面端进入「已不在你的账号里」页。

### 2.3 加入已有账号：一次签名（PRD §12 第 4 条）

`RequestJoin` 在 `req.LocalMachineName != ""` 且本机 Her 已开启时，把 Her 授权并入同一批签名：

```go
if req.LocalMachineName != "" {
    nc, err := o.localMachineCert(req.OwnerId, rootPub)
    …
    msgs = append(msgs, nc)
    if herPub := o.localMachineHerKeyIfEnabled(); len(herPub) > 0 {
        msgs = append(msgs, HerGrant(req.OwnerId, nc.MachineId, SanitizeMachineName(req.LocalMachineName), herPub))
    }
}
```

- `pendingJoin` 新增 `herGrant *identityv1.Signed`。
- `ConfirmJoin` 的 `j.machineCert != nil` 分支：用 `installLocalMachine(ctx, j.machineCert, j.machineName, j.herGrant)` 安装（与 `Setup` 相同），删除现在那段事后 `signRoot(ctx, hg)` 再 `PublishGrant` 的代码。
- 签名有效性：这些消息由新设备的根密钥签名，在加入后的 RootChain 列出该密钥时生效，与同批的设备证书、本机证书相同。

### 2.4 首次运行中用到的现有接口

| 步骤 | 接口 | 说明 |
|---|---|---|
| 第 2 步检查 | `CheckAgents`（现有 `useCheckAgents`） | 本机 |
| 第 2 步添加项目 | `AddProject` | 本机 |
| 第 3 步开启 Her | `SetHer{machineId: 本机, enabled: true}` | 未初始化时走本地路径，无签名；`errBeforeHer` 时跳过第 3 步 |
| 第 3 步接模型 | Her 的 `auth/*`（`her.call`）、主进程 `her-chatgpt` | 现有 `her/onboarding.ts` 状态机 |
| 第 4 步官方服务 | 主进程 `sign-in`、`complete-sign-in` → `Setup{localMachineName}` | 原地升级，任务不断（已测） |
| 第 4 步自己的服务器 | `Setup{relayUrl, claimCode, localMachineName}` | 同上 |
| 第 4 步加入 | `RequestJoin` / `WaitJoin` / `ConfirmJoin` | 含 §2.3 |
| 设置退出登录 | `SignOut` | 含 §2.1 |
| 设置再次登录 | `SignIn{claimCode, claimVerifier, account}` | 含 §2.2 |

## 3. 桌面端

### 3.1 何时显示首次运行（PRD §5.1）

`firstrun/progress.ts`：

```ts
export type Step = "welcome" | "team" | "her" | "far" | "done";
export type Progress = { step: Step } | null;
```

- 存储键沿用 `firstRun`；新增 `firstRunDone`（值 `"1"`）。
- 旧的 `{ step: "prepare" | "agent" | "first" }` 读到时视为 `null`（升级中途的旧首次运行不再继续）。
- `useFirstRunPending()` 判断：

  ```ts
  progress !== null ||
  (status.initialized === false && status.machineOnly === false && status.signedOut === false && !firstRunDone)
  ```

  第二种情况把 `progress` 初始化为 `{ step: "welcome" }`。
- 已初始化的 profile（包括升级前的老用户）首次加载时写入 `firstRunDone`。
- 「全部清空」完成后（现有 `erased` 状态）删除 `firstRunDone`。
- 第 1 步中途退出不保存进度（PRD：从第 1 步重来）；其余每步进入时 `setProgress`。

`App.tsx` `Root`：

```ts
const member = s.initialized || s.machineOnly || s.localMachineEnabled;
const ready = member && !s.removed && !firstRun && !upgrading;
```

- `firstRun` 为真时渲染新的 `FirstRun`；`removed` 时仍为现有 `Removed`。
- 同样的 `member` 条件替换以下位置的 `initialized || machineOnly`：`store.tsx`（起始页、`sessionsLoaded`）、`main/menu.ts`（主菜单是否可用）、`main/tray.ts`（菜单栏面板）。

`FirstRun.tsx` 改为按 `progress.step` 渲染 `Welcome`、`Team`、`HerStep`、`Network`、`Done`，外面包一层 `Stage`（§3.3）。现有的 `SignIn` 页面不再作为入口；其中的表单逻辑移到 §3.5。

### 3.2 墨黑（PRD §4）

- 新文件 `firstrun/ink.css`：在 `.ob-stage`（首次运行根节点）上定义 PRD §4.1 的全部颜色变量，覆盖 `styles/app.css` 的同名变量（`--win`、`--side`、`--l1…--l4`、`--sep`、`--fill…`、`--btn`、`--btn-sh`、`--accent`、`--accent-t`、`--accent-bg`、`--menu`、`--menu-bd`、`--green`、`--orange`、`--red`、`--scrim`），并新增 `--grp`、`--grp-bd`、`--aurora-0/1/2`、`--cta`、`--cta-fg`、`--glow`、`--amb`、`--t-blue…--t-rose`。
- 首次运行期间给 `<html>` 加 `data-ob="ink"` 并设置 `color-scheme: dark`，使窗口标题栏、滚动条、原生控件为深色，不受外观设置影响；进入主窗口时移除。
- 首次运行内打开的表单和提示框挂在 `.ob-stage` 内，继承墨黑。
- 字体、控件尺寸按 PRD §4.2、§4.3 写在 `firstrun/firstrun.css`，替换现有内容中不再使用的部分。

### 3.3 动效基础件

**`firstrun/Stage.tsx`**：首次运行的外壳。

- 渲染红绿灯区域（`.ob-top drag`，高 44）、进度条（`--p` 按步骤 5/28/52/76/100%）、背景极光（两个 `<i>`，`data-step` 决定位置，PRD §5.2 表格写成 CSS）、粒子画布、内容区和表单层。
- 换页：子内容以「页面键」区分。键变化时旧内容加 `.leaving`（160ms），180ms 后换成新内容并加 `.entering`；新内容中带 `.in` 的元素按 `--i`、`--d` 进场（PRD §4.4）。页面键：
  - `welcome`；`team:searching` / `team:found`；`her:choose`（未接上模型和浏览器登录中共用，演示不中断）/ `her:awake`；`far:out` / `far:in`；`done`。

**`firstrun/fx.ts`**：粒子。一张 `<canvas>` 覆盖窗口，`pointer-events: none`，按 `devicePixelRatio` 设置尺寸，合成方式 `lighter`。参数与原型 `FX` 完全相同，以常量写出：

| 方法 | 用途 | 参数（PRD 对应节） |
|---|---|---|
| `ambient(on)` | 背景光点 | 34 个，§5.2 |
| `assemble(el)` | 汇聚成标志 | 3×36，§5.3 |
| `burst(el, colors, n, speed)` | 散开 | Her 醒来 56 个 / 6，§5.5.3；登录后每节点 16 个 / 3.6，§5.6.4；完成 40 个 / 6，§5.7 |
| `travel(path, reverse)` | 沿连线的光点 | 50 帧 + 拖尾，§5.5.2 |
| `confetti()` | 撒花 | 2×80，§5.7 |
| `later(fn, ms)`、`reset()` | 定时与清理 | 换页时 `reset()` |

- 帧循环用 `requestAnimationFrame`，`dt = min(2.5, 实际间隔 / 16.7ms)`；没有粒子且背景光点关闭时停止循环。
- 颜色从 CSS 变量读取（`--aurora-*`、`--amb`）；Her 的颜色常量 `#cbd3fb`、`#ffa3be`、`#f1f3ff`、`#b3bcf2`。
- `matchMedia("(prefers-reduced-motion: reduce)")` 为真时所有方法不做任何事；CSS 中同一媒体查询关闭全部 `animation` 和 `transition`，`.ty span` 直接显示。

**其他**

- `Typed`：把文字拆成单字 `<span style="--i">`，按 PRD §4.4 逐字出现。
- 标志：`@hermesh/ui` 的 `Mark` 增加 `variant="draw"` 和 `delay`（第一道光束的起点）。现有的 `reveal` 是 0.75s、间隔 .12s，与原型不同，所以另加：每道 1s、`cubic-bezier(.6,0,.2,1)`、间隔 .2s。第 1 步 `delay=1000ms`，表单状态页和第 2 步 `200ms`。
- Her 的猫：`her/Cat.tsx` 增加 `asleep`（闭眼，呼吸 5.2s）和 `wake`（睁眼 + 跳起，只在 `.entering` 内播放）两种状态，动画写在 `her/cat.css`，与原型相同。

### 3.4 各步

| 步骤 | 文件 | 要点 |
|---|---|---|
| 1 欢迎 | `firstrun/Welcome.tsx` | 进场时 `fx.assemble(标志)`；三行文字 `--d` 为 1900/2300/2800ms；上方保留现有 `ErasedNote` |
| 2 Agent 提供方 | `firstrun/Team.tsx`（替换 `Prepare.tsx`） | `useCheckAgents()`；`sortAgents()` 给出已安装列表和就绪数；查找中状态显示到 `searching` 为假且距进入不少于 2200ms；行内「在终端运行」复用 `AgentList.tsx` 的现有按钮；项目列表复用 `settings/Projects.tsx` 的数据与「添加项目…」，按 PRD §5.4 的行样式渲染；「继续」可点条件沿用 `canGo` |
| 3 Her | `firstrun/HerStep.tsx`、`firstrun/HerShow.tsx` | 见下 |
| 4 登录 | `firstrun/Network.tsx`、`firstrun/MeshMap.tsx` | 见下 |
| 5 完成 | `firstrun/Done.tsx` | 进场后 300ms `fx.confetti()`，520ms `fx.burst(猫)`；「进入 Hermesh」见 §3.4.3 |

#### 3.4.1 第 3 步

- 是否显示：`herChoices(st.machines)` 中本机一项没有 `why`（现有 `her/environments.ts`）。否则第 2 步「继续」直接进入第 4 步。
- 进入时：本机 `her?.enabled` 为假则调用 `local.setHer({ machineId, enabled: true })`；Her 运行（`status.her.running`）之前三个按钮 `disabled`。
- 外面用现有的 `HerContext.Provider` 提供本机 Her（构造方式与 `her/HerPage.tsx` 相同，抽成 `useLocalHer(machineId)`），使 `her.call` 和模型列表可用。
- 接模型复用 `her/onboarding.ts` 的 `Flow` 和同一个按 `machineId` 存放的 `flows` 表（`ModelOnboarding.tsx` 中的 `put` / `update` 导出为 `startFlow` / `updateFlow`）：
  - 「用 ChatGPT 登录」：`chooseChatGpt` + `window.hermesh.signInHerChatGpt(machineId)`，结束时 `signInEnded`；「换一种方式」：`otherWay` + `cancelHerChatGpt`。
  - 「接入 Token Plan」「填写 API Key」：打开 `ModelSheet`，内容为 `ModelOnboarding.tsx` 中的 `Choices`、`KeyField`（导出），步骤和文案同现有对话式接入。
  - 因为 `flows` 在同一个渲染进程里，进入主窗口后 Her 页直接显示这次接入的完成状态（PRD §6.3）。
- 醒来：`Flow.step` 到达 `connecting` 或 `done`，且 Her 状态报告有可用的默认模型。模型名用 `modelTitle(默认模型, models)`。
- `HerShow.tsx`：`SHOW` 帧表（10 帧，PRD §5.5.2）和三条连线路径为常量；`setInterval(2100)` 推进，每帧整体应用（任务卡状态、气泡文字、询问行、产出行、光标、幕切换高亮），并对 `go` 中的连线调用 `fx.travel`；点幕切换跳到第 0/4/8 帧并重启计时；页面进场 700ms 后开始；减弱动态效果时停在第 1 帧、不启动计时。任务卡的提供方图标用 `AgentLogo`（claude、codex、gemini）。

#### 3.4.2 第 4 步

- `MeshMap.tsx`：节点、团队框、11 条连线和 7 条消息为常量（PRD §5.6.2）；进场 1100ms 后开始，`setInterval(2400)`；每条消息点亮连线、`fx.travel`，650ms 后点亮收件节点并放置气泡（`path.getPointAtLength(总长 / 2)`，x 限制在 110–522，y = max(98, y − 16)）。
- 状态：`signedIn = status.initialized && !status.signedOut`。由未登录变为已登录时页面键变化，进场后按 PRD §5.6.4 对 6 个节点依次 `fx.burst`。
- 底栏「登录」「使用自己的服务器…」打开 §3.5 的表单；「暂不登录」「继续」进入第 5 步。

#### 3.4.3 进入主窗口

「进入 Hermesh」：

1. 应用提醒默认值：与现有 `Authorize` 相同，`save("openAtLogin" | "notifyQuestion" | "notifyFailed", true)`，`window.hermesh.setLoginItem(true)`。
2. `setProgress(null)`，写入 `firstRunDone`，移除 `data-ob`。
3. `Root` 随之渲染主窗口，`setWindowMode("main")` 触发现有的窗口增长动画（`platform.grow`）。
4. 起始页：现有 `start.ts` 在 Her 开启时打开 Her，无需改动；第 3 步因本机不能运行 Her 而跳过时，调用 `setNotNow(true)`，使主窗口打开概览（否则会打开 Her 的「选择 Her 在哪里运行」）。

### 3.5 登录表单（PRD §5.6.3、§7.5）

新文件 `firstrun/SignInSheets.tsx`，导出一个 `SignInFlow` 组件，在首次运行、主窗口（需要登录提示中的「登录…」）和设置窗口（账号与设备）三处使用。逻辑取自现有 `firstrun/SignIn.tsx`：

| 表单页 | 来源 | 改动 |
|---|---|---|
| 浏览器 | `Browser`；`bridge().signIn(center)`；`onLink` 解析 `link` / `join` | 换成表单内状态页样式 |
| 自己的服务器 | `SelfHosted` | 去掉 HTTP 提示以外的说明，只留一条注释；「加入它…」移到底栏左侧 |
| 连接地址 | `JoinAddress` | 注释改为「设置 › 账号与设备 › 高级…」 |
| 授权 | `Authorize` | 去掉三个开关，`trusted = true`、`share = true`；不再在这里保存提醒设置（移到 §3.4.3；在设置里登录时不改提醒设置）；不再 `setProgress` |
| 等待批准、两位数字 | `Join` | 换成表单样式；`RequestJoin` 时 `localMachineName = 电脑名称`，`machineOnly = false` |

- 官方服务成功：`bridge().completeSignIn({ localMachine: true })`（即 `Setup`）后 `st.refresh()`。
- 自己的服务器成功：`local.setup({ relayUrl, claimCode, rootLabel, rootPlatform, localMachineName })`。
- 错误：沿用 `troubleOf` 和 `TroubleSheet`。

**再次登录**（`status.initialized && status.signedOut`）：

- 官方服务：`packages/core/src/signin.ts` 的 `appLoginURL(center, challenge, owners?)` 增加可选参数，生成 `…/app-login?challenge=…&owners=<ownerId>`；主进程 `signin.ts` 新增 `resumeSignIn(center, ownerId)`（打开浏览器）和 `completeResume()`（拿到链接码后调用 `LocalService.SignIn{claimCode, claimVerifier, account}`），preload 暴露为 `resumeSignIn`、`completeResume`。中心在 `owners` 指定时直接回传链接码（trusted-devices.md §4.8，已上线）。账号不匹配时 `Claim{resume}` 失败，显示 PRD §7.5 的提示。
- 自己的服务器：直接 `local.signIn({})`。

### 3.6 主窗口（PRD §6）

- `Sidebar.tsx` `AccountMenu` 底部状态：`initialized && !signedOut` → `common.sidebar.signedIn`（已登录），否则 `common.sidebar.signedOut`（未登录）；不再用 `relayConnected`。
- 新 hook `useNeedsAccount()`：返回 `(entry: "newGrant" | "addMachine" | "enterCode") => boolean`，未登录时打开 `NeedSignIn` 提示框并返回 `true`。接入点：
  - `commands.ts` 中 `newGrant`、`addMachine`、`enterCode` 三个命令（主进程菜单和快捷键都走这里）；
  - `overview/` 的「+」菜单；`AccountMenu` 的对应项；
  - 设置窗口：`settings/Grants.tsx` 的「连接 agent…」、侧栏环境「+」、账号与设备页的两个按钮；
  - Her 的环境切换「添加环境」（`her/Setup.tsx` 的 `onNewServer` 及切换菜单）。
- `main/menu.ts` 中这三项的 `owner` 可用条件改为 `member`（§3.1），不再因未登录而置灰。
- `NeedSignIn`（`sheets/NeedSignIn.tsx`）：PRD §6.2 的提示框；「登录…」打开 `SignInFlow` 的官方服务路径；成功后 toast「已登录」。

### 3.7 设置 › 账号与设备（PRD §7）

- `settings/Account.tsx` 的 `Account` 重写为 PRD §7.2 的三段；`MachineAccount`（仅执行任务的电脑）不变。
- 数据：账号行用 `status.hosted`、`status.relayUrl`（主机名）、`status.signedOut`；设备列表用现有 `st.devices`（`ListDevices`），未登录时只显示本机一行；本机行标题用 `platform.computerName()`。
- 设备行图标：`Device.platform` 为 `ios` 时手机图标，否则笔记本图标；说明固定 `settings.account.viaAccount`（经 Hermesh 账号）。
- 「添加设备…」：新 `sheets/AddDevice.tsx`（替换现有 `addDevice` 表单的界面，等待和批准逻辑不变）。
- 「退出登录…」：提示框确认后 `local.signOut({})`，toast「已退出登录」。被移除（`status.removed`）时沿用现有的「退出登录」到 `Leave` 的路径。
- 「登录」：未初始化时 `SignInFlow` 官方服务路径；已退出登录时再次登录路径（§3.5）。
- 侧栏顶部（`Settings.tsx`）：按 PRD §7.1 渲染。

### 3.8 文案

所有新文案放在 `apps/desktop/src/locales/zh-CN/*.json`，同时补 `en`。键名：

| 文件 | 新增或修改 |
|---|---|
| `firstrun.json` | 重写：`welcome.{title,lede,start}`、`team.{searching,found_other,none,noneBody,sub,signedIn,notSignedIn,projects,noProjects,promise,later,next}`、`her.{title,sub,sample,acts.{patrol,relay,report},pick.{title,chatgpt,plan,key,note},browser.{title,back,note},awake.{line1,line2,model},next,later,show.*}`、`net.{title,sub,titleIn,subIn,gains.*,map.*,own,skip,signIn,next}`、`done.{title,ready,noModel,noHer,enter}`、`sheets.{browser,own,join,auth,authJoin,wait,digits}.*`；删除不再使用的 `authorize.*` 开关文案、`prepare.*`、`connect.*`、`firstTask.*`（实现时以 `pnpm` 的未用键检查为准） |
| `common.json` | `sidebar.signedIn`「已登录」、`sidebar.signedOut`「未登录」（替换 `connected`、`notConnected` 在账号处的用法）；`needSignIn.{title,body,signIn}`；`sidebar.emptyBody`、`team.turnHerOn` 改为「它」 |
| `settings.json` | `account.{sectionAccount,signedOutTitle,signedOutBody,signIn,hostedLine,ownLine,signedIn,connected,localNote,useOwnServer,sectionDevices,thisDevice,viaAccount,devicesNote,addDevice,sectionNetwork,needsSignIn,agents,agentsBody,computers,computersBody,signOutTitle,signOutBody,signOutButton,signedOutToast,signedInToast,ownToast,wrongAccount}`；删除 `signOutFirstMessage` |
| `sheets.json` | `addDevice` 重写为 `{title,sub,step1,step2,step2Own,step3,note,waiting,done}` |
| `her.json` | `grant.runs`、`progress.emptySub`、`hp.builtinWindow`、`hp.builtinNoWindow` 改为「它」 |

具体中文以 PRD 为准，逐字照抄；平台变体（`_win`、`_linux`）按现有规则补齐。

### 3.9 示例数据

第 3 步的 `SHOW` 和第 4 步的 `MESH`、`WIRES`、节点名称是固定示例，以常量放在 `firstrun/samples.ts`，文案走 i18n。它们不读取任何用户数据。

## 4. 兼容与迁移

| 情况 | 行为 |
|---|---|
| 升级前已登录的用户 | `initialized` 为真，首次加载写入 `firstRunDone`，直接进入主窗口 |
| 升级前处在旧首次运行中途（`firstRun` 为旧步骤） | 视为 `null`；已初始化的进入主窗口，未初始化的从新流程第 1 步开始 |
| 仅执行任务的电脑 | 不变 |
| 旧版本的 hermeshd | 桌面应用自带 hermeshd，同版本升级，无版本差 |
| 其他设备（iPhone、旧版桌面端）看退出登录的 Mac | 显示为离线，与关机相同 |
| 退出登录的 profile 升级前就存在（`kSignedOut` 已为 1） | 升级后启动本机 machine，mesh 暂停状态需要初始化：`Run` 发现 `signedOut` 时调用 `SetMeshPaused(true)` |
| `hermeshd` 命令行（`status`、`doctor`） | `signedOut` 时报告「已退出登录，本机照常运行」；无其他改动 |

## 5. 测试

按 AGENTS.md：每项功能有单元测试，并有尽量接近真实使用的端到端测试。Go 端到端测试用 `-count=1 -v`。

### 5.1 单元测试

| 包 | 测试 | 验证 |
|---|---|---|
| `internal/machine` | `TestMeshPausedKeepsSessionsAndPersists` | 暂停后中继连接和直连监听关闭、会话继续、Her 进程内拨号可用；重启后仍暂停；恢复后重新连上 |
| `internal/owner` | `TestSignOutKeepsLocalMachine` | `SignOut` 不重启 profile；`phase == "local"`；`ListMachines` 只有本机；等待回答的会话可回答 |
| `internal/owner` | `TestSignInResumesInPlace` | `SignIn` 后 mesh 恢复、中继重连、本机 ID 不变、会话不中断 |
| `internal/owner` | `TestJoinFromStandaloneSignsOnce` | Her 已开启时 `RequestJoin` 的签名批次为 `PairingRequest, OwnerDeviceCert, MachineCert, Grant`；`ConfirmJoin` 不再签名 |
| `packages/core` | `appLoginURL` | 带 `owners` 的 URL |
| 桌面渲染层 | `progress.test.ts` | §3.1 的判断：新安装、老用户、退出登录、全部清空、旧步骤 |
| 桌面渲染层 | `HerShow.test.ts`、`MeshMap.test.ts` | 帧表与消息表和 PRD 一致（帧数、每帧状态、连线方向） |

### 5.2 端到端：hermeshd（`e2e/standalone_test.go`）

- `TestStandaloneSignOutAndIn`：真实 `hermeshd`、`hermesh-relay`、`hermesh` 和 `fakeacp`。单机启动 → `Setup`（自建中继）→ 外部 CLI 接入并列出项目 → 开一个等待回答的会话 → `SignOut` → 本机回答该会话并完成；外部 CLI 连不到这台 machine；`GetStatus` 为 `phase "local"` → `SignIn` → 外部 CLI 恢复，machine ID 不变。
- `TestStandaloneJoinSignsOnce`：设备 A `Setup`；设备 B 单机开启 Her（her 模拟程序）后 `RequestJoin`，A 批准，B `ConfirmJoin`；断言 B 只签了一次、Her 在中继上以同一个 client ID 发布、Her 能调度 B 上的会话。
- 现有 `TestStandaloneColdStartAndLifecycle`、`TestStandaloneHerFullLifecycle` 保持通过。

### 5.3 端到端：桌面端（`apps/desktop/e2e`，Playwright）

`onboarding.spec.ts` 重写为以下用例（同文件内的用例有先后依赖）：

1. **不登录走完**：全新数据目录、`HERMESH_FAKE_PROVIDERS=1`、her 模拟程序。第 1 步等待「开始设置」可点 → 第 2 步看到「已找到 N 个 Agent 提供方」、添加一个项目 → 第 3 步演示区出现并在 2.1 秒后换帧；用「填写 API Key」接上模拟模型 → 看到「你好，我是 Her。」→ 第 4 步看到连接图的消息气泡 →「暂不登录」→ 第 5 步「设置完成」→ 主窗口打开 Her 页，向 Her 发消息并收到回复。
2. **需要登录的提示**：主窗口账号菜单「添加环境…」、⇧⌘N、设置 › 授权「连接 agent…」都弹出「…需要登录」；「添加项目…」直接打开。
3. **在第 4 步连接自己的服务器**：本地启动 `hermesh-relay`，第 3 步之后用「使用自己的服务器…」填地址和认领码 → 看到「已接入 Hermesh 网络」→ 完成 → 设置显示「已连接」。期间第 1 个用例留下的 Her 会话不中断。
4. **退出登录与再次登录**：设置「退出登录…」→ 页面显示「未登录」，Her 仍回复 →「登录」→ 显示「已连接」，不弹出授权。
5. **减弱动态效果**：`page.emulateMedia({ reducedMotion: "reduce" })`，首次运行各步立即可见，画布没有绘制（检查画布像素），演示停在第 1 帧。
6. **老用户升级**：用已 `Setup` 的数据目录启动，直接进入主窗口。

每个用例在关键步骤各截一张 1 倍图，与原型对应的状态比对布局（人工检查，不做像素断言）。

## 6. 分步交付

每步独立可用、独立带测试。

| 步 | 内容 | 依赖 |
|---|---|---|
| 1 | hermeshd：§2.1、§2.2、§2.3 及 §5.1 Go 单元测试、§5.2 端到端测试 | 无 |
| 2 | 桌面：§3.1 路由、§3.2 墨黑、§3.3 动效基础件、§3.4 第 1、2、5 步（第 3、4 步先直接跳过） | 无 |
| 3 | 桌面：第 3 步（Her、演示、接模型） | 2 |
| 4 | 桌面：§3.5 登录表单、第 4 步、§3.6 主窗口、§3.7 设置 | 1、2 |
| 5 | 文案 en、§5.3 全部用例、清理旧的 `SignIn.tsx`、`Prepare.tsx`、`Connect.tsx`、`FirstTask.tsx` 中不再使用的部分 | 3、4 |

## 7. 风险

| 风险 | 应对 |
|---|---|
| 粒子和动画在低配电脑上掉帧 | 粒子总数峰值约 300（撒花 160 + 光点 34 + 散开）；无粒子时停止帧循环；窗口隐藏时（`document.hidden`）暂停 |
| 第 3 步 Her 启动需要几秒 | 按钮在 Her 运行前不可点；演示区照常播放，不让用户干等 |
| 退出登录后本机仍接受同一用户的进程内调用，用户可能以为「退出登录」会停掉一切 | 提示框写明「项目、任务和 Her 都会保留」；需要停掉的用「保持在线」或退出应用 |
| 墨黑只用于首次运行，进入主窗口时颜色变化明显（浅色外观的用户尤甚） | 窗口增长动画期间内容是淡出状态，颜色切换不突兀；PRD §11 决定 1 |
| 去掉「仅执行任务」的加入方式 | PRD §11 决定 2：只执行任务的电脑用命令行安装 hermeshd；保留后端 `machineOnly` 路径，界面以后可以恢复 |

## 8. 实施记录

正文是实施前的方案。实际实施与正文不同之处如下，以本节为准。

**文件**

| 正文 | 实际 |
|---|---|
| `firstrun/HerShow.tsx`、`firstrun/MeshMap.tsx` | 演示区在 `firstrun/HerStep.tsx`，连接图在 `firstrun/Network.tsx`；帧表、连线和消息放在 `firstrun/samples.ts`；粒子在 `firstrun/fx.ts` |
| `firstrun/SignInSheets.tsx` | `firstrun/SignInFlow.tsx`（样式 `firstrun/signin.css`）。主窗口和设置窗口通过新的 `signIn` 表单（`Sheets.tsx`）打开它 |
| 新的 `sheets/AddDevice.tsx` | 改写 `sheets/Add.tsx` 中现有的 `AddDeviceSheet` |
| 类名前缀 `.ob-`，根节点 `.ob-stage`，极光位置用 `data-step` | 前缀 `fr-`（`.ob-` 已被 `her/her.css` 使用），根节点 `.fr-stage`，极光位置用 `data-aura` |
| `Prepare.tsx`、`Connect.tsx`、`FirstTask.tsx` 中不再使用的部分 | 三个文件整体删除；`SignIn.tsx` 只留共用部分（`savedCenter`、`ErasedNote`、`plainHTTP`、`Join`、`Removed`、`TroubleSheet`） |

**行为**

- 被移除的电脑点「重新设置这台 Mac」后，从欢迎页重新走一遍首次运行（清除 `firstRunDone`），与改版前相同（§2.2「现有逻辑不变」）。
- 修复 hermeshd 的一个崩溃：Leave 清空账号信任后，之前已发起的中继拨号再读取凭据时空指针（`Owner.credentials`）。现在返回空凭据，中继拒绝即可。被移除的电脑重新设置时可复现。

**真机走查后修正**（2026-11-25，全新数据目录、本机真实的 Claude Code 和 Codex、空的 Her）

- 第 2 步列表行的类名 `ic` 与全局图标类 `.ic`（16×16）冲突，行被压成图标宽、内容竖排。改为 `lead`。
- 极光的两团光用 `filter: blur(80px)`，在 Chromium 的合成层上被裁出硬边矩形；改为径向渐变（颜色用注册的 `--c` 过渡），极光层四边各外扩 40px，漂移时不露边。
- 欢迎页的粒子聚成标志从未出现：子组件的 effect 先于父组件运行，`fx.assemble` 时画布还没挂上。`Stage` 改在 layout effect 里挂画布。
- 新安装第一次启动，窗口先以主窗口尺寸（1240×800）显示约 0.4 秒再缩成 720×660。现在没有记住的尺寸时，窗口先不显示，等页面报告模式后按该尺寸直接显示（hermeshd 不回应时 1.5 秒后照常显示）。
- 查找中的 16 个提供方图标一行铺满窗口，改为每行最多 8 个、两行。
- 第 3 步 Her 启动前三个按钮不可点却没有说明，改为在说明行显示「正在启动 Her…」。
- 填写 API Key 时「它只存进 homebook 的凭据库」用的是 machine 的短名，改为这台电脑的名字（与设置页一致）。
- 项目路径过长时折成多行，改为一行省略，悬停显示完整路径。
- 第 2 步已有可用的 Agent 提供方但没有项目时，「还没有项目」下方说明「添加一个项目后即可继续，Her 会在项目里给 Agent 派活。」（「继续」为何不可点）。
- 完成页的撒花画布放到页面之下（`z-index: 0`），彩纸从文字和按钮后面落下。
- 主窗口 Her 的接模型引导和问候语里，Her 在本机时用电脑名（`computer.ts` 的 `useMachineTitle`），在其他电脑上仍用 machine 名。machine 名本身不变（它是给 agent 和命令行用的标识）。

**测试**（全部为新增或改写）

| 位置 | 测试 |
|---|---|
| `internal/machine/meshpause_test.go` | `TestMeshPausedPersists` |
| `internal/owner/credentials_test.go` | `TestCredentialsAfterLeave` |
| `packages/core/src/signin.test.ts` | `appLoginURL` 带 `owners` |
| `apps/desktop/src/renderer/src/firstrun/` | `samples.test.ts`（帧表、连线、消息与 PRD 一致，两种语言的文案齐全）、`progress.test.ts` |
| `e2e/coldstart_test.go` | `TestColdStartSignOutKeepsWorking`、`TestColdStartJoinSignsOnce`（代替正文 §5.2 的 `TestStandaloneSignOutAndIn`、`TestStandaloneJoinSignsOnce`）；`e2e/profiles_test.go` 的 `TestAccountsOnOneDevice` 按原地退出登录改写 |
| `e2e/desktop_coldstart_test.go` + `apps/desktop/e2e/coldstart.spec.ts` | `TestDesktopColdStart`：能运行 Her 的电脑上不登录走完五步（her 模拟程序接模型）、主窗口未登录、需要登录的提示、再次打开直接进主窗口；减弱动态效果 |
| `apps/desktop/e2e/onboarding.spec.ts` | 不能运行 Her 的电脑上走完（跳过第 3 步）；第 4 步用浏览器登录官方服务、连接自己的服务器（HTTP 提示、被代理拦截）、加入已有账号（核对码、两位数字、被移除后重新设置）；设置里退出登录与再次登录；没有 Agent 提供方；添加设备；全部清空；可信设备的环境 |
| `apps/desktop/e2e/settings.spec.ts`、`frame.spec.ts` | 账号与设备页、侧栏底部「已登录 / 未登录」 |

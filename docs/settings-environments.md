# 设置重构：环境、Agent 提供方、项目、储存空间

状态：已实现（2026-11-22）。设计稿见
[settings-environments-v3-demo.html](settings-environments-v3-demo.html)（Mac）和
[settings-environments-iphone-demo.html](settings-environments-iphone-demo.html)（iPhone）。
v1、v2 两版 demo 留作讨论记录。

## 1. 目标

现在的「机器」页把五类东西平铺在一起：身份与状态、访问与安全、能力（编码 agent、项目、Her）、
运行中的任务、磁盘。磁盘占用排在共享开关前面，项目藏在每个编码 agent 下面，后台服务的三项设置要
放在一起推敲才知道后果。这次重构要做到：

- **概念清楚**：环境、Agent 提供方、项目、储存空间各是一个独立的东西，每个只在一个地方管理。
- **两端一致**：Mac 和 iPhone 结构相同，只在平台惯例上不同。
- **开关写后果，偶尔才用的操作做成按钮**：参考 macOS / iOS 的「设置」App。
- **长期正确**：项目和提供方解绑，后台服务在三个平台上行为一致；储存空间远程也能用，不再只限本机。

不在这次范围内：「授权」（连接进来的 agent）的重新定义（等这次重构完成后再单独讨论，名字暂时保持「授权」），以及
CLI、协议和文档里 machine / coding agent 的改名（见 §3）。

## 2. 决策

需要拍板的地方由我来定，理由如下。

| # | 决策 | 理由 |
|---|---|---|
| D1 | 界面上「机器」改叫**环境**（en: Environment），「编码 agent」改叫 **Agent 提供方**（en: Agent provider） | Her 已经把一台机器叫「环境」；hermeshd 里会话本来就是 `provider.Session`。「服务商」已被 Her 的模型提供方占用，所以没用 |
| D2 | 「连接你的 agent」这一页暂时仍叫**授权**（en 维持 Access）。iPhone 上的「连接的 Agent」也改成「授权」 | 用户要求先这样叫；两端先统一，这次重构完成后再重新定义 |
| D3 | 「磁盘目录」改叫**储存空间**（en: Storage）。Her 页里的「存储」也改成「储存空间」 | 和 macOS、iOS 的叫法一致，同一个 App 里不应有两个说法 |
| D4 | 从「通用」里拆出**通知**页 | 一页只讲一件事；iPhone 本来就有单独的「通知」页 |
| D5 | **项目不再绑定 Agent 提供方**。`Project.agents` 废弃，hermeshd 迁移时清空所有项目的这个字段，不再检查；要限制，用授权里已有的「Agent 提供方」范围 | 用户要求项目独立。授权是限制「谁能用什么」的唯一地方；如果项目上还留着一条看不见的限制，就是迟早要出的 bug |
| D6 | 储存空间**从一开始就支持远程环境**，通过 `MachineAdminService` 转发，和 `TestAgent` 走同一条路 | iPhone 上的每个环境都是远程的，不做远程，iPhone 端这个功能就是空的 |
| D7 | 释放时，被 Git 忽略的文件只有落在**可重新生成的白名单**里才放行（`node_modules`、`.venv` 等，§5.7.3），其他一律保留并说明原因 | 默认安全：`.env`、证书这类文件也是被忽略的，不能删 |
| D8 | 自动释放**默认关闭**，每个环境单独开；阈值固定为 7 天，不做成可配置 | 删文件的事要用户自己点头；固定值够用，免得多一个设置 |
| D9 | 「登录时打开」「退出后保持后台服务」「后台服务 启动/停止」合并成**保持在线**一个开关；启动/停止只保留在菜单栏和应用菜单里 | 用户关心的是结果；第三项本来就和菜单里的重复 |
| D10 | 「保持在线」的迁移：`stayOnline = 登录项已开 \|\| keepDaemonOnQuit` | 这两项任意一项开着，都说明用户想在窗口外保持可用。宁可多在线，事后用户可以关 |
| D11 | 侧栏「环境」分组里**始终有「这台 Mac」**。它还不是环境时，这一页提供「加为环境…」和「保持在线」 | 「保持在线」也管审批和 iPhone 连接，和这台 Mac 是不是环境无关；把「是不是环境」当作一个状态，而不是让这一项时有时无 |
| D12 | 暂停从开关改成**按钮加横幅**，两端都一样 | 暂停是偶尔的运维操作，不是一项设置 |
| D13 | 移除一台同时是可信设备的环境，就等于移除那台设备（`RemoveDevice`），确认文案说清楚这一点 | 现在 Mac 上不让移除、只给一句提示，iPhone 上却可以移除，两端行为要一致 |
| D14 | 大小按**操作系统文件管理器的习惯**显示：Apple 平台和 Linux 用 1000 进制，Windows 用 1024 进制 | 和 Finder、资源管理器显示的数字对得上 |
| D15 | CLI、协议、文档里的 `machine`、`coding agent` **不改** | 改了会破坏已接入的 agent 和脚本；界面改名不影响寻址（`名称/项目`）。各端文档加一张术语对照表 |

## 3. 术语对照

| 界面（zh / en） | 代码、协议、CLI |
|---|---|
| 环境 / Environment | `Machine`、`machine_id`、`hermesh machine` |
| 这台 Mac（这台电脑） | `Machine.local`、owner 的 local machine（`SetLocalMachine`） |
| Agent 提供方 / Agent provider | `AgentStatus`、`--agent claude`、`internal/provider` |
| 项目 / Project | `Project`（不再有 `agents`） |
| 储存空间 / Storage | 会话 worktree（`Workspace`），`ListWorkspaces` |
| 保持在线 / Stay online | 偏好 `stayOnline`，加上平台登录项和退出策略 |
| 授权 / Access | `Client`、grant |

## 4. 信息架构

### 4.1 Mac 设置窗口

```
侧栏                          页面
─────────────────             ─────────────────────────────────────────
[头像] 账号与设备       ─→    账号：可信设备、添加设备…、退出登录…、高级…（表单）
通用                   ─→    外观、语言、自动下载更新
通知                   ─→    会话、待处理请求和菜单栏圆点
授权                   ─→    （不变）
环境                 [+]      + 菜单：把这台 Mac 加为环境…（只在还不是时显示）/ 添加机器…
  这台 Mac                    环境页（§5.3），二级页：Agent 提供方 › 提供方、项目、Her、储存空间
  vps-1
  mac-mini   离线
```

machineOnly（只当执行环境的 Mac）：侧栏是通用、通知、账号，环境分组里只有「这台 Mac」，没有加号。

### 4.2 iPhone

点头像打开的表单改名为**设置**：账号卡片（→ 账号与设备）、用量、通知 / 外观 / 授权 / 网络诊断、环境列表加「添加环境…」、关于。
环境页及其二级页和 Mac 一一对应（demo 里有对照表）。iPhone 不是环境，所以没有「保持在线」和「停用」。

### 4.3 设置窗口路由（desktop）

`#settings/<page>[/<item>]`：

| 新路由 | 旧路由（别名，继续可用） |
|---|---|
| `account`、`general`、`notifications`、`grants[/<grant>]` | `devices` → `account` |
| `env/<machineId>` | `machines/<machineId>` |
| `env/<machineId>/providers`、`…/provider/<name>` | `machines/<machineId>/<agent>` |
| `env/<machineId>/projects`、`…/her`、`…/storage` | — |
| `env/this` = 这台 Mac（不论它是不是环境） | `machines`（列表页取消，转到 `env/this`） |

调用方（`Inspector.tsx:66`、`overview/Map.tsx:610`、`overview/Attention.tsx:53`）改用新路由；别名留在 `route()` 里，
给菜单栏和通知里已有的链接用。窗口工具栏加上前进和后退，用一个栈记录走过的页面。

## 5. 技术方案

### 5.1 命名与文案

- desktop：`locales/{zh-CN,en}/*.json` 里指「机器」这个概念的地方都改（settings、main 的「添加机器…」→「添加环境…」、
  common 的 tray、overview、session inspector 的「机器设置」→「环境设置」、firstrun、sheets）。
  「编码 agent / Coding agents」→「Agent 提供方 / Agent providers」。改名对话框的说明改成「agent 用『名称/项目』找到项目…」。
- iPhone：`Localizable.xcstrings` 同样处理（约 80 处「机器」）。HermeshKit 里的 zh-Hans `Localizable.strings` 也要查一遍。
- 有一个测试（desktop 和 iOS 各一份）在 zh 文案里找「机器」和「编码 agent」，确保没有漏改；
  只在明确列出的地方允许出现「机器」（命令行示例、`hermeshd pair` 等）。

### 5.2 Mac 设置窗口骨架

- `Settings.tsx`：`pages` 改成：账号（侧栏顶部的特殊一项）、general、notifications、grants，再加上动态的环境组。
  环境组从 `st.machines` 生成；「这台 Mac」永远排第一，名字固定显示为「这台 Mac」，状态小字（已暂停、离线、已停止）写在右侧。
- 新文件，都在 `settings/` 下：
  - `Environment.tsx`：环境页
  - `Providers.tsx`：由 `AgentList` 改成提供方列表
  - `Provider.tsx`：由 `AgentPage` 改来，去掉项目部分
  - `Projects.tsx`：项目表格
  - `Storage.tsx`：替代 `WorkspaceStorage.tsx`
  - `Notifications.tsx`：从 `General.tsx` 拆出来
  - `AccountAdvanced.tsx`：「高级…」表单
- 删除：`Machines.tsx` 的列表页和 `ShareRow`，以及 `WorkspaceStorage.tsx`。
  `AgentList.tsx` 里给首次运行用的组件保留，按 §5.6.3 改。
- 样式沿用 `settings.css` 的 `.fgroup/.frow`。新增的只有：居中的 hero、状态胶囊、横幅、带 +/− 的表格、储存空间条形图。

### 5.3 环境页

**页面结构**：
- hero：图标、名称、「平台 · Hermesh 版本」，再加一个状态胶囊：
  - 在线时：「在线 · N 个任务运行中」，点击调用 `showInMain({machine})`。
  - 离线时：「离线 · 最近在线 …」。
  - 后台服务停止时（只有这台 Mac）：「后台服务已停止 · 启动」。
- 问题提示：`health.tsx` 的 `Problems` 放在 hero 下面，不变。
- 已暂停时显示横幅「已暂停接受新任务 · 恢复」。
- 「名称」行可以直接编辑。失去焦点或按回车时调用 `renameMachine`；输入时复用 `cleanName` 的提示（「将保存为…」）。
- 「保持在线」：只有这台 Mac 有（§5.4）。
- 四个入口：Agent 提供方、项目、Her、储存空间。
- 底部按钮：
  - 左边：「停用这台 Mac…」（这台 Mac）或「移除…」（其他环境）。
  - 右边：「复制诊断信息」（只有这台 Mac）、「暂停接受新任务」或「恢复接受新任务」。

**入口行的值**：

| 行 | 显示 | 不可用时 |
|---|---|---|
| Agent 提供方 | 「N 个可用 · M 个未登录」，来自 `m.status.agents` | 离线：「—」，行变灰 |
| 项目 | 项目数，来自 `st.projects` 里属于这个环境的 | 离线：「—」 |
| Her | 「开启」或「关闭」，来自 `m.her.enabled` | machineOnly 不显示这一行（开启 Her 需要根密钥签名） |
| 储存空间 | 缓存的总量（§5.7.5），没有缓存时显示「—」 | `methods` 里没有这个接口时显示「需更新」（§5.7.6）；离线显示「—」 |

**操作**：
- **暂停**：`setMachinePaused`。不再弹确认，暂停后横幅说明后果；侧栏状态显示「已暂停」。
- **停用这台 Mac**：确认表单，复用 `main/daemon.ts` 的 `impactLines` 列出会停下的任务和 Her。
  然后 `setLocalMachine({enabled:false})`，再跳回「这台 Mac」的「还不是环境」状态。
- **这台 Mac 还不是环境**：hero 写「这台 Mac 还不是环境」，下面是「加为环境…」按钮（`setLocalMachine({enabled:true})`，需要 Touch ID）和「保持在线」。原来 `Machines.tsx` 列表页的 `ShareRow` 删掉。
- **移除**：
  - 普通环境：`removeMachine`，确认表单和现在一样（含 Her 数据的选项）。
  - 同时是可信设备的环境：调用 `removeDevice`（D13），文案沿用 `account.removeDeviceMessage`。
- **Her**：二级页放 `HerRow`、清空数据、`GrantLine`、`ComputerUse`，从 `Machines.tsx` 原样搬过去。

### 5.4 保持在线

**偏好**：`Preferences.keepDaemonOnQuit` 换成 `stayOnline`（`shared/bridge.ts`、`main/notify.ts` 的默认值和白名单）。

- **迁移**：读偏好时如果 `stayOnline` 还没设置，就按 D10 算出来（登录项的状态从 `platform.loginItem()` 读），写回后删掉 `keepDaemonOnQuit`。
- **新装默认**：打开。首次运行按现在的逻辑设置登录项。

**`setStayOnline(on)`**（主进程新增，经 IPC 调用）：
1. `platform.setLoginItem(on)`：打开就在登录时以菜单栏模式启动 App，App 再把后台服务拉起来。
2. 存偏好，刷新应用菜单、Dock 菜单和菜单栏面板。

**退出流程**（`main/daemon.ts` 的 `before-quit`）：
- 打开：直接退出界面，服务继续跑，不弹窗（等同现在 `keepDaemonOnQuit` 打开时）。
- 关闭：停止服务后退出。**只有 `hasActiveWork` 为真时才弹窗确认**；现在用的是 `hasAnyImpact`，只要在线几乎每次退出都会问，用户已经明确选了「只在打开时可用」，不该每次都问。
- 菜单项：「退出并停止后台服务…」（打开时）和「仅退出界面（保持后台运行）」（关闭时）这两个一次性的替代项保留。

**三平台的一致性**：「关闭」的意思是服务跟着 App 走，所以「停止」必须让服务下次登录时也不会自己启动：

| 平台 | 登录时谁启动服务 | 停止后下次登录会不会自己起来 | 要改的 |
|---|---|---|---|
| macOS | App（plist 设了 `RunAtLoad=false` 和 `AfterInitialDemand`） | 不会 | 不用改 |
| Windows | hermeshd 的登录计划任务 | 不会（`stopDaemon` 会执行 `service uninstall`） | 不用改 |
| Linux | systemd 用户服务（enabled） | **会** | `stopDaemon` 改成 `systemctl --user disable --now`，`startDaemon` 和 `registerDaemon` 改成 `enable --now` |

**界面**：
- 「保持在线」下面的说明随开关状态改变（demo 里的两句话）。
- `loginItem().needsApproval` 时，说明后面加上「还要在系统设置的『登录项』里允许它。打开系统设置…」，这句话现在就有。
- 关闭开关时先弹确认（demo 的 `offline` 对话框）。
- 通用页里的「启动」一组删掉。菜单栏和应用菜单里的启动/停止不变。

### 5.5 Agent 提供方

- 提供方列表：已安装的每个一行（标志、名称、版本，右边「已登录 / 未登录」）。没安装的单独一组。下面是「重新检测」（只有这台 Mac），说明文字是「Hermesh 像你一样在命令行调用它们，不读取 API Key 和 Token。」，原来是共享开关的说明，挪到这里。
- 提供方页（`Provider.tsx`）：hero、未登录时的登录提示（`logins` 和「在终端运行」）、`AgentDefaults`（默认模型）。项目部分去掉。
- 授权详情里「编码 agent」那一行改名为「Agent 提供方」，逻辑不变。

### 5.6 项目独立

#### 5.6.1 hermeshd

- `internal/machine/store.go`：`migrations` 里加一条 `UPDATE projects SET agents = ''`。`Project.AllowsAgent` 和
  `sessions.go:393` 的检查删掉，`projectAgents`（`services.go:197`）只用来兼容旧客户端，收到的值一律丢弃。
- `proto/hermesh/identity/v1/identity.proto`：`AddProject.agents` 标注废弃，注释写明「ignored since …」。
  `ListProjects` 返回的 `agents` 从此为空。字段不删，旧客户端还会读。
- `cmd/hermeshd/project.go`：`--agents` 改成隐藏参数，传了就打印 `--agents is ignored: every signed-in agent provider can work in every project; limit agents in their access`。
  成功时的输出不再写「for every coding agent」。
- `internal/cli/projects.go:188`：判断一个项目能用哪些提供方，只看「已安装、已登录、授权允许」。
- `internal/preview/preview.go:354`：添加项目的预览不再显示 Coding agents 那一行。

#### 5.6.2 版本兼容

- **新 App + 旧 hermeshd**：App 不再发送 `agents`，空值在旧 hermeshd 上本来就表示「全部」。
  旧 hermeshd 上已有的限制要等那台机器更新后才清掉，这段时间里，项目页不显示这条限制。
- **旧 App + 新 hermeshd**：旧 App 发来的 `agents` 会被忽略。旧 App 里「不再给 X 用」点了没有效果，这一点写进更新说明。

#### 5.6.3 界面与首次运行

- 项目页（Mac 用 +/− 表格，iPhone 用列表加详情页）：名称、路径、工作方式（独立 worktree / 原地）、移除。
  添加时，这台 Mac 用 `pickDirectory`，其他环境用 `addProject` 表单（`ListRepos` 和文件夹浏览）。
  store 里的 `addProject` 表单状态去掉 `agent` 字段。
- desktop 首次运行（`firstrun/Prepare.tsx`）：`canGo` 改成「有一个提供方已登录，并且至少有一个项目」。
  页面上下排两块：提供方列表、项目表格。`AgentList.tsx` 里按提供方统计项目数的代码（`projectsFor`）删掉。
- iPhone 首次运行（`GuideView.swift:273/390`）：添加项目不再带 agent，`AddProjectSheet(machineID:)`。

### 5.7 储存空间

#### 5.7.1 协议

`proto/hermesh/machine/v1/machine.proto`，`MachineAdminService` 新增两个接口（`ACCESS_WRITE`，只有 owner 的设备能调）：

```proto
rpc ListWorkspaces(ListWorkspacesRequest) returns (ListWorkspacesResponse);
rpc ReleaseWorkspaces(ReleaseWorkspacesRequest) returns (ReleaseWorkspacesResponse);

message Workspace {
  string session_id = 1; string project_id = 2; string title = 3; string path = 4;
  uint64 size_bytes = 5; string size_error = 6;
  hermesh.session.v1.SessionStatus status = 7;
  google.protobuf.Timestamp last_active = 8;   // the session's updated_at
  WorkspaceBlock block = 9;                     // why it can't be released now
  string block_path = 10;                       // the first file behind it
}
enum WorkspaceBlock { WORKSPACE_BLOCK_NONE = 0; BUSY = 1; CHANGES = 2; LOCAL_FILES = 3; UNVERIFIED = 4; }
message ListWorkspacesRequest  { bool cached = 1; }          // cached: the last scan, at once
message ListWorkspacesResponse { repeated Workspace workspaces = 1; google.protobuf.Timestamp scanned_at = 2;
                                 bool auto_release = 3; }
message ReleaseWorkspacesRequest  { repeated string session_ids = 1; }
message ReleaseWorkspacesResponse { repeated Released results = 1; }  // per id: ok, or the block it hit
```

`identity.proto` 的 `AdminCommand` 加一个分支 `SetWorkspacePolicy { bool auto_release = 1; }`。
它和 `SetPaused`、`SetAgentDefaults` 一样，是一项设置，走 `o.admin(...)`，不需要 Touch ID。

`local.proto`：

- 新增 `ListWorkspaces{machine_id, cached}`、`ReleaseWorkspaces{machine_id, session_ids}`、`SetWorkspacePolicy{machine_id, auto_release}`，响应类型直接用 machine 包里的。
- owner 端转发（`internal/owner/owner.go`）：
  - 前两个照 `TestMachineAgent`（`owner.go:2291`）的写法：目标是本机就直接调，否则通过 `machineClient(...).Admin` 转发。
  - `SetWorkspacePolicy` 照 `SetMachinePaused`（`owner.go:2328`）的写法，发一条 AdminCommand。
- 旧的 `ListLocalWorkspaces` 和 `ReleaseLocalWorkspace` 再保留一个版本，内部转到新接口，然后删除。现在只有 desktop 在用。

#### 5.7.2 hermeshd 机器端（`internal/machine/workspace_storage.go`）

- **`ListWorkspaces`**：在现在的扫描基础上，每个工作目录再跑一次只检查、不删除的 `checkCheckout`，得到 `block` 和 `block_path`；`last_active` 取会话行的 `updated_at`。
  - 拆分 `checkCheckout`：改成返回 `(WorkspaceBlock, path, error)`，释放和列表共用同一套判断。
  - 运行中、等回答、有排队消息的会话直接标 `BUSY`，不再跑 git。
  - 并发：最多同时扫 4 个工作目录，结果按大小排序。
- **`ReleaseWorkspaces`**：逐个调用现有的 `releaseCheckoutLocked`。某个失败不影响后面的，每个的结果单独返回。
  成功的会话打一条 `workspace released` 日志，同时更新缓存。
- **错误文案改成结构化**：现在释放失败时返回的是英文字符串（`workspace kept: …`）。改成由 `block` 决定的错误，界面按 `block` 自己写文案。

#### 5.7.3 可重新生成的白名单（D7）

`checkCheckout` 读 `git status --porcelain=v1 --ignored=matching` 时，`!!` 开头的条目（被忽略的文件）：
路径的**某一级目录名**在白名单里就放行，否则返回 `LOCAL_FILES` 和这个路径。

```
node_modules .pnpm-store bower_components
.venv venv __pycache__ .pytest_cache .mypy_cache .ruff_cache .tox
.gradle .next .nuxt .svelte-kit .turbo .parcel-cache .angular .expo
DerivedData .build Pods .dart_tool
.cache coverage
```

- 文件级别只放行 `.DS_Store` 和 `*.pyc`。
- `dist`、`build`、`target`、`out` **不进白名单**：这些名字也常被用来放手写的东西，宁可让用户自己判断。
- `git worktree remove`（不加 `--force`）不看被忽略的文件。2026-11-22 在 git 2.54 上实测：
  一个工作目录里只有被忽略的 `.env` 和 `node_modules/`，`git worktree remove` 直接成功，两个都被删了。
  所以**被忽略的文件只有我们这一道检查在保护**。要加一个测试固定这个行为，CI 换 git 版本时能及时发现。

#### 5.7.4 自动释放（D8）

- 机器端的 kv 里加一个 `workspace_auto_release`（`"1"` 或 `"0"`），由 `AdminCommand_SetWorkspacePolicy` 写入，
  处理方式照 `services.go:404` 的 `SetPaused`。`ListWorkspacesResponse.auto_release` 把当前值带回界面。
- `Sessions.Janitor`（`sessions.go:2345`）每小时检查一次：自动释放开着的话，对每个 `IDLE`、`updated_at` 超过 7 天、并且工作目录还在的会话调用 `releaseCheckoutLocked`。
  被拒绝的（有本地改动等）跳过，24 小时内不再试同一个会话。
- 测试用可注入的时钟（Janitor 已经有 `janitorEvery`）。

#### 5.7.5 缓存

- 机器端在内存里保存最近一次扫描的结果和时间。扫描、释放、自动释放都会更新它；重启后为空。
- `ListWorkspaces{cached:true}`：有缓存就直接返回，没有就扫一次。环境页的那一行用 `cached`，储存空间页打开时用 `cached:false` 重新扫。
- 不把它放进 `MachineStatus`：Watch 频繁推送状态，`du` 的结果不该跟着推。

#### 5.7.6 版本门控（[version-skew.md](version-skew.md) §6.1）

这是第一个需要按 hermeshd 自己的接口做门控的功能，所以按那份文档的说法补上：

- `MachineStatus` 加上 `methods`（`repeated string`）：hermeshd 注册的 Admin RPC 名称。从注册处理器的地方自动生成，不手写。
- 客户端检查：desktop 在 `skew.ts` 里加 `machineAnswers(m, "ListWorkspaces")`，iPhone 在 Swift 里加同名函数。返回值是 true、false 或未知，未知按「能用」处理。
- 返回 false 时，入口行显示「需更新」，点进去是 Needs Update 视图。调用时遇到 `Unimplemented` 也走同一个视图，作为兜底。

#### 5.7.7 界面

- **desktop `Storage.tsx`**：
  - 顶部：会话工作目录的总量，加一条按项目分段的条形图。
  - 「建议」：
    - 自动释放：「开启…」，确认后调用 `SetWorkspacePolicy`；开启后显示「已开启」，旁边可以「关闭」。
    - 释放空闲的：「释放…」，确认后调用 `ReleaseWorkspaces`，传入所有空闲且可以释放的会话。
  - 按项目列出，ⓘ 打开一个表格：可以多选，不能释放的那一行变灰并写原因；底部是「在访达中显示」（只有这台 Mac）、「释放…」、「完成」。
- **iPhone `StorageView`**：同样的结构；项目页里左滑一个工作目录就能释放。
- **大小格式**：`@hermesh/core` 加 `formatBytes(n, platform)`（D14），iPhone 用 `ByteCountFormatter(.file)`。
- **原因文案**：

| `block` | 文案 |
|---|---|
| `BUSY` | 任务进行中 |
| `CHANGES` | 有未提交的改动 |
| `LOCAL_FILES` | 有不能从分支恢复的文件：`<path>` |
| `UNVERIFIED` | 无法确认这是这个任务的工作目录 |

### 5.8 通用、通知、账号（desktop）

- **通用**：外观、语言、自动下载更新。
- **通知**分为「会话」（Her 回复、会话结果、新任务）和「需要你处理」（Her 提问与审批、会话提问与权限请求、连接请求）。会话结果涵盖成功和失败。新安装默认开启。升级时，原来两个结果开关有一项开启就保留结果提醒，两项都关闭则继续关闭。连接请求始终开启。
- Mac 另设菜单栏圆点。iPhone 提供系统通知权限入口，应用图标角标显示待你响应的对象数（等待回答或审批的会话、连接请求和有未读消息的 Her，每个对象计 1），处理或已读后消退。设置按设备保存；iPhone 的推送订阅由执行环境按类别过滤。Her 的独立类别和会话权限推送需要执行环境支持。
- 正在本机前台查看对应 Her（环境和助手均匹配）、会话或连接请求时，应用内照常显示，系统提醒静默。切到其他对话或后台后，按该设备的设置提醒。会话完成时优先显示本轮回复摘要，缺少摘要时显示文件改动统计。[双端交互 demo](notification-settings-demo.html)
- **账号**：hero、可信设备、底部三个按钮（「退出登录…」「添加设备…」「高级…」）。
  「高级…」打开一个表单，里面是现在折叠在「高级」下的地址和命令、网页控制台，以及「全部清空…」。
  `MachineAccount`（machineOnly 的账号页）结构不变，只改文案。

### 5.9 iPhone

- **路由**（`AppModel.swift` 的 `Route`）：
  - 改名：`machine(id)` → `environment(id)`，`machineAgent(id, name)` → `provider(id, name)`。
  - 新增：`environmentName(id)`、`providers(id)`、`projects(id)`、`project(id, pid)`、`storage(id)`、`storageProject(id, pid)`、`account`、`appearance`。
  - 删除：`machines`。
- **视图**：

| 新视图 | 来源 |
|---|---|
| `SettingsSheet` | `AccountSheet` 改名并重排：账号卡片、用量、应用设置、环境列表、关于 |
| `AccountView` | 新建：可信设备从 `MachinesView` 挪来；退出登录、全部清空、删除账号、网页控制台、地址 |
| `EnvironmentView` | `MachineView` 重排：hero 和状态胶囊、暂停横幅、名称、四个入口、系统和版本、暂停按钮、移除 |
| `ProviderView` | `MachineAgentView` 去掉项目部分，保留登录、测试、默认模型 |
| `ProjectsView` / `ProjectView` | 新建 |
| `StorageView` / `StorageProjectView` | 新建 |
| `AppearanceView` | 外观原来是表单里的一个分区，改成单独一页 |

- 「接受新任务」开关改成按钮加横幅，`setPaused` 不变。右上角的「重命名… / 移除…」菜单去掉。
- `Service.swift` 加上 `listWorkspaces`、`releaseWorkspaces`、`setWorkspacePolicy`，`DemoService` 补上对应的演示数据（demo 模式和 UITest 都要用）。
- Her 页里的「存储」改叫「储存空间」（D3），只改文案。

## 6. 协议变更汇总

| 文件 | 变更 | 兼容 |
|---|---|---|
| `machine.proto` | `MachineAdminService` 加 `ListWorkspaces`、`ReleaseWorkspaces`；`MachineStatus.methods` | 旧机器返回 `Unimplemented`，走门控 |
| `local.proto` | 加 `ListWorkspaces`、`ReleaseWorkspaces`、`SetWorkspacePolicy`，都带 `machine_id`；旧的两个接口标废弃，保留一个版本 | desktop 同一版本里改为调用新接口 |
| `identity.proto` | `AdminCommand` 加 `SetWorkspacePolicy`；`AddProject.agents` 废弃 | 旧机器不认识新的 AdminCommand，会报错，界面走同一套「需更新」门控；旧客户端发来的 `agents` 被忽略 |

改完 proto 要重新生成 Go、TS、Swift 三份代码（`gen/go`、`packages/proto`、`packages/HermeshKit`）。

## 7. 测试

按 AGENTS.md 的要求：每项改动除了单元测试，还要有一个尽量贴近真实使用的端到端测试。

| 层 | 测试 |
|---|---|
| Go 单元 | `workspace_storage_test.go`：<br>• 白名单放行 `node_modules`、`.venv`<br>• `.env`、`dist` 被拦下<br>• 每种 `block` 都能测到<br>• 批量释放时部分失败<br>• 释放后缓存更新<br>• 自动释放（注入时钟，7 天边界，24 小时内不重试）<br>• `git worktree remove` 会删掉被忽略的目录<br><br>`store` 迁移后 `agents` 为空；`StartSession` 不再检查项目的 agents |
| Go e2e（`e2e/`） | 两台机器，owner 通过中继列出并释放远程环境的工作目录；非 owner 设备调用会被拒绝；旧版机器（模拟没有 `methods`）返回 `Unimplemented` |
| CLI | `hermeshd project add --agents` 给出警告，项目对所有提供方可用；`hermesh project list` 的提供方那一列 |
| desktop 单元 | `route()` 的别名；`stayOnline` 迁移的四种组合；`formatBytes`；zh 文案里不再出现「机器」和「编码 agent」 |
| desktop e2e（Playwright） | 改写 `settings.spec.ts:326/421/623`：<br>• 侧栏环境组<br>• 改名称<br>• 暂停后的横幅和恢复<br>• 提供方页<br>• 项目表格的 +/− 和工作方式<br>• 储存空间：单个释放、多选释放、批量释放、开启自动释放、看到被拦下的原因<br>• 停用后再加回这台 Mac<br><br>`daemon.spec.ts`：保持在线打开时退出不弹窗、服务继续跑；关闭时空闲直接停、有任务才问<br><br>`onboarding.spec.ts`：首次运行可以不选提供方就添加项目 |
| iPhone UITest | 在 iPhone 17 Pro 模拟器上（`HERMESH_IPHONE_SIMULATOR`）：<br>• 设置首页 → 环境 → 名称 / 提供方 / 项目 / 储存空间（demo 数据）<br>• 暂停和恢复<br>• 左滑释放<br><br>更新用到 `account-machines` 等标识的现有测试 |
| 跨平台 | Windows 用 GNU target 交叉编译检查，真机跑 `workflow_dispatch`；Linux 在 Docker 环境里验证 systemd 的 disable 和 enable |

所有 e2e 都用 `-count=1`，并在被测的源码树里重新构建 her。

## 8. 分阶段交付

每一阶段一个 PR，前一个合并之后再开始依赖它的下一个。

1. **hermeshd 和协议**：§5.6.1、§5.7.1 到 §5.7.6、§6；Go 测试和 CLI。界面暂时不变，旧的 local 接口转发到新接口。
2. **desktop 设置重构**：§4.1、§4.3、§5.1（desktop 部分）、§5.2、§5.3、§5.5、§5.6.3、§5.7.7、§5.8；Playwright 测试。依赖第 1 步。
3. **保持在线**：§5.4，包括 Linux 的 systemd 调整；`daemon.spec.ts`。可以和第 2 步并行，两步改的文件不重叠（主进程和 `General.tsx` 的启动部分都归这一步）。
4. **iPhone**：§4.2、§5.1（iPhone 部分）、§5.9；UITest。依赖第 1 步，可以和第 2 步并行。
5. **文档和更新说明**：
   - `desktop-app.md` 和 `iphone-app.md` 里的设置结构
   - `cli.md` 补上术语对照
   - `version-skew.md` §6.1 补上 hermeshd 的 `methods`
   - 双语更新说明：项目不再绑定提供方；储存空间；保持在线

## 9. 风险

- **项目限制被清空**：有人原来特意限制过某个项目只给某个提供方用，迁移后限制没了。
  应对：更新说明里写清楚；授权本来就能按 agent 限制提供方。只是表达不了「项目 X 只给提供方 Y」这种组合，这次接受这个损失。
- **白名单误删**：白名单只收公认的依赖和缓存目录名；删除由 `git worktree remove` 完成，git 还会再检查一遍已跟踪的改动。
- **`du` 和 git 扫描很慢**：限制并发；环境页只读缓存；储存空间页打开时显示「正在统计…」，可以随时取消。
- **保持在线的迁移把登录项打开了**：只在用户原来开过保持后台服务时才会这样（D10），之后可以在环境页直接关掉。

## 10. 不做的事

- 不改「授权」的定义和结构（D2）。
- 不提供储存空间里 Hermesh 数据目录的其他部分（附件、日志、Her 数据）；Her 的数据已经有自己的储存空间页。
- 自动释放的天数不做成可配置（D8）。
- 设置窗口不加搜索。

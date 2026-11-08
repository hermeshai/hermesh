# Her Server: the agent lives on a machine you own

Status: decided 2026-11-08, not built. The kernel is in [her/](../her/README.md).

Her is the agent built into Hermesh. It runs on one machine the owner
chooses, the Her Server, and every app (the Mac app, the iPhone app, later
others) is a window onto it. Its conversation, memory, files, model
connections, connectors and skills live only there. The relay stays what it
is: it knows which machines are online, their names and versions, and keeps
sealed messages for whoever is away. It never sees what Her says or does.

## 1. Why

An agent used from several devices needs three things: the same
conversation on every device, models set up once, and work that goes on
while the devices sleep. Four shapes were considered:

| Shape | What goes wrong |
|---|---|
| Her on every machine, each its own, as Claude Code runs in environments | Every machine needs its own model setup, and a ChatGPT login can't be shared: it rotates its refresh token, so each machine's refresh invalidates the others'. History and memory split by machine |
| The relay stores conversations and settings, as Claude's Remote Control keeps transcripts on Anthropic's servers | In plaintext it breaks Hermesh's promise that the relay sees no content. Encrypted, it needs an account-wide data key, rotation when a device is removed, and a recovery key |
| Hermesh runs Her for its users | Hermesh would see everything, and would carry the compute, the isolation between users and the liability |
| **One Her Server per owner** | The owner needs a machine that stays on. That is the cost, taken knowingly |

With a Her Server, models are set up once, on it. One machine holds the
ChatGPT login, so its rotation is harmless. The conversation has one home,
which every device reads. Routines and long work go on all night when the
Her Server is a machine that stays on. And the relay learns nothing new.

## 2. Decision

**Her runs on one machine of the owner's, the Her Server, with everything
of hers. Every app reaches it end to end through the relay, or in process
on that machine itself. The relay only connects.**

1. **One Her per owner, on one machine.** Any macOS or Linux machine paired
   to the owner can be the Her Server: a cloud server, a NAS, a mini PC, or
   a Mac through the Mac app's built-in hermeshd. Windows can't yet (the
   kernel has no Windows code), and iOS never can (no processes, no
   background work).
2. **Everything of Her's lives there**: conversation, memory, the files Her
   makes, routines, model connections and their credentials, connectors (MCP
   servers) and theirs, skills, instructions and permissions (§4).
3. **Every app is a client.** The same conversation and the same settings
   on every device, through the relay end to end, or in process when the app
   runs on the Her Server.
4. **The relay only connects**: presence, names and versions as for every
   machine, and sealed messages while the Her Server is offline (the post
   office, [agent-messages.md](agent-messages.md) §5). A self-hosted relay
   works the same way.
5. **hermeshd runs her** as a child (`her serve --stdio`), supervised and
   updated like the coding agents, and forwards her's own protocol to the
   owner's devices over the end-to-end tunnel (§5).
6. **Settings change only from the owner's trusted devices or on the Her
   Server itself**, through the same API on every device. Secrets go to the
   Her Server and never come back (§6).
7. **Coding agents don't change.** Claude Code, Codex and the rest still
   run on each machine, as today.

## 3. Names

- **Her**: the agent. On the Her Server it is her's default bot (today
  named `HerBot` in [bots.rs](../her/crates/her-server/src/bots.rs); to be
  renamed Her).
- **Her Server**: the machine running Her. A role any machine can take, not
  a program or a kind of hardware, as a Plex Media Server can be any
  computer. It stays a machine like the others and can run coding agents
  too.
- **In the apps** the Her Server is not called a server: the apps already
  use "server" (服务器) for the self-hosted Hermesh service ("使用自己的服务器…"),
  and many owners will run both on one cloud machine. The apps say where Her
  runs: "Her runs on Mac mini" (Her 运行在 Mac mini 上).

## 4. Where things live

| What | Her Server | Owner's devices | Relay |
|---|---|---|---|
| Conversation, memory, files Her makes, routines | The only copy, in `~/.her` | A cache of recent messages, to open at once and read offline | Nothing |
| Model connections, API keys, connector tokens | Its credential store | Status only: connected, which account, until when | Nothing |
| Connectors, skills, instructions, permissions | `~/.her`: the user layer of `config.toml`, `skills/` | Nothing | Nothing |
| Which machine is the Her Server | Its machine settings, signed by a trusted device | The signed setting | The signed setting, which it can't change |
| Messages written while the Her Server is offline | Taken when it is back | Shown as waiting | Sealed, until taken (at most 30 days) |
| Presence | | | Online or offline, name, version, as for every machine |

## 5. Connections and the API

### 5.1 Paths

```
Mac app ─ local IPC ─ hermeshd (owner) ─┬─ this Mac is the Her Server: in process ─ her
                                        └─ relay ─ E2E tunnel ─ hermeshd (Her Server) ─ her
iPhone app ───────────── relay ─ E2E tunnel ─ hermeshd (Her Server) ─ her
```

The relay and the tunnel are the ones every machine uses today
([technical-design.md](technical-design.md) §3.7). The Her Server dials out,
so it needs no public address and no open port. A Mac app on the Her Server
itself never goes through the relay. Direct connections on a local network
are later work (§9).

### 5.2 Forward her's protocol

her already has the methods the apps need, made for FinxWork's desktop app:
the conversation and routines (`bots/*`, `session/op` and its events),
configuration (`config/*`), model connections (`connections/*`, `models/*`),
secrets (`credentials/*`), connectors (`mcp/*`), skills (`skills/*`), web
search keys (`web_search/*`) and browser site permissions (`browser/*`), each
with a change notification. Their types are in
[schema.json](../her/crates/her-protocol/schema.json) and the reference is
[protocol-reference.md](../her/docs/cli/protocol-reference.md).

Declaring some sixty methods again in protobuf would make a second contract
to keep in step with the kernel. So the Her Server's hermeshd forwards her's
JSON-RPC over the tunnel, and adds three things:

- **An allowlist by caller.** The owner's devices get the conversation and
  settings methods. Agents with grants get none in this version. The methods
  of the desktop panels (`doc/*`, `deck/*`, `docx/*`) are not forwarded.
- **Files**: upload to and download from the Her Server, for attachments in
  the conversation and for installing skills (`skills/install` takes a path
  on the Her Server's disk).
- **Her's status**: running or not, her's version, whether a model is set up.

Hermesh's own state (machines, presence, which machine is the Her Server)
stays in protobuf, as today. The apps take her's types from `schema.json`:
TypeScript for the Mac app, Swift for the iPhone app.

### 5.3 Offline

- The owner's hermeshd keeps the recent conversation, as the replica keeps
  sessions ([state-sync.md](state-sync.md) §4). Her opens at once, and an
  offline Her Server still shows what was said.
- A message written while the Her Server is offline waits at the post
  office and reaches Her when it is back. The app shows it as waiting.
- Her's replies reach the iPhone as notices sealed to the phone, as a
  machine's do today ([iphone-app.md](iphone-app.md) §3).

## 6. Settings

### 6.1 One place, on every device

- **Mac app**: Settings → Her, with Models, Connectors (MCP servers, web
  search, browser sites), Skills, Instructions, Permissions and Data (clear
  the conversation, export). The header of Her's conversation opens it.
- **iPhone app**: the same sections under its settings.
- **In the conversation**: when Her needs something, such as a connector to
  sign in to or a login that expired, a card runs the same flow there.
- **First run** asks only for the machine and a model. Connectors and skills
  come when they are wanted.

### 6.2 The same path when this Mac is the Her Server

The Mac app goes through the API even when Her runs on the same Mac, never
to the files, so the iPhone and the Mac always show the same thing. Only
three things differ: the calls stay on the Mac, sign-ins finish in this
Mac's browser with her's own callback, and folders can be shown in Finder.

A Mac that is only a machine ([trusted-devices.md](trusted-devices.md) §4.7)
is not a trusted device, but it is the Her Server's own machine: its app
manages the Her running on it, as it manages the sessions agents run on it,
and the owner's trusted devices manage it like any Her Server. Someone who
edits `~/.her` by hand loses nothing: her announces the change
(`config/changed`, `skills/changed`, `mcp/changed`) and the apps follow it.

### 6.3 Secrets and sign-ins

- **Typed secrets** (API keys, web search keys, connector tokens) go to the
  Her Server with `credentials/set` and never come back. The apps show only
  their status.
- **Sign-ins start and end on the Her Server.** her's ChatGPT login and its
  MCP sign-ins (PKCE with a loopback redirect on 127.0.0.1) wait for the
  browser to call back to the machine running her, which a server with no
  screen can't do. So the Her Server makes the authorization request and
  keeps its PKCE verifier. The client opens the request in its own browser
  and catches the callback: on a loopback port on a computer, through the
  app's own callback on the iPhone. The client then passes the authorization
  code to the Her Server end to end, and the Her Server exchanges it. Tokens
  are made on the Her Server and never leave it.

### 6.4 Who may change what

From another device, only the owner's trusted devices change Her's
settings, with their device key over the tunnel, as they read sessions
today. Changes that let Her run more on the Her Server need the root key
(Touch ID), as other changes that add permissions do
([trusted-devices.md](trusted-devices.md) §4.6). Examples are a connector
started as a command, and turning approvals off. On the Her Server itself,
its own app may change them too: whoever uses that machine can edit
`~/.her` anyway. Agents can't change them.

## 7. The apps

### 7.1 Mac app

- **Sidebar**: Her is its own item under Overview and Search, with an unread
  count. It belongs to no machine or project, so it stays out of the session
  list.
- **Conversation**: messages, files as cards, questions with buttons to
  answer them, progress lines ("comparing four laptops…"), routine results.
  It is a chat, not the step-by-step record of a coding session.
- **First run**, in the main area rather than a sheet:
  1. Choose where Her runs: this Mac (Her goes offline while it sleeps), a
     machine already paired (Windows machines shown as not yet supported), or
     a new server (an install command, paired as Add Machine does).
  2. Connect a model: sign in to ChatGPT, or enter an API key.
- **States**: offline (the cached conversation, a line such as "Mac mini
  offline · seen 3 min ago", messages waiting), an expired login (a card in
  the conversation), files dragged in or pasted, notifications while the app
  is in the background.
- **Settings → Machines** marks the Her Server, and each machine's menu has
  "Run Her on this machine".

### 7.2 iPhone app

The same conversation and settings. Sign-ins use the app's own callback
(§6.3); files come from the Files app or the share sheet.

## 8. Who can reach Her, and security

- **Keys decide, not accounts.** The Her Server is one of the owner's
  machines, paired with a certificate a trusted device signed. On every
  connection it checks the other side itself, against the owner's root
  chain it pins ([technical-design.md](technical-design.md) §3.7); the
  relay's admission only keeps out abuse. So only the owner's trusted
  devices reach Her, and the Her Server's own app. A new device becomes
  trusted only when one of the owner's devices approves it
  ([trusted-devices.md](trusted-devices.md) §4.2): someone holding the
  owner's account alone reaches nothing. Agents can't reach Her in this
  version.
- **Accounts.** On the official relay the owner signs in to hermesh.io. The
  account lets the relay put the owner's devices and machines together and
  lets a new device find its owner; it is not a key. With a self-hosted relay
  there is no hermesh.io account: devices join with the connection address.
  The Her Server goes by keys alone, so it works the same with either relay.
- **One owner per Her Server.** Sharing Her with other people is not in this
  version (§9).
- **Every device lost.** The owner starts over as today
  ([trusted-devices.md](trusted-devices.md) §4.5) and pairs the machine
  again. Her's conversation and settings are on that machine, and pairing it
  again must keep them.
- **A compromised relay** can't read the conversation or settings, and
  can't send the apps to another machine, since the Her Server setting is
  signed. It can withhold traffic, as it can today.
- **A compromised Her Server** gives away all of Her's data and
  credentials. It needs no open port, updates itself like every machine, and
  should use disk encryption. This is the trade for keeping everything on a
  machine the owner controls.

## 9. Not doing now

- Her working on the owner's other machines (starting coding sessions,
  reading files there) through Hermesh's tools.
- More than one Her Server, and moving Her to another machine.
- Windows as a Her Server; computer use on Linux (her's desktop tools use
  the macOS accessibility API).
- Hermesh running Her for its users.
- Sharing Her with other people, such as a family or a team.
- Direct connections on a local network.

## 10. Open questions

- Sign-in from the iPhone for providers that accept only loopback
  redirects.
- How large a file may go through the relay.
- The backup and export format of `~/.her`.

## 11. Steps

Each step ends with an end-to-end test made from what a person does,
through the real apps and the relay.

1. hermeshd runs her on macOS and Linux; the Her Server setting; forwarding
   with the allowlist; the Mac app's conversation and first run, with this
   Mac as the Her Server.
2. A Her Server on another machine through the relay; files; the settings
   pages; sign-ins started on the Her Server.
3. The iPhone app.
4. The Linux install command, export and backup, moving Her.

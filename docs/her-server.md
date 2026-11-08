# Her Server: Her runs on machines you own, the apps only talk to it

Status: decided 2026-11-08, not built. Revised the same day: the apps only
converse, each server has its own console, and an owner may run several Her
Servers from the start. The kernel is in [her/](../her/README.md).

Her is the agent built into Hermesh. It runs on Her Servers: macOS or Linux
machines the owner chooses. Each Her Server is an environment of its own,
with its assistants, conversations, model logins, connectors and skills, and
a web console to manage them. The apps only converse: one entry, Her 助手,
where the owner picks an environment and an assistant and talks, as one picks
where a session runs in Claude. The relay stays what it is: it knows which
machines are online, their names and versions, and keeps sealed messages for
whoever is away. It never sees what Her says or does.

## 1. Why

An agent used from several devices needs the same conversation on every
device, settings that don't wait for every app's release, and work that goes
on while the devices sleep. Owners also want to keep some things apart: work
and personal data, or a Mac's apps and an always-on Linux machine. The
shapes considered:

| Shape | What goes wrong |
|---|---|
| Her on every machine, by itself | Every machine needs a setup nobody asked for, and Windows machines can't run her |
| The relay stores conversations and settings, as Claude's Remote Control keeps transcripts on Anthropic's servers | In plaintext it breaks Hermesh's promise that the relay sees no content. Encrypted, it needs an account-wide data key, rotation when a device is removed, and a recovery key |
| Hermesh runs Her for its users | Hermesh would see everything, and would carry the compute, the isolation between users and the liability |
| Her's settings inside the apps | Every new setting waits for the Mac, iPhone and Windows releases; an older app can't show a newer server's settings; the same screens are written once per app |
| **Her Servers the owner chooses, each with its own console; the apps only converse** | Each environment is set up on its own, with its own model login. Work at night needs a machine that stays on |

Each environment keeps one ChatGPT login of its own, so the token rotation
that breaks a login shared between machines never comes up. A conversation
lives in one environment, which every device reads. Settings ship with the
server that has them, so no app can be out of step with them. And the relay
learns nothing new.

## 2. Decision

**Her runs on Her Servers, machines of the owner's, each a self-contained
environment with its own console. The apps only converse, and reach each
environment end to end through the relay. The relay only connects.**

1. **Her Servers.** Any macOS or Linux machine paired to the owner can run
   Her once the owner turns it on for that machine: a cloud server, a NAS, a
   mini PC, or a Mac through the Mac app's built-in hermeshd. One or more;
   most owners have one. Windows can't yet (the kernel has no Windows code),
   and iOS never can (no processes, no background work).
2. **Each Her Server is an environment of its own**: its assistants (Her and
   any the owner adds), their conversations, memory, files and routines, its
   model logins and credentials, connectors (MCP servers), skills,
   instructions and permissions (§4). Nothing is shared or synced between
   environments.
3. **The apps only converse.** One entry, Her 助手, built on FinxWork's bot
   design (§7). Picking an environment and an assistant is the only choice
   there.
4. **Each server has its own web console.** A Her Server's console manages
   that environment (§5.3). The relay's console is the hermesh.io console on
   the official service. They are never merged, even on one machine: the
   relay must never see Her's content, and the two often run on different
   machines.
5. **The relay only connects**: presence, names and versions as for every
   machine, and sealed messages while a Her Server is offline (the post
   office, [agent-messages.md](agent-messages.md) §5). A self-hosted relay
   works the same way.
6. **Keys decide** who reaches a Her Server and its console (§8). Anything
   that needs the root key (Touch ID) is signed in the apps.
7. **hermeshd runs her** as a child (`her serve --stdio`), supervised and
   updated like the coding agents. It forwards her's own protocol (§5.2) and
   serves the console through the end-to-end tunnel.
8. **Coding agents don't change.** Claude Code, Codex and the rest still run
   on each machine, and the rest of the apps stays as it is.

## 3. Names

- **Her**: the agent, and the default assistant of every Her Server (her's
  default bot, today named `HerBot` in
  [bots.rs](../her/crates/her-server/src/bots.rs); to be renamed Her).
- **Assistant** (助手): a named bot on a Her Server. The apps say 助手, since
  "agent" already means coding agents and connected AIs in Hermesh, and her
  calls its own sub-agents agents too.
- **Her Server**: a machine running Her. A role any machine can take, not a
  program or a kind of hardware. It stays a machine like the others and can
  run coding agents too.
- **Environment**: a Her Server as the apps show it, named after its machine
  (the hostname by default, renamable): "Mac mini". The apps don't say
  "server" (服务器) for it, since they already use that word for the
  self-hosted Hermesh service ("使用自己的服务器…").
- **Console**: a server's web pages for managing it.

## 4. Where things live

| What | Each Her Server | Owner's devices | Relay |
|---|---|---|---|
| Assistants, conversations, memory, files Her makes, routines | The only copy, in `~/.her` | A cache of recent messages per environment, to open at once and read offline | Nothing |
| Model logins, API keys, connector tokens | Its credential store | Status only: connected, which account, until when | Nothing |
| Connectors, skills, instructions, default permissions | `~/.her`: the user layer of `config.toml`, `skills/` | Nothing | Nothing |
| Whether a machine runs Her | Its machine settings, signed by a trusted device | The signed setting | The signed setting, which it can't change |
| Messages written while a Her Server is offline | Taken when it is back | Shown as waiting | Sealed, until taken (at most 30 days) |
| Presence | | | Online or offline, name, version, as for every machine |

## 5. Connections, the API and the console

### 5.1 Paths

```
Mac app ─ local IPC ─ hermeshd (owner) ─┬─ this Mac runs Her: in process ─ her
                                        └─ relay ─ E2E tunnel ─ hermeshd (each Her Server) ─ her
iPhone app ───────────── relay ─ E2E tunnel ─ hermeshd (each Her Server) ─ her
```

The relay and the tunnel are the ones every machine uses today
([technical-design.md](technical-design.md) §3.7). A Her Server dials out,
so it needs no public address and no open port. A Mac app on a Her Server
itself never goes through the relay to it. Direct connections on a local
network are later work (§9).

### 5.2 Forward her's protocol

her already has the methods the apps and the console need, made for
FinxWork's desktop app: assistants and their conversations (`bots/*`,
`session/op` and its events), configuration (`config/*`), model connections
(`connections/*`, `models/*`), secrets (`credentials/*`), connectors
(`mcp/*`), skills (`skills/*`), web search keys (`web_search/*`) and browser
site permissions (`browser/*`), each with a change notification. Their types
are in [schema.json](../her/crates/her-protocol/schema.json) and the
reference is [protocol-reference.md](../her/docs/cli/protocol-reference.md).

Declaring some sixty methods again in protobuf would make a second contract
to keep in step with the kernel. So each Her Server's hermeshd forwards
her's JSON-RPC over the tunnel, with:

- **An allowlist by caller.** Her 助手 in the apps gets the assistants and
  their conversations. The console gets the settings methods. Agents with
  grants get none in this version. The methods of FinxWork's document panels
  (`doc/*`, `deck/*`, `docx/*`) are not forwarded.
- **Files**: upload to and download from a Her Server, for attachments in
  conversations and for installing skills (`skills/install` takes a path on
  the Her Server's disk).
- **Status**: whether her runs, its version, whether a model is set up.

Hermesh's own state (machines, presence, which machines run Her) stays in
protobuf, as today. The apps take her's types from `schema.json`: TypeScript
for the Mac app, Swift for the iPhone app.

### 5.3 The console

A web app that ships with her, built on FinxWork's Customize page (whose
renderer already builds for a plain browser, today against a mock server),
served by the Her Server's hermeshd. It manages that environment: model logins and API keys,
connectors, skills, web search, browser sites, instructions, the default
permissions of new assistants, export.

How it is reached, in order:

1. **From the Hermesh app** (first): "Manage this environment" opens it
   through the tunnel, as the conversation goes. No password, no open port:
   it acts as the owner's device that opened it. On a Mac that runs Her, at a
   local address.
2. **From any browser** (later): a trusted device scans a code the page
   shows and approves that browser for a limited time, as a phone approves a
   web chat session. Still a device's approval, never a password.
3. **From the internet without the app** (later): the relay forwards the
   encrypted connection without opening it, to a name and certificate of the
   Her Server's own, as Home Assistant's remote access works.

A change that needs the root key is not signed in the page: the console
asks, and the app's signer shows exactly what will be signed and asks for
Touch ID, as signing requests from hermeshd work today.

### 5.4 Offline

- The owner's hermeshd keeps the recent conversations of every environment,
  as the replica keeps sessions ([state-sync.md](state-sync.md) §4). An
  assistant opens at once, and an offline environment still shows what was
  said.
- A message written while a Her Server is offline waits at the post office
  and reaches it when it is back. The app shows it as waiting.
- Replies reach the iPhone as notices sealed to the phone, as a machine's do
  today ([iphone-app.md](iphone-app.md) §3). With more than one environment,
  a notice names the environment too.

## 6. Settings: where each is changed

| Setting | Where |
|---|---|
| Which machines run Her | The app, signed by a trusted device: Her 助手's first run, "Add environment", or Settings → Machines |
| Model logins, API keys, connectors, skills, web search, browser sites, instructions, default permissions for new assistants | That environment's console |
| An assistant's name, avatar, model, thinking, approvals, clearing its conversation, deleting it | The app, in the assistant's settings panel (§7.1) |
| Routines | The conversation, as FinxWork's routine design has them |

**Secrets and sign-ins.** Typed secrets (API keys, connector tokens) go to
the Her Server with `credentials/set` and never come back; pages and apps
show only their status. Sign-ins start and end on the Her Server: her's
ChatGPT login and its MCP sign-ins (PKCE with a loopback redirect on
127.0.0.1) wait for a browser to call back to the machine running her, which
a server with no screen can't do. So the Her Server makes the authorization
request and keeps its PKCE verifier; the owner's browser signs in, the
callback is caught on the owner's device, and the authorization code goes to
the Her Server end to end, which exchanges it. Tokens are made on the Her
Server and never leave it.

**Who may change what.** From another device, only the owner's trusted
devices, with their device key over the tunnel, as they read sessions
today. Changes that let Her do more on a Her Server need the root key
(Touch ID), as other changes that add permissions do
([trusted-devices.md](trusted-devices.md) §4.6): a connector started as a
command, an assistant switched to full access. On a Her Server itself, its
own app and console may change them too: whoever uses that machine can edit
`~/.her` anyway. Agents can't change them.

## 7. The apps

### 7.1 Her 助手

One entry in each app, for conversation only. In the Mac app it is one item
in the sidebar under Overview and Search; the rest of the app is unchanged.
Its pages and behaviour are FinxWork's bot mode: the assistant list, adding
and editing assistants, the default assistant, the conversation with its
messages, files, questions, the last bubble while it works, messages sent in
a row, its steps, the moving avatar, the approval window, the input box, the
blank page of a new assistant, menus, unread marks and alerts, and routines.

What changes, because FinxWork assumed Her ran on the same Mac:

- **Environments.** The assistant list shows one environment at a time, with
  a switcher above it, as Claude lets one pick where a session runs. Each
  environment shows whether it is online and its unread count. The app
  remembers the last one. With a single environment the switcher is not
  shown.
- **Models.** An assistant picks only from the models its environment can
  already use. Adding a key or a login is in the console; the model menu
  links there.
- **Permissions.** New assistants ask before risky steps. Switching one to
  full access needs Touch ID (§6). FinxWork gives every bot full access by
  default; that is not carried over, because a Her Server reads web pages
  and messages from anyone.
- **Files.** Her's files are on its Her Server: the app previews or saves
  them through the tunnel, and files dragged into a conversation are
  uploaded there.
- **Not carried over**: the switch between bot mode and session mode (the
  apps have Hermesh's own sessions), the built-in browser (Her browses on its
  Her Server), and the document panels beside the conversation.

### 7.2 First run

In Her 助手, while no machine runs Her:

1. Choose where Her runs: this Mac (it goes offline while the Mac sleeps), a
   Mac or Linux machine already paired (Windows machines shown as not yet
   supported), or a new server (an install command, paired as Add Machine
   does). Touch ID turns Her on there.
2. That environment's console opens to connect a model: sign in to ChatGPT,
   or enter an API key.
3. Her greets the owner.

"Add environment" in the switcher runs the same two steps for another
machine.

### 7.3 States

- **Offline environment**: its cached conversations, a line such as "Mac
  mini offline · seen 3 min ago", messages waiting.
- **Expired login**: a card in the conversation; the fix is in the console.
- **Notifications** while the app is in the background, naming the
  assistant, and the environment when there is more than one.

### 7.4 iPhone app

The same design, written in Swift (FinxWork has no iPhone app). Sign-ins
catch their callback in the app; files come from the Files app or the share
sheet.

## 8. Who can reach Her, and security

- **Keys decide, not accounts.** A Her Server is one of the owner's
  machines, paired with a certificate a trusted device signed. On every
  connection it checks the other side itself, against the owner's root chain
  it pins ([technical-design.md](technical-design.md) §3.7); the relay's
  admission only keeps out abuse. So only the owner's trusted devices reach
  Her and its console, and the Her Server's own app. A new device becomes
  trusted only when one of the owner's devices approves it
  ([trusted-devices.md](trusted-devices.md) §4.2): someone holding the
  owner's account alone reaches nothing. Agents can't reach Her in this
  version.
- **Accounts.** On the official relay the owner signs in to hermesh.io. The
  account lets the relay put the owner's devices and machines together and
  lets a new device find its owner; it is not a key. With a self-hosted relay
  there is no hermesh.io account: devices join with the connection address.
  A Her Server goes by keys alone, so it works the same with either relay.
- **One owner per Her Server.** Sharing Her with other people is not in this
  version (§9).
- **Every device lost.** The owner starts over as today
  ([trusted-devices.md](trusted-devices.md) §4.5) and pairs the machines
  again. Each environment's assistants, conversations and settings are on its
  machine, and pairing it again must keep them.
- **A compromised relay** can't read conversations or settings, and can't
  add an environment or send the apps to another machine, since whether a
  machine runs Her is signed. It can withhold traffic, as it can today.
- **A compromised Her Server** gives away that environment's data and
  credentials, and nothing of the others. It needs no open port, updates
  itself like every machine, and should use disk encryption.

## 9. Not doing now

- Her working on the owner's other machines (starting coding sessions,
  reading files there) through Hermesh's tools.
- Environments talking to each other, sharing settings, or moving a
  conversation from one to another. Copying the settings of one environment
  into a new one, secrets left out, may come later.
- Windows as a Her Server; computer use on Linux (her's desktop tools use
  the macOS accessibility API).
- Hermesh running Her for its users.
- Sharing Her with other people, such as a family or a team.
- The console in any browser and from the internet without the app (§5.3).
- A console for the self-hosted relay.
- Direct connections on a local network.

## 10. Open questions

- Sign-in from the iPhone for providers that accept only loopback
  redirects.
- How large a file may go through the relay.
- The backup and export format of `~/.her`.

## 11. Steps

Each step ends with an end-to-end test made from what a person does,
through the real apps and the relay.

1. hermeshd runs her on macOS and Linux; turning Her on for a machine,
   signed; forwarding with the allowlist; the console through the tunnel,
   with models and API keys; Her 助手 in the Mac app with FinxWork's bot
   pages and the environment switcher, tested with this Mac and a Linux
   machine as two environments.
2. Files; the rest of the console (connectors, skills, web search);
   sign-ins started on the Her Server; routines.
3. The iPhone app.
4. The Linux install command, export and backup.

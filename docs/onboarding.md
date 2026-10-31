# Onboarding without a Mac

> Status: designed 2026-10-28; the owner took every recommendation in §12
> the same day. Built the same day, all seven steps of §10 with the tests
> of §11; what is left is listed after §10's table. It builds on
> [trusted-devices.md](trusted-devices.md), [iphone-app.md](iphone-app.md)
> and [mcp-gateway.md](mcp-gateway.md). Getting the iPhone app onto phones
> (TestFlight, then the App Store) is the owner's and is left out.

In one sentence: **someone with only an iPhone signs in, runs one command on
a Linux server, scans what the server shows, connects their agent and sees
its first task, in about five minutes; later a Linux computer can approve
too.**

## 1. Goal

Today's first run assumes a Mac. The Mac is the first trusted device and the
first machine, and it is always on, so every request finds it. Without a
Mac:

- The **iPhone** is the trusted device. It signs in, approves with Face ID
  and shows everything, and it runs nothing (iphone-app.md).
- **Machines** are elsewhere (a VPS, a Linux box at home, a Mac mini), each
  added from its own terminal.
- **Personal agents** connect as they do today (Grok Bot, ChatGPT, Claude,
  OpenClaw, or an agent on the Linux server itself). They hand work to the
  coding agents on those machines.
- Later, **a Linux computer** can be a trusted device as well (§9).

What done means, for the build and for the demo:

1. From opening the app to a coding agent's first answer on the server takes
   about five minutes without help, given a coding agent already signed in
   there. Installing one adds its own steps.
2. Nothing long is carried between devices by hand. The install command is
   the same for everyone, the phone scans what the terminal shows, and the
   only thing typed across is two digits.
3. Every request to connect reaches the phone, whether Hermesh is open or
   not.
4. No screen, terminal line or web page says "your Mac" where any trusted
   device would do.
5. The security model stays as it is (§3).

Out of scope: Android, Windows, an iPad layout, starting sessions from the
phone (iphone-app.md §7) and installing the app.

## 2. Where it breaks today

Read in main 44c911e, not run:

| Step | Today | Where |
|---|---|---|
| Setting up on the iPhone | Works on the official service. An own server can only be joined, not claimed. No end-to-end test sets an owner up on a phone (TestIPhone joins one) | `OnboardingView` |
| After setup | An empty overview with no next step. The Mac's first run goes on to its coding agents, connecting an agent and a first task | `OverviewView`; the desktop's `firstrun/` |
| Adding a machine | The phone shows a command with the 26-character owner id for the person to carry to the terminal. The console sends people back to the Mac app for it | `AddMachineSheet` |
| Adding one without an address | After the owner approves, the machine asks a y/N question on stdin. Under `curl … \| sh`, stdin is the script, so it can't ask and gives up after the owner's device signed its cert. Not tested: the tests pass `--yes` | `TerminalConfirm` in `internal/machine/pair.go` |
| Installing as root | Refused, with no way forward | `scripts/install.sh` |
| The background service | `systemctl --user` runs before `enable-linger`, so it fails after `su`. There is no fallback without systemd | `internal/service/service.go` |
| Coding agents | Found and shown as "Not signed in", with "run `claude` in Terminal". Nothing says how to sign in on a server without a browser | `MachineAgentView`, `hermeshd doctor` |
| Checking them again | The phone's pull to refresh calls `CheckAgents`, which covers only the device's own machine, and a phone has none. A remote machine can't be asked to look again (`GetInfoRequest` is empty) | `internal/owner/localapi.go` |
| Projects | A path typed on the phone, and nothing on the machine itself | `AddProjectSheet` |
| Connecting an agent | The iPhone sends the old message with the address, even on the official service (the Mac sends the /connect line) | `ConnectAgentSheet`; the desktop's `personal.ts` |
| An agent's pairing link | Claimed in the console ("send it to my Mac"), which wakes the devices that are online and pushes nothing | `ClaimPairing` in `pkg/relay` |
| MCP apps | The approval request is made only while a trusted device is connected. An iPhone is connected while open and for 5 minutes after | `refresh` and `OwnerOnline` in `pkg/mcpgw` |
| The first task | None on the iPhone | |
| Wording | "Approve it on your Mac", "Touch ID" and "Hermesh.app" in the terminal, the CLI, the MCP pages, the console and /connect | §8 |
| One trusted device | Losing the phone means setting up again, and nothing says so | |

## 3. What does not change

- **Approvals stay on trusted devices.** Each one uses the device's own key
  and the person's presence (Face ID, Touch ID, a PIN). The console and the
  center approve nothing.
- **Machines stay strict.** Their owner is pinned by the connection address,
  or proven by two digits from the person's own device. Agents that pair
  through a link trust the center once, as decided for pairing links.
- **The center learns nothing new.** Pushes carry sealed notices, and the
  folders offered as projects go end to end.
- **Nothing runs unasked.** Coding agents and folders are read when a page
  needs them, never on a timer.
- **One decision, one confirmation** (trusted-devices.md 4.6).
- **Few words**, and nothing on screen twice.

## 4. The journey

| # | On the iPhone | On the Linux server | Underneath |
|---|---|---|---|
| 1 | First time using Hermesh, the browser sheet; Create your management key (Face ID); Turn on notifications | | `Setup` with the link code (unchanged) |
| 2 | The guide: Where does your code live? Linux | | §5.2 |
| 3 | The install command, Copy, Share, Scan the QR Code | Paste the command (from the sheet, hermesh.io or the README) | §6.1 |
| 4 | | A QR and the pairing code | An unpinned machine request with `confirm_match` (§6.3) |
| 5 | Scan: "A machine calling itself vps-1 wants to join". Name it, Face ID | "Claimed by a…@example.com. Approve it on your iPhone" | The phone claims the code (`LookupPairing`) |
| 6 | "Type these digits on vps-1: 47" | "Approved on Alex's iPhone. Type the two digits it shows: 47". It pairs, starts the service and checks the coding agents | `JoinMatchCode` of the machine cert |
| 7 | Coding agents: Claude Code signed in; its page has Test and Add a Project, which lists the git repositories in the home folder | Or `hermeshd project add ~/app` | §5.5, §6.7 |
| 8 | Connect your agent. ChatGPT gets the connector URL, Grok Bot the /connect line | The agent signs in or asks to connect | §5.6 |
| 9 | A notification: "ChatGPT wants to connect". Choose its scope, Face ID | | §7 |
| 10 | Try it: "Send ChatGPT this: Use Hermesh to have Claude Code say hello to me in the app project". Its session and answer show as it runs | Claude Code runs | §5.7 |

What crosses devices by hand: one command anyone can copy, one scan and
two digits.

## 5. The iPhone

### 5.1 First run

It opens on a page of its own the first time, after signing out and after
Erase Everything, and it splits by what the person says first:

```
 ⋰ Hermesh
 Let your agents
 talk to each other

 (Grok Bot, in the cloud)  #142: someone says sign-in does nothing…
        Found it, and the plan is written. Codex…  (Claude Code, your Mac mini)
                     Fixed: PR #143, all tests pass.  (Codex, your server)
 (Grok Bot)  Good, I'll get you to merge it.

 ✦ First time using Hermesh                 ›
 ▭ Already using it on another device       ›
 Use Your Own Server…
```

- One screen says what Hermesh is for, without pages to swipe: agents in
  the cloud (left) and on your computers (right) talk, a line after another,
  then again from the start (`WelcomeChat`). A small iPhone shows the first
  three lines; with Reduce Motion they all show at once. The story itself
  is the website's.
- Either card opens the sign-in at once. What comes back decides the rest,
  so a wrong card is corrected rather than refused: the first time on an
  account that already has trusted devices says "This account is already in
  use" and leads to joining; another device on a new account says "This is
  a new account" and leads to setting up here. Use Another Account signs in
  again with nothing kept from the last.
- **First time.** Create your management key: a diagram of what the key
  does (a computer, an agent or a device asks through the server, the
  approval is signed here, and the server only relays) and three lines (it
  stays on this iPhone; everything that joins needs Face ID; the server
  can't approve). Create with Face ID runs `Setup`. Never call it an
  encryption key: it authorizes, and content is end to end by other keys.
- **Another device.** Join your Hermesh: what joining gives, a diagram (this
  iPhone's key, the server that only relays, the other device that approves)
  and three lines (a management key made here; Face ID signs the request, so
  the server can't pretend to be this iPhone; compare the code and approve).
  Nothing goes out before Send Request with Face ID: iOS shows no reason on
  its Face ID prompt, so the page says it first. A cancelled Face ID goes back
  to the button without an error (the key page does the same). Once signed,
  the page shows the code to compare; the other device's name is only
  known once it approves (`WaitJoinResponse.approved_by`), so the digits page
  names it. No other device? says that only setting up again is left.
- **Turn on notifications**, last on both paths: a small lock screen with
  one notice of each kind (a request to connect, a question with its
  answers, a session an agent started, a turn done) and "Notifications are
  end-to-end encrypted; the server can't read them". Turn On Notifications
  asks iOS; Not Now goes on without.
- Pages that belong to the guide carry a thin progress bar in the navigation
  bar, with no labels: the account, a computer, coding agents, connecting.
- If notifications are turned down, a To Do row stays until they are on:
  "Notifications are off", with Turn On… opening the app's page in Settings.

### 5.2 The guide, and Get Started

Only an iPhone that set the owner up here runs the guide, full screen,
after the first run; one that joined an owner goes straight to the
Overview. The guide's pages:

| Page | What it does |
|---|---|
| Where does your code live? | Mac or Linux; Set Up Later puts the guide off |
| Install Hermesh on your Mac | The download link (Copy Link, AirDrop…), then signing in with the same account (or the connection address on one's own server); the Mac's request opens to approve, then its two digits, until it joins |
| Install Hermesh on Linux | The install command, then Scan the QR Code or Enter the Pairing Code… (one's own server: the pinned command, whose request comes by itself); the same approval, digits and join |
| Coding agents | Those found on that computer, each opening its page (§5.5: Test, default model, projects and Add a Project…); the others Hermesh works with, by logo; Rescan after installing one there; Continue |
| Connect your agent | As Connect Your Agent (§5.6), its request opening by itself, for all projects |
| Try it | The first task (§5.7), and the session it starts with its answer; Add a project first when there is none; Done ends the guide |

- Each step that waits for a request opens it to approve as it comes; once
  approved the guide goes on to what joined.
- A Mac that joins an owner (from this guide or any other way) only sets
  itself up in its own first run: its coding agents and projects, then Done.
  Connecting an agent and the first task are the account's, so it leaves
  them to the device that set the owner up.
- Later puts the guide off. Then, and only then, Get Started sits on the
  Overview with what the guide left, and Continue goes back into it:

```
Get Started                                    ⋯
 ✓ Connect your computer
 ● Add a project                       [Continue]
 ○ Connect your agent
 ○ Try it
```

- Done rows show a check, from the world the phone follows. All done, or ⋯ ›
  Hide, and it is gone for good: a row that goes back to undone later (a
  machine back from a reconnect reports its coding agents a moment after
  it) does not bring it back.
- Where the guide stands is kept on this phone for its owner: another owner,
  or Erase Everything, forgets it.

### 5.3 Add a Machine

```
Add a Machine                                              ✕
[  Mac  ] [ Windows ] [ Linux ]      tiles: the Apple and Windows logos, a terminal
[ Install the App | Terminal Command ]      Linux: the app is greyed out
Agents use Claude Code and Codex on it.

┌ The app (a Mac or a PC) ─────────────────────────────────┐
│ ① On that Mac, open                                      │
│   hermesh.io/download                       (copy) (send)│
│ ② Install it, open it, and sign in with the same account │
│   (an own server: "…and join", with its address)         │
└──────────────────────────────────────────────────────────┘
Waiting for that machine to connect…      Enter a Pairing Code

┌ The command ─────────────────────────────────────────────┐
│ ① On that machine, run                                   │
│   curl -fsSL https://github.com/hermeshai/hermesh/…/install.sh | sh
│   As the user you code as. It shows a code. (copy) (send)│
│ ② Scan the code it shows                                 │
│   [ Scan the Code ]                                      │
│   Can't Scan? → Enter a Pairing Code                     │
│                 Install from a Terminal on This iPhone   │
└──────────────────────────────────────────────────────────┘
```

- **The system, then how.** A Mac and a Windows PC have the app as well as
  the command; Linux has only the command, which the picker shows by
  greying out the app. The system is three tiles on the iPhone as in the
  desktop app's sheet, the steps numbered in one card, and what is waited
  for at the bottom (beside Done on the desktop).
- **Can't scan?** On the official service the command shows a code to
  scan. Typing its pairing code, or a terminal on this very iPhone (the
  command then carries this account's address, and the request comes here
  by itself), are behind one "Can't Scan?" menu, not lines of their own.
- **The app's way** names a page to open on that computer, not a download
  here: the installer belongs on the other machine. `hermesh.io/download`
  offers the build for its system and chip. That app then signs in to the
  same account (or joins the own server), as a trusted device or a machine
  only (trusted-devices.md 4.7); either request opens here.
- The command is the same for everyone, so it can come from this sheet,
  from hermesh.io or from the README, on whatever computer has the
  terminal.
- Scan the Code opens the camera in the sheet (VisionKit's data scanner)
  and reads the terminal's QR (§6.2). A pairing code typed by hand does the
  same.
- Today's command, with the connection address in it, stays one tap away,
  for a terminal on this phone (an SSH app), where there is nothing to scan.
  It needs no digits, because the owner is in the command. Its label doesn't
  say "address", following the decision to keep the address out of the
  official service's screens.
- A Scan item in the New menu reads any Hermesh code: a machine's, or an
  agent's link (§7.3).
- A camera prompt needs its usage string, in both languages.

### 5.4 Approving a machine

- A machine whose code was scanned or typed on this phone has nothing to
  compare. The code is derived from the machine's key (60 bits), and the
  phone already checks that the request the relay returns matches it
  (`pairingDetails`), so the relay cannot swap the key. The approval sheet
  asks only for a name, then Face ID.
- A machine that came in pinned (the command with the address) is unchanged.
  Its request reaches the phone by itself, with no code to check it
  against, so the person compares the verification code.
- After Face ID, a request that asked for the two digits (`confirm_match`)
  shows them on the sheet the signer already has (`MatchSheet`). Today it
  says "Type these digits on the new device" and waits for Done. For a
  machine it changes in three ways:
  - it names the machine: "Type these digits on vps-1";
  - it closes itself once the machine connects ("vps-1 joined");
  - if the machine never connects, it offers to remove it.

### 5.5 A machine's page as its setup

- **Coding agents**: each one is ready, or the page says what to do on that
  machine.
  - Not installed: a link to its install page.
  - Not signed in: the command to run there, plus "On a server without a
    browser, it shows a link: open it on this iPhone."
  - Check Again asks that machine to look now: `GetInfoRequest` gains
    `recheck`, sent end to end. Today's `CheckAgents` reaches only the
    device's own machine. Nothing checks on a timer.
- **Projects**: Add a Project lists the git repositories the machine finds
  under its home folder. This is a new end-to-end call, made when the sheet
  opens: three levels deep, skipping dependency and cache folders.
  - Picking one fills in the name and makes it a worktree project.
  - Browse Other Folders… opens the machine's folders one screen at a
    time (`ListFolders`, end to end, read when opened): each marked when it
    is a git repository, Choose at the bottom. The name follows the folder;
    How Agents Work shows only for a repository, its base branch picked
    from the repository's own branches. Hidden folders and Type a Path… are
    in the menu; a machine from before `ListFolders` takes a typed path.
  - On a Mac machine the search skips the folders macOS protects (Desktop,
    Documents, Downloads, iCloud Drive, other volumes): reading them would
    put a privacy prompt on a screen nobody may be watching. Browsing into
    one says so first, since that prompt shows on the Mac.
- **Version**: when a machine's Hermesh is older than the phone's, its
  version row has an Update button that opens the install command that
  updates it (a 0.0.0 build of no release never shows it). Machines also
  update themselves ([machine-updates.md](machine-updates.md)).
- **Offline**: the page shows only what still holds (its sessions, its
  version) and one row, How to Reconnect, which opens the command to check
  it with and the last error. Renaming and removing are in the bar's menu.

### 5.6 Connect Your Agent

- On the official service, the sheet gives the /connect line, as the Mac
  does (`personal.ts`): "Connect to Hermesh: follow https://hermesh.io/connect".
  MCP apps also get the connector URL. On an own server, the sheet keeps
  today's message with the address.
- On the official service, Account › About stops showing the connection
  address, as on the Mac.
- A request that comes in arrives as a notification (§7).

### 5.7 The first task

- After the first grant is approved, the approval ends on the Mac's first
  task page. It says what to send the agent: "Use Hermesh to have Claude
  Code say hello to me in the app project", with Copy and Share.
- That grant's first session opens as soon as it appears. Continue closes
  the page. In the guide, Try it shows that session and its answer in place.
- **Test** (decision 2) is a button on a coding agent's page. It runs one
  short fixed turn ("Reply with the single word ready.") in a scratch folder
  and shows the answer. This proves the install and the sign-in before any
  personal agent is connected.
  - It is not a session: nothing is kept, and no project or grant is used.
  - It is a new owner-only call to the machine
    (`MachineService.TestAgent`), sealed end to end like the machine's other
    settings.

### 5.8 A second trusted device

Once the first machine joins and this phone is the only trusted device, a
To Do row appears: "Only this iPhone can approve. If you lose it, you'll set
up again and pair your machines and agents again." It offers Add Another…
(another iPhone, a Mac, later a Linux computer, §9) and Hide.

### 5.9 Your own server

Use Your Own Server… takes a relay's address and its claim code
(`SetupRequest.claim_code`), as the Mac does, besides joining with a
connection address. The relay's banner says "Enter it in the Hermesh app",
not "Hermesh.app".

## 6. Linux: the first install

### 6.1 The command

```
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh
```

- **It stays on GitHub.** The script carries the key that checks the
  downloads, so whoever serves the script decides what runs. A short URL on
  hermesh.io would hand that to the center (decision 4).
- **With a connection address after it** (`sh -s -- connect.hermesh.io/o_…`),
  the owner is pinned and nothing changes from today except the words (§8).

### 6.2 What the terminal shows

```
› downloading hermeshd-linux-amd64, hermesh-linux-amd64
✓ installed to /home/dev/.local/bin

Pair this machine with your Hermesh

    █▀▀▀▀▀█ ▄▀▄█ █▀▀▀▀▀█
    █ ███ █ ▀█▄▀ █ ███ █      Scan with Hermesh on your iPhone,
    █ ▀▀▀ █ █▀ ▄ █ ▀▀▀ █      or open Hermesh › Add a Machine ›
    ▀▀▀▀▀▀▀ ▀ █▄ ▀▀▀▀▀▀▀      Enter the pairing code:
    …                         K7QM-2XPA-9RTD

Waiting…
✓ Claimed by a…@example.com: approve it on your iPhone
✓ Approved on “Alex’s iPhone”
  Type the two digits it shows: 47
✓ Paired as “vps-1”
✓ Background service on: starts with the server, keeps running after you log out

Coding agents
  claude   2.1.3   signed in
  codex    not installed: https://…
Next: add a project here (hermeshd project add <folder>) or on your iPhone.
```

- **The QR**: drawn with half-block characters (a small encoder such as
  `rsc.io/qr`) when the output is a terminal wide enough for it. The code is
  always printed.
- **The digits**: read from `/dev/tty`, so they work under `curl | sh`.
  - Three wrong tries end it, and nothing is installed.
  - The phone's "Type 47" page then offers to remove the machine it approved.
- **No terminal** (a script, cloud-init): `--yes` takes the center's word,
  as today, or the command takes a connection address.
- **Names**: the approving device is named from the root chain (its key's
  label). Before the claim, the text says "Hermesh on your iPhone or Mac".

### 6.3 Pairing underneath

```
vps-1 (terminal)                  relay                        iPhone
SubmitPairing(unpinned,
  confirm_match) ───────────────▶ code K
prints K and the QR
                                                 scan K ─ LookupPairing(K):
                                                          claimed for owner O
WaitPairing ◀───── Claimed(O, account)                    checks the key gives K
                                                 Face ID: signs MachineCert,
                                                          shows JoinMatch(cert)
WaitPairing ◀───── Done(cert, chain) ◀────────── CompletePairing
asks for the digits, checks JoinMatch(cert)
installs; starts the service; checks the coding agents
```

- **An unpinned machine request sets `confirm_match`** whenever the machine
  can ask (a terminal, no `--yes`). Every trusted
  device that signs its cert shows the two digits, as for a Mac that joins
  as a machine only (trusted-devices.md 4.7). The y/N question goes away.
- **The code binds the key, and the digits bind the owner.** The phone
  checks that the request matches the code it scanned, so the relay can't
  swap the key. A center that claims the request for another owner can't
  make the person's own device show the digits. That leaves nothing for a
  verification code to catch, so none is compared in this flow.
- **What the QR holds**: `https://<console>/p/<code>`. On a phone with
  Hermesh, the link opens the app, which claims the code over its own
  connection, with no browser and no console. Anywhere else, the console's
  page claims nothing for a machine: it says to scan the code with Hermesh,
  or to type it in the app.
- **Own servers keep the command with the address.** A self-hosted relay
  refuses requests that name no owner (`submitPairing`), and that command
  pins the owner already, so it needs no digits. On an own server the
  iPhone's Add a Machine shows it first; scanning is for the official
  service.
- **Re-submitting**: while the command waits, an expired request is
  submitted again. The code comes from the key, so the code and the QR stay
  the same. A request with the owner in it (the address) reaches the phone
  again; an unpinned one waits to be scanned again. Ctrl-C ends it.

### 6.4 Root

A fresh VPS logs in as root. Whom Hermesh runs as is the person's choice:
the installer says who they are, and installs for root unless they name
someone else.

```
You are root: Hermesh will be installed for root.
Press Enter to continue, or type another user to install for (q quits): dev
“dev” doesn't exist. Create it? [Y/n]
```

Enter installs for root, with lingering on for root. Without a terminal it
installs for root without asking. For another user, named there or with
`--user <name>`, the installer:

1. creates the user if needed (`useradd -m`);
2. turns on lingering for it;
3. installs into its home and runs the rest as that user (`runuser -u
   <name>` with its `XDG_RUNTIME_DIR`), so the service is the user's own.

The coding agents are installed and signed in as the user Hermesh runs as
(`su - dev` for another user), and the final check says so.

### 6.5 The background service

- **Order**: lingering first (`loginctl enable-linger`, or the `sudo` line
  to run when that isn't allowed). Then `systemctl --user`, with
  `XDG_RUNTIME_DIR=/run/user/<uid>` when the session has none, as after
  `su`.
- **No user systemd** (a container, WSL without systemd): the installer says
  so and gives two ways, `hermeshd run` as the container's command, or
  `hermeshd run` under tmux or nohup. It does not claim a service that isn't
  there.
- **`hermeshd doctor`** reports whether the service runs, not only whether
  it is installed.

### 6.6 Coding agents

At the end of the install, and on the phone's machine page, each coding
agent is shown in one of three states:

- ready;
- not installed, with its install page;
- not signed in, with the command to run once as this user. On a server,
  that command prints a link to open on any device.

Hermesh never signs in for them (the onboarding concepts).

### 6.7 Projects on the machine

`hermeshd project add <folder> [--name] [--in-place] [--base main] [--agents
claude,codex]`, plus `hermeshd project list` and `hermeshd project remove`.

- These are the machine's own settings. It reports changes to the trusted
  devices like any other change, and the phone shows the new project at
  once.
- They give no one more than they had: whoever has a shell on the machine
  can do as much already.

### 6.8 An agent on the same server

Claude Code, OpenClaw or an agent of one's own on the server hands out work
with the `hermesh` CLI, which is installed with the machine.

- `hermesh auth login --name "OpenClaw on vps-1"` prints its link, with a QR
  when the output is a terminal.
- Opened on the phone, the link opens the request in the app. Like any
  unpinned client request claimed by the owner, it needs no verification
  code (`clientConfirmed`).

## 7. Requests reach the phone

### 7.1 Requests claimed through a link

- When the console claims an agent's request for an account ("It's me"),
  the relay notifies the owner's devices (`notifyPairing`), as it already
  does for a request that names its owner.
- A code typed or scanned on a device notifies no one, since the person is
  already there.

### 7.2 MCP apps

- **A connection that asked keeps asking.** It keeps `Asked` until the owner
  approves or declines, not just until its request is sent.
- **Its request goes out when a trusted device can receive it**: one is
  connected, or one is registered for push (the relay keeps those
  registrations already). It also goes out when a trusted device connects
  and the connection is still asking.
- **No timer.** The triggers stay the app's own use and a device
  connecting (hosted MCP decision).
- **The consent and status pages** say where the request waits (§8).

### 7.3 Links into the app

- **The links**: the app takes `https://<console>/p/<code>` as a universal
  link (the center serves `apple-app-site-association`), and
  `hermesh://pair?code=…`. Today it takes `hermesh://` links only during
  onboarding.
- **A machine's code**: the app claims it and opens its request (§6.3).
- **An agent's link**: the app claims it and opens its request, already
  confirmed, as the console's button does.
- **Without the phone at hand**: on the official service, an agent's link
  keeps working in any browser signed in to the account, as today. A
  machine's code is typed into the app on any trusted device (§6.3).

### 7.4 Waiting

A push can arrive late, and a request lives 10 minutes. Nothing dies
quietly:

- a machine's command and `hermesh auth login` submit again while they wait
  (§6.3);
- MCP requests go out again (§7.2).

## 8. Words

| Where | Today | Instead |
|---|---|---|
| `hermeshd pair`, `hermesh auth login` | "Approve only if Hermesh on your Mac shows this same code", "In Hermesh on your Mac, choose…", "Sent to Hermesh on your Mac" | Before the claim, "Hermesh on your iPhone or Mac"; after it, the approving device by name, or "your trusted devices" |
| The `hermesh` CLI | "renew it in Hermesh.app", "add one … in Hermesh.app" | "in the Hermesh app" |
| MCP consent and status (`pkg/mcpgw`) | "approve it on your Mac with Touch ID", "Install Hermesh for Mac" | Chosen by the platforms of the owner's devices in the root chain: "on your iPhone", "on your Mac", or "on one of your trusted devices". Before setup: "Install Hermesh on your iPhone or Mac" |
| `hermesh-relay serve` | "Enter it in Hermesh.app during setup" | "in the Hermesh app" |
| The console (closed source) | "是我，发到我的 Mac 确认", "在 Mac 上批准", onboarding's "请在 Mac 上打开这个页面", device lists headed "你的 Mac" | Trusted devices. The onboarding card offers the iPhone app and the Mac |
| /connect | "approve you in Hermesh on their Mac with Touch ID" | "on their phone or Mac" |
| README, Get started | Mac only | iPhone first or Mac first |

Only the device that uses them names Face ID or Touch ID.

## 9. Linux as a trusted device

### 9.1 For whom

- People with Android phones, whose only computer runs Linux.
- iPhone owners who want a second device that can approve (§5.8) without
  buying another Apple device.

### 9.2 The risk to design around

On a Mac, the root key sits in the Secure Enclave and only the signed signer
app can use it, with Touch ID. hermeshd and the coding agents can ask for a
signature, but they can never sign. Linux has no such boundary between the
programs of one user:

- a coding agent runs as that user with full trust. It can replace
  `~/.local/bin/hermeshd`, read what is typed into it, and wait for the next
  approval;
- on a cloud server that user usually has passwordless sudo, which makes the
  agent root.

So **a Linux trusted device runs no coding agents.** It is a computer, or a
Unix user, that is not one of the owner's machines.
`hermeshd device join` refuses where this user's hermeshd is a machine, and
warns where another user's hermeshd on the computer is one. Decision 5
covers relaxing this.

### 9.3 Shape

- **Owner role**: `hermeshd run --owner` as a systemd user service, as on a
  Mac but without the app. It holds the relay connection, the requests and
  the profile. It never hosts a machine.
- **Signer**: `hermeshd approve`, in a terminal. It watches signing requests
  (`WatchSigningRequests`), shows what will be signed, asks for the key's PIN
  or passphrase, signs and completes the request. What it shows comes from a
  Go port of SigningPreview, checked against the Swift one on shared test
  vectors.
- **Key**:
  - in the TPM 2.0 (`/dev/tpmrm0`; the user joins the `tss` group) when
    there is one. The key can't be exported, and its PIN is guarded by the
    TPM's lockout;
  - otherwise, a file sealed with a passphrase (Argon2id).

  The key's label says which ("Linux · TPM", "Linux · passphrase"), and the
  other devices show it.
- **Notifications**: on a desktop, one notification per request. On a
  server, none: someone signs in and runs `hermeshd approve`.

### 9.4 Commands, first version

| Command | Does |
|---|---|
| `hermeshd device join` | Creates the key and shows a pairing code and QR, as a machine does (§6.3); the phone scans it, approves the device, and shows two digits to type here. No browser |
| `hermeshd device setup` | The first device, for someone with no other device: signs in as in §9.5 |
| `hermeshd approve [code]` | Goes through each waiting request (or the one with this code): who asks; the verification code when the request came pinned; for an agent, its scope (projects, coding agents, lifetime; default all, all, 30 days); then the PIN. After a machine, it shows its two digits |
| `hermeshd device list`, `hermeshd device remove <name>` | Lists the trusted devices, and removes a lost one, re-issuing what it signed (trusted-devices.md 4.3) |
| `hermeshd device leave` | `Leave` |

The first version approves and removes. Sessions, settings and editing
grants stay on the phone, the Mac or the owner's agent.

### 9.5 Signing in without an app

Joining needs no sign-in: a device request that names no owner is claimed
by an owner's own device through its code (`LookupPairing`), never through
a console link, and the two digits prove the owner, as for machines. The
relay accepts such device requests on a hosted center; today it refuses
them ("a device joins a named owner").

Only the first device signs in. `/app-login` hands the link code back
through `hermesh://`, which a terminal can't receive.

- With `cli=1` the page shows the code instead, and the terminal asks for
  it.
- The code is bound to the PKCE challenge the terminal keeps, so it is
  worthless on its own.
- The page warns to type it only into a terminal the person started: being
  talked into reading such a code to someone else is the known risk of
  device-code sign-ins.
- The hosted center needs only this page change.

### 9.6 What an attacker can do

| Attack | Result | Why |
|---|---|---|
| A coding agent on the same computer | Ruled out by the rule in §9.2 | A trusted device is not a machine. Breaking the rule is what decision 5 is about |
| Malware with the user's shell | Can approve while the person types the PIN | As with a stolen unlocked device; removing the device from another one ends it |
| A copy of the disk (passphrase key) | Offline guessing of the passphrase | Argon2id; a TPM key never leaves the TPM |
| The host of a cloud VM | Can use a TPM key whenever the PIN is typed there | The vTPM is the host's: keep Linux trusted devices on computers the person holds |
| A center that sends the join elsewhere | Fails | The two digits, as for any device |

### 9.7 One box (not recommended)

Some people will want one Linux server that is both a machine and a trusted
device. Then anything an agent runs there can approve as the owner, on every
machine. If it is wanted anyway (decision 5), it would come only with a
passphrase key, a label, and a line on every other device: "vps-1 can
approve, and runs coding agents". The recommendation is not to build it.

## 10. Building it, in the smallest steps

Each step works end to end on its own and ships with its end-to-end test
(§11).

| Step | What | Size | Where |
|---|---|---|---|
| 1 | Requests reach the phone: pushes on link claims (§7.1), MCP requests (§7.2), the iPhone's /connect line and no address on the official service (§5.6), the words (§8) | S | relay, mcpgw, iOS, CLI; the console's words |
| 2 | The Linux install: the digits for unpinned machines (§6.3), `/dev/tty`, re-submitting, root (§6.4), the service (§6.5), doctor (§6.6) | M | `internal/machine`, `internal/service`, `install.sh`, `cmd/hermeshd` |
| 3 | The iPhone adds machines: scan, links (§7.3), the digits page (§5.4) | M | iOS; `apple-app-site-association` and the machine `/p/` page on the center |
| 4 | The iPhone's first run: Get Started, notifications, the first task, the second-device row (§5.1, §5.2, §5.7, §5.8), and Check Again on a remote machine (`recheck`, §5.5) | M | iOS, machine |
| 5 | Projects and Test: `hermeshd project add`, the repositories offered (§5.5, §6.7), Test (decision 2) | S–M | machine, iOS |
| 6 | An own server set up from the iPhone (§5.9) | S | iOS |
| 7 | Linux trusted devices (§9) | L | owner role, signer, `cmd/hermeshd`; `/app-login` on the center |

Steps 1 to 4 make the five-minute journey of §4. Steps 5 to 7 make it
comfortable and widen who can use it.

**Left after the build:**

- the TPM key (§9.3): a Linux trusted device's key is sealed by a
  passphrase only (`internal/devicekey`);
- notifications on a Linux trusted device with a desktop (§9.3);
- shared test vectors between the Go preview (`internal/preview`) and
  HermeshKit's SigningPreview (§9.3): each has its own tests;
- `hermeshd device join` refuses where this user's hermeshd is a machine,
  but doesn't yet warn when another user's on the computer is one (§9.2);
- how often a request may notify, and production APNs (decision 13).

**Running the whole journey locally**, for the build and the demo:

- a relay on the Mac;
- the iPhone app in the Simulator against it (an own server), or against a
  hosted center run locally, for the official flow;
- a Linux machine in Docker, with systemd as PID 1 (an Ubuntu image) and
  logged in as root. It runs the install script from a local release
  directory (`HERMESH_DOWNLOAD_BASE`), with the scripted ACP agent
  (`fakeacp`) or a real `claude`.

## 11. Tests

- **TestLinuxInstall** (hermesh-cloud, `HERMESH_DOCKER=1`): a systemd
  container logged in as root, the one command from a local release, the
  user it creates, the code claimed by the owner's device and the digits
  typed into the script's pty after a wrong pair. The service runs with
  nobody logged in and the console lists the machine.
- **TestRemoteMachineSetup**: Check Again on a remote machine, the
  repositories it finds, Test, and projects added in its shell and from a
  device (`~/…`).
- **TestIPhoneFirstRun** (`HERMESH_IPHONE_E2E=1`): the iPhone app in the
  Simulator claims a new own server and makes its key, puts the guide off
  and takes it up again from Get Started: a machine added by its pairing
  link with nothing to compare, Rescan, Test and a project the machine found
  on its coding agent's page, an agent connected and its first session's
  answer, then the second-device row. **TestIPhone** keeps the rest of the
  app.
- **TestMCPReachesAnIPhone**: the owner's only device is an iPhone
  registered for push and offline. An MCP app uses its connection and the
  push arrives (spool); the phone connects later, the request is there, and
  it approves it.
- **On the center** (hermesh-cloud): `TestCenterPush` (an agent's link
  claimed in the console pushes a sealed notice), `TestDirectApproval` (a
  machine's code on the console page claims nothing), and the console's
  tests of the machine `/p/` page, `apple-app-site-association` and
  `/app-login?cli=1`.
- **TestLinuxTrustedDevice** (hermesh-cloud): a first device set up with
  the code `/app-login?cli=1` shows, another joining by its code with the
  digits, approving a machine and an agent with the passphrase, removing a
  lost device, and a machine's user refused.
- **Wording**: `internal/words` fails on "your Mac", "Hermesh.app" or
  "Touch ID" in Go strings, outside code that picks a device's words.

## 12. Decisions

Decided by the owner on 2026-10-28, each as recommended:

1. **Adding machines from the phone**: scan and two digits by default, with
   the command with the address as the alternative.
2. **Test on a coding agent's page**: one fixed turn, no session.
3. **The second-device row**: shown once the first machine joins.
4. **The install URL**: it stays on GitHub, because a short hermesh.io URL
   would let the center change what runs on new machines.
5. **Linux trusted devices**: only on computers that run no coding agents,
   and no one-box mode.
6. **The Linux key**: in the TPM when there is one, a passphrase otherwise,
   and a YubiKey (PIV) later.
7. **MCP connections**: keep asking until answered (§7.2).
8. **Verification codes**: none to compare for machines added by their code
   (§5.4); pinned requests keep them.

Technical decisions, also by the owner on 2026-10-28:

9. **Own servers**: no scanning; the command with the address stays their
   way to add a machine (§6.3).
10. **Projects on a Mac machine**: the repository search skips the folders
    macOS protects (§5.5).
11. **Joining a Linux trusted device**: by its code on the phone, with no
    sign-in; only the first device signs in, with the warning of §9.5.
12. **Updating Linux machines**: this build only says a machine is behind
    and shows the command; self-updating machines get their own design.
13. **Notifications**: how often a request that keeps asking may notify,
    and the production APNs setup, are studied once this is built.

Details settled without the owner: `hermeshd project add` reaches the
running machine through a new local socket (0600, same user), since the
machine locks its data directory; Linux keeps the systemd user service; a
machine asks for the digits only when it can ask (a terminal, no `--yes`);
every protocol change is a new field, so older apps and machines keep
working; the terminal's QR comes from a small Go library, and the TPM from
`google/go-tpm` (pure Go).

## 13. The demo (to define after this review)

A clickable walk through §4: an iPhone frame beside a terminal, every step
from the welcome page to the first task's answer. It also covers the
branches that matter:

- notifications turned down;
- an install started as root;
- wrong digits;
- an agent connected before any machine;
- an MCP app connected while the phone is closed.

Its scenes, and how real the terminal is, get settled once the doc is
agreed.

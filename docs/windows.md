# Hermesh on Windows

Status: accepted 2026-10-30, with the owner's decisions in §12; phases W0 to W3 are built, and §14 lists what is checked on a real PC next and what is left. It replaces [desktop-app.md](desktop-app.md) §9 for Windows. Windows goes into [technical-design.md](technical-design.md) §6.1 and [hermeshd.md](hermeshd.md) as it is built.

## 1. Goal

A Windows PC does what a Mac does, in three steps that are each useful alone:

1. **A machine.** Coding agents (Claude Code, Codex, ACP agents) run natively on it, in Windows projects, started from any of the owner's devices or agents. No app needed; another trusted device (the iPhone, a Mac, a Linux device) approves it.
2. **The app.** The same Electron app, from the same code, that feels like a Windows app. Until step 3 it joins as a machine only, approved from another trusted device.
3. **A trusted device.** It approves, keeping the Mac's promise as far as Windows allows (§7): programs on the PC can ask for a signature, but only the owner, looking at what is really signed, can give one.

**Which Windows.** Windows 11 22H2 or later, x64 and arm64, for the app and the trusted device. Machines also run on Windows 10 22H2 and Windows Server 2019 or later, best effort (CI's Windows runners are Server images). Windows 10's support ended on 2025-10-14; Windows Hello's verifier for desktop apps needs build 22000, Mica build 22621.

**Not in scope:** per-machine and managed (MDM) deployment, the relay on Windows, the app on Windows Server, and Hermesh itself running inside WSL (a WSL distribution is a Linux machine, §8).

**What does not change:** the relay, the center, the iPhone app, the wire protocols and the signature format. The only API change is two small additions to `hermesh.local.v1` for the signer (§7.3).

## 2. Principles

1. **One product.** One protocol, one UI codebase, one set of flows. Windows is a set of adapters, not a fork.
2. **Named seams.** Everything that differs between operating systems sits behind a few named seams (§4, §5.1), each a Go package or a TypeScript module with one implementation per OS, chosen at build time (Go build tags) or once at start (Electron main). Code outside the seams does not branch on `runtime.GOOS` or `process.platform`.
3. **Windows' own mechanisms.** Named pipes, job objects, the registry's environment, logon tasks, toasts, the window caption overlay. Hermesh itself needs no Git Bash, MSYS, Cygwin or WSL; no `sh -c`, no signals.
4. **Contract tests on every OS.** Each seam has one test suite that runs on macOS, Linux and Windows in CI. Windows joins CI before any Windows feature does, so Unix-only code cannot creep back.
5. **Promise only what the OS enforces.** The trust boundary is stated per OS (§7.1). Where Windows enforces less than macOS, the product says so.
6. **Stable paths for what others run.** Agents' hooks, terminals and scheduled tasks name programs by paths that survive updates.

The processes on a Windows PC that is both a machine and a trusted device:

```
 the owner's programs (medium integrity)                  elevated (high integrity)
 ┌──────────────────────────────────────────────┐         ┌───────────────────────┐
 │ Hermesh.exe        window · tray · toasts    │         │ hermesh-signer.exe    │
 │   │ named pipe, this user only               │         │ in Program Files      │
 │   ▼                                          │  pipe   │ TPM key · panel ·     │
 │ hermeshd           logon task, supervised    │◀────────│ Windows Hello         │
 │   │ a job object per session                 │         └───────────────────────┘
 │   ▼                                          │
 │ claude · codex · ACP agents, and their tools │
 └──────────────────────┬───────────────────────┘
                        │ WSS + E2E TLS
                  relay / machines
```

As on the Mac, the signer and the app never talk to each other; hermeshd is the only coordinator.

## 3. What the code assumes today

Cross-compiling with `GOOS=windows` fails first in `pkg/rlimit`, `internal/flock`, `internal/spawn` and `internal/peercred`. Behind them:

| Area | Today | Where |
|---|---|---|
| Local endpoints | Unix sockets (0600 in a 0700 folder) and a peer uid check: three listeners, five dialers | `internal/owner/localapi.go`, `internal/machine/{control,sessiontools,messagemcp}.go`, `cmd/hermeshd/{main,device}.go`, `internal/cli/hook.go` |
| Process trees | Process groups, `kill(-pgid)`, `Wait4`, a sampled process table for descendants that leave the group, posix_spawn helpers on macOS | `internal/provider/{proc,tree,proctable_*}.go`, `internal/spawn` |
| Background service | LaunchAgent or systemd user unit, through `runtime.GOOS` switches | `internal/service/service.go` |
| PATH for agents | The login shell's (`$SHELL -lic`) | `internal/shellenv` |
| Finding agents | Unix install locations; executable bits (`mode&0o111`); `/` in paths | `internal/provider/{claude,codex,acp}`, `proc.go` (`LookPath`, `startSpawn`) |
| Hooks | `strconv.Quote(exe) + " message hook …"` in the agents' settings; `Flock`, `Setsid`, `kill(pid, 0)` | `internal/cli/hook.go` |
| Health | `Statfs`, `RLIMIT_*`, `kern.proc` or `/proc` | `internal/machine/{health*,diagnostics}.go` |
| Data | `~/Library/Application Support/Hermesh` or XDG; the CLI's `~/.config/hermesh` | `cmd/hermeshd/main.go`, `internal/cli/state.go`, mirrored in `apps/desktop/src/main/paths.ts` |
| Electron main | Daemon, login item, signer, tray, updater and install gate only on `darwin`; Terminal `.command` files; Activity Monitor; off macOS, quitting when the last window closes | `apps/desktop/src/main/*.ts` |
| Renderer | `⌘` written into shortcut labels; folder names split on `/`; Add Project requires a leading `/`; about 60 English strings (outside release notes) name the Mac, Touch ID, Finder or System Settings | `Sidebar.tsx`, `Search.tsx`, `sheets/Add.tsx`, `src/locales` |
| Packaging and release | DMG, Squirrel.Mac zip and feed, notarization; binaries for darwin and linux; `install.sh` | `apps/desktop/scripts/build-app.mjs`, `scripts/release.sh`, `.github/workflows` |
| CI | `ubuntu-latest` and `macos-latest` | `.github/workflows/ci.yml` |

Already right: SQLite is pure Go (modernc); machines report `windows/amd64` and `platformTitle` names Windows; `rootPlatform` maps `win32` to `windows`; menu accelerators are written `CmdOrCtrl+…`; deep links already arrive through `second-instance`; every signature is checked in one place (`pkg/identity`).

## 4. The seams (Go)

Each seam gathers code that today repeats or is spread out, so that its Windows half is written once. Phase W0 (§9) moves today's Unix code behind them unchanged.

### 4.1 Where things live

One package, `internal/paths`, says where everything is; `cmd/hermeshd`, `internal/cli` and `internal/machine` ask it instead of computing paths. `paths.ts` mirrors it, and a test compares the two through a hidden `hermeshd paths --json`.

| | macOS | Linux | Windows |
|---|---|---|---|
| Data: daemon, keys, database, worktrees, logs | `~/Library/Application Support/Hermesh` | `$XDG_DATA_HOME/hermesh` | `%LOCALAPPDATA%\Hermesh` |
| Local endpoints | `<data>/run/*.sock` (a short fallback in the temp folder) | same | named pipes whose names are in `<data>\run\*.pipe` (§4.2) |
| CLI: agents' identity and state | `~/.config/hermesh` | same | `%LOCALAPPDATA%\Hermesh\cli` |
| App profile | `<data>/desktop` | same | `<data>\desktop` |
| Programs | `Hermesh.app/Contents/Helpers`, or `~/.local/bin` (`install.sh`) | `~/.local/bin` | `%LOCALAPPDATA%\Programs\Hermesh\bin`, on PATH: the app copies its programs there (§6.1), as `install.ps1` downloads them |

- **Local, never Roaming.** A domain profile copies Roaming AppData to the user's other PCs, and device keys must stay on one.
- **One folder.** The CLI moves in too (today's code would pick `%USERPROFILE%\.config\hermesh`), so erasing has a single folder to remove.
- On Windows, paths compare case-insensitively (projects, worktrees), and the worktrees Hermesh creates set `core.longpaths`.

### 4.2 Local endpoints

`internal/localipc` replaces the three listeners and five dialers:

```go
Listen(path string) (net.Listener, error)                // at paths.LocalAPI, Control or Tools
Dial(ctx context.Context, path string) (net.Conn, error) // ours, or another program's (Claude Code's inbox)
Peer(c net.Conn) (p Process, ok bool)                    // pid and executable: a hint, as today
```

- **Unix:** what the code does today.
- **Windows:** a named pipe (go-winio, already in `go.mod`), created with a security descriptor that admits only the user's SID. go-winio also refuses remote clients and creates the first instance exclusively. The name is random, `\\.\pipe\hermesh-<128-bit random>`; the daemon writes it into `<data>\run\<endpoint>.pipe` at each start, and clients read it there; `Dial` also takes a pipe's own name, for another program's endpoint. No other user can read that file, so no other user can learn the name, take it first or pose as the daemon: the protection the 0700 folder gives the socket on Unix. The peer comes from `GetNamedPipeClientProcessId` and `QueryFullProcessImageName`.
- **Node** connects to a pipe the way it connects to a socket (`http.request({ socketPath })`), so the gateway, the notifier and sign-in only change how they find the path.
- **Liveness** is a dial, not `os.Stat`, since a pipe is not a file: `hermesh message watch` checks Claude Code's inbox that way, where today it looks for the socket file.

### 4.3 Process trees

The process code of `internal/provider` splits by OS. `proc.go` keeps what every system shares (the pipes, the lines, stderr, the agent's exit) and calls four functions that each system defines:

```go
setGroup(cmd *exec.Cmd)        // before Start: a tree of its own
joinGroup(cmd *exec.Cmd) error // after Start (Windows: into its job, then resumed)
killGroup(pgid int) error      // end the whole tree now
KillGroup(pgid int)            // end it gently, then for sure (also at recovery)
```

- **Unix** (`proc_unix.go`, `tree.go`): as today: a process group, the sampled process table for descendants that leave it, posix_spawn helpers on macOS.
- **Windows** (`proc_windows.go`, `tree_windows.go`): a job object per session with `KILL_ON_JOB_CLOSE`. The process starts suspended and joins its job before it runs, so no descendant ever runs outside it. Only the three stdio handles are inherited (`PROC_THREAD_ATTRIBUTE_HANDLE_LIST`), with `CREATE_NO_WINDOW`. Stop closes stdin, waits, then calls `TerminateJobObject`.
- **Less to do.** A job sees every descendant, so there is no process table to sample. `CreateProcess` copies no descriptor table, so there are no spawn helpers. `RunCommand` (git) runs in a job too, so a timeout ends its whole tree.
- **When hermeshd dies**, its jobs close and their processes end: the state `Recover` already expects (the turn in progress is crashed). On Windows there are no orphans to kill at recovery.
- Interrupting a turn stays in each agent's protocol (stream-json, app-server, ACP); no signal is involved.

### 4.4 The background service

- **A windowless twin.** A console program started at logon opens a console window. So Windows has `hermeshd.exe` (console, every command) and `hermeshdw.exe` (the same code built with `-H windowsgui`, for `run` only), as Python has `python.exe` and `pythonw.exe`.
- **Started at logon** by a Task Scheduler logon task of this user's, which needs no administrator: `hermeshd service install`, which `install.ps1` runs for a machine, and the app with `--owner` for its own daemon (the owner's, and the PC's machine). The app registers the task again whenever it finds the daemon not answering, so it takes over a machine that `install.ps1` set up.
- **Kept running.** Task Scheduler's restart-on-failure does not cover a program that starts and later exits, so `hermeshdw run` supervises: it runs the daemon as its child in a kill-on-close job, and restarts it with backoff when it exits unexpectedly. That is what `KeepAlive` and `Restart=on-failure` do elsewhere.
- **Stopped and restarted through the control endpoint**, since there is no SIGTERM: `hermeshd service restart`, and the app after an update, ask the daemon to exit with "restart", and the supervisor starts it again from the current program.
- **Logoff and shutdown** reach a windowless program as window messages, not console events: `hermeshdw` keeps a message-only window and stops cleanly on `WM_ENDSESSION`.
- **One daemon per data folder**, as today: the folder's lock, taken with `LockFileEx`.
- **Later:** running with nobody logged on (a PC reached only over SSH) needs a task that runs whether the user is logged on or not, which an administrator sets up once.

### 4.5 The PATH agents get

`internal/shellenv` gets a Windows half: "the PATH a new terminal would have". On Windows that is no shell's. It is the machine's and the user's `Environment` in the registry, expanded, then the current PATH, then known install folders. It is read again at each agent check, and starts no program, so a CLI installed after the daemon started is found without a restart.

### 4.6 Coding agents

- **Where they are** is data, per OS, next to each provider:

| Agent | Windows locations |
|---|---|
| Claude Code | `%USERPROFILE%\.local\bin\claude.exe` (native installer); WinGet's links folder; the npm package, which installs the same native binary |
| Codex | The npm package's native `codex.exe`; the Codex Windows app's CLI (S4) |
| ACP agents | Their npm shims, resolved (below) |

- **Lookup** follows `PATHEXT` and does not look for executable bits.
- **Never `.cmd`, `.bat` or `.ps1`.** `cmd.exe`'s quoting cannot carry arbitrary arguments safely. npm's shims are resolved to the program they start (node and its script, or a native binary), the shape `acp.command` already returns for app bundles (a binary plus leading arguments). Anything else is refused, saying why.
- **Git.** Worktrees need Git for Windows; a missing git is a machine problem with its fix (`winget install Git.Git`).
- **Claude Code** uses Git Bash for its shell tool when Git for Windows is installed, PowerShell otherwise; nothing for Hermesh to do. It has no sandbox on native Windows.
- **Codex** brings its own Windows sandbox; S4 checks it under our job.
- **Hooks.** On Windows the command names `hermesh` by its execution alias, in the syntax of the shell the agent runs hooks with (S4: which shell, and a user name with a space in it).
- **Claude Code's inbox** is whatever Claude Code exports in `CLAUDE_CODE_MESSAGING_SOCKET`; `localipc.Dial` takes a pipe's name as well as an endpoint's path.

### 4.7 Health, diagnostics and the small ones

- Disk space from `GetDiskFreeSpaceEx`; no descriptor limits (`pkg/rlimit` does nothing); process counts from the jobs.
- "Who started this" names a program by its version resource (`FileDescription`), as `appname_darwin.go` reads a bundle's display name.
- The "shell PATH" problem becomes a "PATH" problem.
- `hermesh message watch` detaches with `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`; a pid is alive if `OpenProcess` says so.
- `fakeacp` and the tests run their scripted commands through the platform's shell, not `sh -c`.

## 5. The app

### 5.1 One main process, one module per OS

`src/main/platform/{mac,win}.ts` export the same interface. `index.ts` takes one at start, and no other file of the main process branches on `process.platform` but `paths.ts`, which mirrors `internal/paths`; the page reads the platform's token (`renderer/src/platform.ts`, `html[data-platform]`).

| | macOS | Windows |
|---|---|---|
| Daemon | `SMAppService` agent; `launchctl kickstart` | The logon task, which the app registers (`hermeshd service install --owner`); a restart ends the task, whose supervisor's job ends the daemon, and runs it again |
| Open Hermesh at login | `mainAppService` | The user's Run value, through Electron's login item API, naming the Hermesh.exe above the versions; Startup apps lists and switches it |
| Signer | `Hermesh Signer.app`, the app's child | A protected system component, not the app's child (§7.3); set up before the first root key (`readySigner`) |
| Updates | Squirrel.Mac | Squirrel.Windows, through the same `autoUpdater`, from a signed feed (§6.2) |
| First launch | Move to Applications | Nothing: Setup.exe installed it; the installer's events (install, update, uninstall) start the app to set itself up and quit |
| Login commands (`codex login`) | A `.command` file in Terminal | A Windows Terminal tab (`wt.exe`), else PowerShell; a command table per OS, still chosen by key |
| System links | Activity Monitor; Login Items | Task Manager; `ms-settings:startupapps` |
| Computer name | `scutil --get ComputerName` | `os.hostname()`, the device name |
| The signer's language | `-AppleLanguages` | Windows' display language |

### 5.2 Feeling native on Windows

- **Window.** A hidden title bar with the caption overlay (`titleBarOverlay`): Windows draws minimize, maximize (with Snap Layouts) and close, the page's top bands leave them room (`env(titlebar-area-*)`), and the toolbar rows are the drag area, their height the caption's. Mica shows through the sidebar, where macOS has the sidebar's vibrancy (Windows 11 22H2 and later; before, the sidebar is solid): `backgroundMaterial`, and the page clear there (`html.vibrant`, `platform.material`).
- **Type and tokens.** Segoe UI Variable under `data-platform="win"`, at Windows 11's sizes (the owner chose them on 2026-10-31): 14 px body text where the Mac has 13 (`--fs-text`), 4 px corners on controls and rows and 8 px on boxes, cards and panels (`--r-ctl`, `--r-box`, `--r-card`, `--r-panel`; the Mac's 7, 10, 12 and 14). The selected row in the sidebar has Windows 11's accent bar.
- **Menus.** Windows has no menu bar at the top of the screen, and the caption overlay leaves no room for one in the window. The command list in `menu.ts` stays the one source: on Windows it becomes the window's hidden menu (its accelerators keep working) and a menu button at the top of the sidebar. Context menus stay native.
- **Shortcut labels** come from the accelerators (`CmdOrCtrl+K` → `⌘K` or `Ctrl+K`; `src/shared/keys.ts`), not from `⌘` written in the renderer. The two that differ get Windows keys of their own: the sidebar Ctrl+B, the info panel Ctrl+Shift+I (never Ctrl+Alt, which is AltGr on many keyboards).
- **Notification area.** An icon in light and dark versions, for the taskbar's theme. A left click opens the panel above it, a right click a native menu. Closing the last window keeps Hermesh there, since notifications come from main; today, off macOS, it quits.
- **Notifications** are toasts, as the Mac's notifications are, under the app's AppUserModelID (`com.hermesh.app`, its Start menu shortcut's); a click opens what it tells of while the app runs (it stays in the notification area). Buttons and replies (Electron 41 and later) can come to both.
- **Deep links:** the app registers `hermesh:` for the user, naming the Hermesh.exe above the versions, and links come through `second-instance` as today.

### 5.3 Words

Where the wording differs, a key's Windows variant takes its place on Windows when the text loads: `key_win` (plural forms `key_win_one`), in every language and namespace, so no call site changes (`src/shared/words.ts`). "This PC" stands for "This Mac", "Windows Hello" for "Touch ID", "Settings › Apps › Startup" for "System Settings › Login Items". The Chinese of each variant goes into the translation files, as for every string. The same goes for hermeshd's and the CLI's output, and for the center's pages in hermesh-cloud: "On your Mac" becomes "on your computer" where both apply.

### 5.4 Paths of other machines

The app shows paths of machines of every OS, so it reads each by that machine's `platform`. A folder's name ends at `/` or `\`. An absolute Windows path starts with a drive (`C:\`) or a share (`\\server\share`), which Add Project accepts for a Windows machine. Diffs may carry CRLF.

## 6. Packaging, updates and signing

### 6.1 Setup.exe

The app ships as a per-user Squirrel.Windows installer, `Hermesh-windows-<arch>-setup.exe`, from the GitHub release (and the website, which links there). It needs no administrator, and no signature (§6.4): Windows warns once, through SmartScreen, before it runs. While it installs it shows the mark on the icon's dark fill, a thin bar running under it, and no words, since it can't pick the person's language (`windows-splash.mjs` draws it). (The plan was an MSIX package through the Microsoft Store, which would have signed it; the owner chose to distribute it themselves, and an MSIX package installs only when signed.)

Setup.exe installs into `%LOCALAPPDATA%\HermeshApp` (Squirrel removes that folder on uninstall, so it isn't `Hermesh`, the data folder): a folder per version, and above them `Hermesh.exe`, which starts the newest, and `Update.exe`, which installs updates and uninstalls. Squirrel starts the app with an event, before anything else, for it to set itself up and quit (`installerEvent`, `platform/win.ts`):

| Event | The app |
|---|---|
| Install | A Start menu shortcut at the top of All apps (with the app's AppUserModelID, for toasts); `%LOCALAPPDATA%\Programs\Hermesh\bin` on the user's PATH |
| Updated | That folder on PATH, still |
| Uninstall | Ends its running copies (Squirrel ends none, and their files could not go), removes hermeshd's logon task (`hermeshd service uninstall`), the shortcut, the PATH entry, the `hermesh:` link, Open at login and the programs' folder. The data folder stays, as a Mac app's does, and so does the signer in Program Files, which only an administrator removes |

What these do goes to `<data>\logs\setup.log`, where nobody sees the app's output. Each start, the app then:

- **Places its programs where they stay.** Each version has a folder of its own, but hooks, terminals and the logon task name programs by paths that survive updates (§2, principle 6). So the app copies `hermeshd.exe`, `hermeshdw.exe` and `hermesh.exe` into `%LOCALAPPDATA%\Programs\Hermesh\bin`, the folder `install.ps1` uses, unless they are there already; a running program moves aside first, as `install.ps1` does (§6.3).
- **Registers `hermesh:`** for the user, naming the `Hermesh.exe` above the versions.
- **Runs hermeshd** from the logon task, registering it again (`hermeshd service install --owner`) when the daemon doesn't answer.

```
%LOCALAPPDATA%\HermeshApp\
  Hermesh.exe, Update.exe                  start the newest version; update and uninstall
  app-<version>\Hermesh.exe, resources\    Electron: main, preload and the page
  app-<version>\bin\hermeshd.exe, hermeshdw.exe, hermesh.exe
  app-<version>\bin\hermesh-signer.exe     copied into Program Files when this PC becomes a trusted device (§7.3)
%LOCALAPPDATA%\Programs\Hermesh\bin\       hermeshd.exe, hermeshdw.exe (the logon task), hermesh.exe: on PATH
```

What MSIX would have given and Setup.exe doesn't: files that no program of the user can change (here a program running as the user can replace the app's files, as it can replace the CLI that `install.sh` installs; §7.4), and declarations instead of the steps above.

### 6.2 Updates

- **A signed feed.** Squirrel.Mac checks the app's Developer ID signature; the Windows app has none. So each architecture's feed, `RELEASES-win32-<arch>.json`, is signed with the release key, which signs the checksum list too (`.sig`, openssl's DER signature), and the app carries the public key (a build-time constant, as the feed's address is). The feed names this version (`currentRelease`), its highlights (`hermeshNotes`, as the darwin feed has them) and its package: the `.nupkg`'s address, its SHA-256 and Squirrel's `RELEASES` line for it.
- **Downloaded by the app.** The app reads the feed as on the Mac, checks its signature, downloads the package into `<data>\desktop\update`, checks its hash, writes the `RELEASES` line beside it, and points `autoUpdater` (Squirrel.Windows) at that folder. Squirrel installs the version beside the running one, at once; the app runs it from its next start, as Restart to Update does under the Mac's guard (desktop-app.md §6.5): never a restart under the owner.
- **hermeshd after an update.** The new version places its programs (§6.1); hermeshd keeps running the old ones, which moved aside, until the app restarts it as on the Mac: once no approval is pending and no session on this PC is queued, preparing, running or waiting for an answer.
- **Tested end to end** (`check-windows-update.ps1`, in CI): an older build, installed from its Setup.exe, finds, downloads and installs a newer one from a feed signed with a key of the test's, and opened again runs it, with hermeshd restarted on the new programs.

### 6.3 Programs without the app

- Release assets add `hermeshd`, `hermeshdw` and `hermesh` for `windows-amd64` and `windows-arm64` (`.exe`), in the signed checksum list.
- `install.ps1` (Windows PowerShell 5.1) mirrors `install.sh`: a machine by default, or `-Cli`. It has no `-Device`: a Windows trusted device is the app, whose protected signer keeps the key in the TPM behind Windows Hello (§7.3); one set up in a terminal would have a passphrase only, below what decision 4 asks of a PC. It installs into `%LOCALAPPDATA%\Programs\Hermesh\bin`, adds that folder to the user's PATH, checks the signed checksum list (ECDSA, through .NET), registers the logon task, then runs `hermeshd pair`.
- Updating while the daemon runs: rename the running program aside (Windows allows it), write the new one, restart the daemon, delete the old file later. Machines update themselves this way too (machine-updates.md §5.5).
- One PC, one daemon: the installer refuses machine mode where the app's daemon is registered, and the app takes over a daemon that `install.ps1` set up (it registers the logon task again, with `--owner`).

### 6.4 Code signing

The owner's decision (§12, decision 3): Hermesh's Windows programs are released unsigned, as open source, like its Linux binaries; the signed checksum list stays how `install.ps1` checks what it downloads. What that means:

- **Unsigned programs run.** Downloaded through a browser, a program first meets SmartScreen's "Windows protected your PC", where More info, then Run anyway, starts it. `install.ps1` downloads with PowerShell, which should leave no such mark (S6).
- **Smart App Control**, on where Windows 11 turned it on after a fresh install, blocks unsigned programs outright; its owner has to turn it off. Defender is also quicker to suspect new, unsigned Go programs.
- **The protected signer's** one UAC prompt says "Unknown publisher".
- **The app's Setup.exe** meets SmartScreen the same way, once; its updates don't (the app downloads them itself), and they are checked against the signed feed (§6.2).
- **Later, without changing anything else:** SignPath Foundation (free for open-source projects) or a certificate, if warnings or Smart App Control keep people out.

What signing would take, checked 2026-10-30:

- EV certificates no longer skip SmartScreen (since August 2024). Reputation builds per file and per publisher, whatever the certificate, and a certificate now lasts at most 460 days.
- Azure Artifact Signing (formerly Trusted Signing) takes individuals only in the US and Canada, and organizations in the US, Canada, the EU and the UK.
- The Microsoft Store signs Store packages, and registration is free for individuals and companies.

| Route | Signs | Publisher people see | Cost |
|---|---|---|---|
| OV certificate, its key in the CA's cloud, signing from CI | Everything | The certificate's legal name: a person's or a company's | A few hundred dollars a year |
| Microsoft Store | Only the Store's package | The Store account's name | Free; a review per release |
| SignPath Foundation | Everything (open-source projects only) | "SignPath Foundation" | Free |
| Artifact Signing | Everything | The organization | About $10 a month; an organization in one of its countries |

### 6.5 Build and CI

- `package-win.mjs` builds the app's files on any system (the page, main, Go for the target, the fuses the Mac has) and, on Windows, Setup.exe, the update's package and its feed, signed when given the release key (electron-winstaller); `windows-icons.mjs` draws the program's icon from the Mac icon's mark. `build-app.mjs` stays the Mac's.
- The Go programs carry version resources and the icon: Task Manager, Startup apps and Defender show them.
- `ci.yml`: `windows` runs the Go unit tests, `TestWindowsInstall` and `TestWindowsSigner`; `windows-e2e` the whole Go suite; `windows-app` the desktop spec for Windows, then makes Setup.exe and installs, opens and uninstalls it (`check-windows-setup.ps1`), updates between two builds (`check-windows-update.ps1`), and makes the arm64 Setup.exe for Arm PCs and VMs. First of all (W0) came a compile check, `GOOS=windows go vet ./...`.
- `release.yml`: a Windows job per architecture makes Setup.exe, the package and the feed, signed with the release key; on x64 it installs, opens and uninstalls the app; it uploads them, with the icon Installed apps shows (`Hermesh.ico`). `release-check.sh` checks the feeds' signatures, versions and packages' hashes.

## 7. A trusted device on Windows

### 7.1 What Windows enforces

On a Mac the root key is in the Secure Enclave. Only the signed signer can use it (its keychain access group), every signature needs Touch ID, and no other program of the owner can change the signer's panel. So a coding agent with full access can ask for a signature but never sign, and the owner signs what they see.

Windows gives ordinary programs no such binding. Any program of the user can use the user's keys, and can read and write the memory of any other program of the user at the same integrity level. A coding agent running as the owner could change what a signer running as the owner shows, while it signs something else.

What Windows does enforce is integrity levels: an elevated (high-integrity) process cannot be opened, injected into or sent input by the owner's ordinary (medium-integrity) programs (Mandatory Integrity Control and UIPI). That is UAC's line. Microsoft does not service UAC bypasses as vulnerabilities. Administrator protection (Windows 11, from late August 2026, on in Microsoft's security baselines from October 2026) hardens elevation, though Microsoft has described it both as a new security boundary and as not a formal one.

### 7.2 Options

| | Key | Who can use it | What the owner checks | Verdict |
|---|---|---|---|---|
| A. TPM key, signer as the user | TPM P-256 (NCrypt, Platform Crypto Provider) | Any program of the user, silently | A panel any program of the user can alter | Only under the Linux rule: no coding agents on this PC |
| B. Windows Hello passkey (WebAuthn) | P-256 in Hello's container (ES256) | Any program of the user, with one Hello gesture per signature | Windows' Hello dialog, with no text of ours; a fake panel next to it | A program can trick one approval per gesture; a new signature format for every verifier |
| C. Protected signer | TPM P-256 that only this user's elevated processes can open | Only the signer, elevated, from where only administrators write | The signer's panel, which ordinary programs cannot touch, then Windows Hello with its summary | The Mac's promise against unelevated programs; needs an administrator account; an agent that elevates without consent could sign |

**Choice: C**, for administrator accounts, which most personal PCs use. It keeps the Mac's promise against the attacker Hermesh designs for (a coding agent running as the owner), needs no new signature format, and leaves the remaining risk where Windows itself draws its line.

Standard (non-administrator) accounts get no protected signer; their trusted device is a phone, a Mac or a Linux device. Option A, under the Linux rule, can come later if people ask for it.

### 7.3 The protected signer

- **Program.** `hermesh-signer.exe`, in Go (`cmd/hermesh-signer`). When the owner makes this PC a trusted device, one elevated setup step (one UAC prompt) copies it into `%ProgramFiles%\Hermesh Signer\` and registers a Task Scheduler task for this user that runs it with the highest privileges, at logon and when triggered. The app and the daemon start it only by triggering that task; nothing else starts it elevated.
- **Key.** A TPM 2.0 P-256 key (Microsoft Platform Crypto Provider), not exportable, usable only with a random PIN that the signer keeps in `%ProgramData%\Hermesh Signer\keys\<SID>`, which only Administrators and SYSTEM can read: the daemon, the app and the agents, at medium integrity, can ask the TPM for the key by its name but cannot sign with it, and the TPM locks out guessing. One key per profile, as on the Mac (`root_key_id`, `delete_root_key`). Windows 11 requires a TPM 2.0; a PC without one cannot be a trusted device.
- **Panel.** A window the elevated signer draws itself (`drawn_windows.go`, GDI+ and GDI), in the design of the Mac's panel, on top of other windows, showing what the signer computes from the bytes it signs (`internal/preview`, in the panel's language), Deny the default. Approve asks Windows Hello (`UserConsentVerifier` for the panel's window), the panel waiting under it; not verified, it stays to approve again or deny. Drawn by the elevated process, the user's other programs can neither change nor click it; WebView2 was not an option, since Edge de-elevates its processes. Should the window fail, a native task dialog stands in.
- **Signature.** ECDSA P-256 in DER, the format everything verifies today. `RootKey.platform` is `windows`, the label "Windows · TPM". Nothing changes in what is signed or how it is checked.
- **One signer core in Go.** The loop of `hermeshd approve` (watch, preview, confirm, sign, complete) moves into `internal/signer`, behind two interfaces: `Confirmer` (the terminal; the Windows panel) and `KeyStore` (the passphrase file; the TPM). Linux's terminal signer and the Windows panel then share one implementation of what is shown and signed, and a Linux desktop signer can reuse it later. The Swift signer stays for Apple. The shared Go and Swift preview vectors (desktop-app.md §5.6, not built yet) come first.
- **Through the daemon only.** On the Mac the signer is the app's child: it reports busy on stdout, and SIGUSR1 brings it forward. The Windows signer is not the app's child, so hermeshd reports the signer's presence and busy state in its status, and relays "show" on the signing stream: `LocalService.ShowSigner` and a status field, the two additions to `hermesh.local.v1`. The app lets the signer's window take the foreground when the owner clicks Approve. Moving the Mac to the same later would remove the one direct channel between an app and its signer.
- **Setup.** Before hermeshd needs a new root key (sign-in, an own server's setup, joining as a trusted device), the app makes sure a signer watches: it starts the signer's task, or runs `hermesh-signer setup` elevated (one UAC prompt). The refusals of the next point each have an exit status, which the app tells in its own words.
- **Updates.** The signer changes rarely, and refuses what it cannot show in full. When the app carries a newer signer than the installed one, Needs You offers to update it: the same elevated setup (one UAC prompt) copies the app's signer over the installed one, moving a still running one aside, keeps the keys and restarts the task.
- **Refusals.** With UAC off (`EnableLUA` 0), every program of an administrator runs elevated and the line is gone: such a PC cannot be a trusted device that also runs coding agents. Where elevation needs no consent ("Never notify"), Settings says so and recommends Administrator protection.

### 7.4 What an attacker can do

| Attack | Result | Why |
|---|---|---|
| A coding agent on this PC, not elevated | Can ask; cannot sign or change the panel | The key and the signer are at high integrity |
| A program running as the owner before the signer is set up or updated | Can swap the app's copy of the signer, which the owner's one UAC prompt then installs | The app's files are the user's (Setup.exe, §6.1; MSIX's would not have been); once signed (§6.4), the elevated setup can check the signer's publisher |
| A coding agent that elevates without consent (a UAC bypass) | Can sign as the owner while elevated | UAC is not a formal boundary; Administrator protection makes it harder; removing this device from another one ends it |
| Malware running as administrator | The same | An administrator owns the PC |
| Another, non-administrator user of the PC | Nothing | The key, the pipes and the data folder admit only this user |
| A stolen PC | Needs the owner's Windows Hello | Hello's PIN is guarded by the TPM's lockout |
| A copy of the disk | Nothing | The key never leaves the TPM |
| A center that sends a join elsewhere | Fails | The two digits, as for any device |

## 8. WSL

A WSL distribution is a Linux machine: `install.sh` inside it pairs it like any other. Native Windows sessions do not reach into WSL, since paths, git and process control would cross a VM boundary.

Later, the app can list distributions and offer "Add as a machine". WSL stops a distribution that nothing on Windows uses, so the Windows daemon would keep the distribution's daemon running as a supervised child (`wsl.exe -d <distribution> -- hermeshd run`). Its traffic goes through the relay like any machine's, until the separate project for agents on the same computer.

## 9. Plan

Every phase ends with its tests green, end-to-end tests included (AGENTS.md §4).

| Phase | Work | Done when |
|---|---|---|
| W0. Seams (built) | `internal/paths` and `internal/localipc`, holding today's Unix code, with their contract tests; the process, health, lock and hook code split by OS; Windows files that compile and report "not supported yet"; `GOOS=windows go vet ./...` in `make lint` | Every current test passes; the Windows vet passes in CI |
| W1. A Windows machine (built) | The Windows halves of the seams; agents on Windows (locations, shims, hooks); health; `fakeacp` for Windows; `.exe` release assets; `install.ps1`; Windows in hermeshd.md; the Go suite on `windows-latest` | On a clean Windows 11, `install.ps1` pairs from the iPhone, and Claude Code and Codex sessions start, answer, interrupt, show diffs and archive; `make e2e` passes on `windows-latest`; `make e2e-real` passes on a Windows 11 VM |
| W2. The app (built; §14) | `platform/win.ts`; window, tray, notifications, menus, shortcut labels, words, paths; Setup.exe, signed feed, release job; the desktop e2e suite on Windows. The first run joins as a machine only, approved from another trusted device | `TestDesktop` passes on `windows-latest`; the packaged test installs the signed package and finds the daemon, the aliases and the protocol; an update between two signed builds works |
| W3. A trusted device (built; §14) | Preview vectors; `internal/signer`; `hermesh-signer.exe` and its protected setup; the TPM key; Windows Hello; signer status and show through the daemon; the refusals of §7.3 | Approve, renew, add device, remove device and join pass e2e on Windows with the test signer; a medium-integrity program can neither open the key nor touch the signer; a checklist with real Windows Hello hardware passes |
| Later | WSL distributions as machines from the app; machines that run with nobody logged on; a Linux desktop app on the same seams and signer core | Per their own plans |

W1 comes first because it is useful alone: a Windows PC becomes a machine for an owner who approves from the iPhone or a Mac.

## 10. Spikes

Each answers yes before the phase that needs it, or this document changes.

| Spike | Questions | Before |
|---|---|---|
| S1. MSIX (dropped with the Store, §6.1) | The daemon's startup task at logon; aliases on PATH; the protocol; toast buttons while the app is quit; Store updates: whether they wait for the package's processes or end them, and whether the app can hold one (`StoreContext`); the Store's review of a package with startup tasks, aliases and full trust; the data folder's exclusion, seen from a program outside the package; agents breaking away from it. Fallback: Squirrel.Windows | W2 |
| S2. Pipes | Electron main ↔ hermeshd over a pipe: unary calls, `Watch`, cancellation; another local user refused; the name file across daemon restarts | W1 |
| S3. Jobs | Creating a process with the job and handle lists from Go; a whole tree (Claude Code → PowerShell or bash → node → its children) ends on Stop and when hermeshd dies; inside the package; with Codex's own sandbox processes | W1 |
| S4. Agents | Claude Code, native, with and without Git Bash, in stream-json; which shell runs hooks for Claude Code and for Codex, and a user name with a space; Claude Code's inbox on Windows; Codex's app-server and sandbox under our job; where the Codex app keeps its CLI | W1 |
| S5. Protected signer | The elevated task starts without a prompt; a medium-integrity program of the same user can neither open the key through NCrypt nor load it through TBS; `UserConsentVerifier` works from the elevated signer with our window and message; the panel takes the foreground; the elevated setup step runs from inside the package | W3 |
| S6. Unsigned programs | What a first run of `install.ps1`'s programs, and of a browser download, looks like with SmartScreen and Smart App Control on; what Defender says | W1 |

## 11. Tests

| Level | What |
|---|---|
| Contract, every OS | `localipc`: only this user connects, another user is refused, the endpoint survives a restart; process trees: the whole tree ends on Stop, on Kill and when its parent dies; `service`: install, status, restart and uninstall under a scratch name; `shellenv`: a PATH change shows without a restart; `paths`: Go and TypeScript agree |
| Go end-to-end | The whole suite on `windows-latest`, with `fakeacp` built for Windows |
| Real agents | `make e2e-real` on a Windows 11 VM: Claude Code, with and without Git Bash, and Codex |
| Desktop end-to-end | Playwright drives the app on `windows-latest`, in Chinese as on the Mac; a test-only language switch stands in for `-AppleLanguages` |
| Install | `TestWindowsInstall`: `install.ps1` as a fresh user pairs and runs a session; run again, it updates while the daemon runs |
| Packaged | `check-windows-setup.ps1`: installs from Setup.exe; checks the shortcut, PATH, the programs, the logon task and the protocol; opens the app, which starts its daemon; uninstalls, and checks what went and that the data folder stays |
| Updates | `check-windows-update.ps1`: two builds, the older finding, checking and installing the newer from a signed feed; opened again, the app is the newer one, and the daemon comes back on its programs |
| Signer | The test signer (a software key) in every e2e test; security tests: a medium-integrity program cannot use the key or touch the signer; a checklist with real Windows Hello hardware for each release |

## 12. Decisions

Taken by the owner on 2026-10-30: function first, and as much security as Windows lets Hermesh build.

| # | Decision | Decided |
|---|---|---|
| 1 | Which Windows | Windows 11 22H2 or later, x64 and arm64, for the app and the trusted device; machines also on Windows 10 22H2 and Server 2019 or later, best effort |
| 2 | Packaging | A per-user Squirrel.Windows Setup.exe, unsigned, from GitHub and the website, updated from a feed signed with the release key (§6.1, §6.2); decided 2026-10-31, in place of MSIX through the Microsoft Store |
| 3 | Signing | None: open-source releases, unsigned like the Linux binaries and checked through the signed checksum list; SignPath or a certificate later if warnings keep people out |
| 4 | The trusted device | The protected signer on administrator accounts, with coding agents allowed on the same PC; the risk that remains (§7.4) is each owner's to accept; standard accounts approve from another device |
| 5 | WSL | Distributions are Linux machines; the app's "Add as a machine" later |
| 6 | Order | W0, W1, W2, W3: a Windows machine first |

## 13. Costs and risks

| Cost or risk | Mitigation |
|---|---|
| Agents' Windows support moves fast (Codex still called its own experimental in 2026) | Locations as data; real-agent tests on a VM before each release |
| UAC is not a formal security boundary | Said in §7; refused with UAC off; Administrator protection recommended; a device removed elsewhere stops signing |
| Unsigned programs meet SmartScreen and Smart App Control | Said where the app is downloaded (the release's text); updates come through the app, past SmartScreen; SignPath or a certificate later, without other changes |
| Defender and SmartScreen distrust new Go programs | Signing; version resources; reporting false positives to Microsoft |
| Antivirus scanning slows worktrees and builds | Worktrees on a Dev Drive, later |
| Mac-first work breaks Windows | Windows in CI from W0; a contract suite per seam |
| The desktop e2e suite takes over the computer it runs on | CI runners and VMs, not the owner's PC |
| More to keep up | No new language: Go and TypeScript only, the Windows signer included |

## 14. Built, then checked on a real PC

All of W0 to W3 is built (2026-10-30). CI checks on `windows-latest` what it can: the Go suite, `install.ps1`, the signer over its pipe with a test key, the desktop spec for Windows against the real daemon, and the app installed, opened, updated and uninstalled from its Setup.exe. What only a real PC shows, checked in one pass:

| Check | How |
|---|---|
| The app installed from its Setup.exe | Downloaded through a browser: SmartScreen's warning, once; Start lists Hermesh, `hermesh` and `hermeshd` run from a new terminal, a `hermesh:` link opens the app |
| The daemon at logon | Sign out and in: the logon task starts hermeshd; Startup apps lists "Hermesh" once Open Hermesh at login is on |
| The window | Caption buttons over the top band, the menu button, Ctrl shortcuts, the words of a PC, light and dark |
| The notification area | The icon in the taskbar's colours, the panel above it, the right-click menu; closing the window keeps the app |
| Joining as a machine only | Approved from the iPhone or a Mac; sessions run from there |
| The protected signer (S5) | Make this PC a trusted device: one UAC prompt, the task under Task Scheduler, the TPM key; approve a pairing with the panel and Windows Hello; a program not elevated cannot sign (it has no PIN) nor click the panel |
| The refusals | A standard account, User Account Control off, Windows Hello not set up |

Checked on a Windows 11 arm64 VM (2026-10-31), with the MSIX package of then: the app from its package, its window and its start; the signer's setup with one UAC prompt, the TPM key, the panel and Windows Hello, and the refusal without Windows Hello; joining as a trusted device, being removed, and joining as a machine only. Not yet: a standard account, User Account Control off, a program not elevated trying the key and the panel, a `hermesh:` link, and everything about Setup.exe on a real PC.

Left for later, each its own change:

- Preview vectors shared by the Go and Swift signers (desktop-app.md §5.6).
- Administrator protection (Windows 11 24H2 and later, where Microsoft offers it) asks for Windows Hello at every elevation, which closes the UAC-bypass row of §7.4: recommended to owners here. The app says nothing of it yet: whether a PC has it, and where it is turned on, isn't settled enough to point at. It does warn, in Account & Devices, when User Account Control is off or set to Never notify.
- The Mac's desktop specs that stay off Windows: their menu bar, Touch ID, moving to Applications, the process limit (health), and tests whose mechanics are Unix's (a proxy on a Unix socket, stopping a machine by its process group, a Codex that is a shell script). Eight specs run there (`windowsSpecs`, e2e/desktop_test.go).
- A session now and then missing from a list on `windows-latest`: the sidebar of `sessionlist.spec.ts` lacked one session after its older page (neither page had it), and `TestSessionList` waited on the owner for one. Both point at the owner's replica, slow runners showing it; to look into, so `sessionlist` stays off Windows until then (`TestSessionList` logs what is missing).

## Appendix: sources

Checked 2026-10-30.

**Coding agents**

- Claude Code on Windows (native install, Git Bash optional, PowerShell tool, no native sandbox, WinGet and npm): https://code.claude.com/docs/en/setup
- Codex's Windows app and sandbox (third-party reports): https://www.neowin.net/news/openai-releases-the-much-awaited-codex-app-for-windows/, https://codex.danielvaughan.com/2026/05/14/codex-cli-windows-sandbox-engineering-restricted-tokens-acls-elevated-architecture/

**Electron**

- `autoUpdater`, MSIX and Squirrel.Windows: https://www.electronjs.org/docs/latest/api/auto-updater
- Electron 41 (MSIX updates with the Squirrel.Mac JSON format; Windows notification actions): https://www.electronjs.org/blog/electron-41-0, https://github.com/electron/electron/pull/49787
- Login items in packaged apps: https://github.com/electron/electron/issues/42016

**Windows**

- MSIX flexible virtualization: https://learn.microsoft.com/en-us/windows/msix/desktop/flexible-virtualization; a packaged Electron app meeting it: https://github.com/anthropics/claude-code/issues/47977
- Process attributes (job list, handle list, desktop app policy): https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute
- Task Scheduler restart on failure: https://learn.microsoft.com/en-us/windows/win32/TaskSchd/taskschedulerschema-restartonfailure-settingstype-element
- `UserConsentVerifier` for desktop apps (build 22000): https://learn.microsoft.com/en-us/windows/win32/api/userconsentverifierinterop/nf-userconsentverifierinterop-iuserconsentverifierinterop-requestverificationforwindowasync
- WebAuthn API: https://learn.microsoft.com/en-us/windows/win32/api/webauthn/nf-webauthn-webauthnauthenticatorgetassertion
- Administrator protection: https://learn.microsoft.com/en-us/windows/security/application-security/application-control/administrator-protection, https://mc.merill.net/message/MC1506291
- go-winio's pipe listener (remote clients refused, exclusive first instance, SDDL): `pipe.go` in github.com/Microsoft/go-winio v0.6.3

**Signing and distribution**

- Code signing options and Artifact Signing eligibility: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options, https://learn.microsoft.com/azure/artifact-signing/quickstart
- SmartScreen reputation; EV no longer skips it: https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation, https://www.todesktop.com/blog/posts/windows-apps-psa-ev-certs-do-not-grant-immediate-reputation-anymore
- Free Microsoft Store registration: https://blogs.windows.com/windowsdeveloper/2025/09/10/free-developer-registration-for-individual-developers-on-microsoft-store/
- SignPath Foundation: https://signpath.org/
- Windows on Arm runners for public repositories: https://blogs.windows.com/windowsdeveloper/2025/04/14/github-actions-now-supports-windows-on-arm-runners-for-all-public-repos/

# Hermesh Desktop: Hybrid Architecture

Status: accepted 2026-10-19, being built. It replaces [technical-design.md](technical-design.md) §7 and settled decision #3, and amends decision #4.

## 1. Decision

The owner console becomes an **Electron app** whose windows run a React UI bundled inside the app. It shares design tokens and components with the web console.

Everything security-critical stays **outside Electron**:

- **hermeshd** (Go, unchanged) is still the only process that talks to relays and machines.
- **A small native signer** (Swift on macOS; one per OS later) holds the root key. It renders what is about to be signed from the bytes being signed, and asks for Touch ID.

The UI can ask the daemon to do things. It can never sign, and it never sees a signing request.

```
                    ┌──────────────────── Hermesh.app ────────────────────┐
                    │                                                     │
  user ── window ──▶│ Renderer (React, sandboxed)                         │
                    │      │ allowlisted LocalService calls               │
                    │      ▼                                              │
                    │ Electron main (TS)                                  │
                    │   tray · notifications · deep links · menus ·       │
                    │   login item · updates · API gateway ──┐            │
                    │                                        │ Unix socket│
                    │ Hermesh Signer.app (Swift) ────────────┤            │
                    │   root key (Secure Enclave, Keychain)  │            │
                    │   confirmation panel · Touch ID        ▼            │
                    │                              hermeshd (Go, LaunchAgent)
                    └──────────────────────────────────────┼──────────────┘
                                                           │ WSS + E2E TLS
                                                     relay / machines
```

The signer and the UI never talk to each other. The daemon is the only coordinator.

## 2. Goals and non-goals

**Goals**

- One UI codebase for macOS, Windows and Linux, rendered by the same engine everywhere.
- Ship the locked redesign (prototype v5) faithfully. The prototype is HTML, so it ports directly.
- Keep the trust model of technical-design §2.2: the hosted center is untrusted, and the owner signs what they see.
- Drive the real app in end-to-end tests. The SwiftUI app cannot be automated this way.

**Non-goals**

- No change to the relay, center, machines, CLI or wire protocols. The only proto changes are in `hermesh.local.v1` (§5.3).
- Windows and Linux are designed here (§9) and built later.

## 3. Options considered

| | Electron | Tauri 2 | Native shell per OS + system WebView | Keep SwiftUI |
|---|---|---|---|---|
| Same rendering on all OSes | Yes (bundled Chromium) | No: WKWebView / WebView2 / WebKitGTK | No | n/a (Mac only) |
| Languages added | TypeScript | TypeScript + Rust | TypeScript + C# + C/GTK | Swift + C# + … |
| Bundle / idle memory, same app, macOS ¹ | 323 MB / 128 MB | 57 MB / 109 MB | small | small |
| macOS notification actions | Yes (UNNotification since Electron 42) | Mobile only | Yes | Yes |
| E2E driving | Playwright `_electron`, first-class | WebDriver, not on macOS | Hard | Not possible here |
| Comparable products | Claude, Codex (now also the ChatGPT Mac app), Docker Desktop, Bitwarden, 1Password, VS Code, Slack | GitButler, Yaak; one small app on this Mac | ChatGPT Classic | Raycast, Ghostty |
| Maturity | Electron 44.9 stable (2026-10-17); 3 majors supported | 2.12 stable, 3.0 alpha | n/a | n/a |

¹ Better Stack built the same app in each framework. The often-quoted "30 MB vs 300 MB" gap does not hold for idle memory.

**Choice: Electron.**

- Reason 1: one engine on three OSes. WebKitGTK on Linux is the weak spot of every system-WebView option.
- Reason 2: one language for the UI and the shell.
- Reason 3: the best end-to-end testing story.
- Reason 4: the same structure as the products closest to ours:
  - **Docker Desktop** pairs a Go backend over Unix sockets with an Electron UI, and does Touch ID in the Go backend, not in Electron.
  - **Codex** keeps its hardware-key owner in a separate `.app` with its own provisioning profile and keychain group, and talks to it over stdio.
- Cost: about 300 MB on disk and about 130 MB idle; today's app is a 62 MB bundle. See §11.

## 4. Processes and trust

| Process | Language | Lifetime | Can | Cannot |
|---|---|---|---|---|
| hermeshd | Go | LaunchAgent (`SMAppService.agent`), always on | Everything the owner device key allows; asks for root signatures | Sign with the root key |
| Electron main | TS | While the app runs; starts at login | Allowlisted LocalService calls, tray, notifications, deep links, files the user picks | See or answer signing requests; touch the root key |
| Renderer | TS/React | Only while a window is open; destroyed on close | Whatever main exposes | Node, the socket, the network, remote content |
| Hermesh Signer | Swift | Started and restarted by Electron main; about 15 MB | Create the root key, show the confirmation, sign | Anything else: it serves only the daemon's signing stream |

**Threat model.** Trusted: the daemon, the signer, and the OS (Secure Enclave, Touch ID UI).

The renderer is treated as **compromisable**, because it shows content written by others: agent output, diffs, file names, labels chosen by remote agents.

A compromised renderer, or even a compromised Electron main, gets the same power that any process of the user already has over the socket today. It can read sessions, answer questions, interrupt, archive, add projects and revoke. It **cannot add trust**: every Grant, MachineCert, RootCert or device addition goes through the signer, which shows the true content.

## 5. Signing

### 5.1 Flow (example: an agent asks to connect)

1. A notification ("Grok Bot 请求连接") opens the approval sheet in the UI. The sheet shows who is asking and lets the owner choose the scope (projects, coding agents, expiry). The owner clicks "批准".
2. The UI calls `ApproveClientPairing(scope)`. The daemon builds the Grant and posts a `SigningRequest` batch.
3. The signer receives it. It parses the body and refuses anything it cannot fully show (§5.6). It brings a compact confirmation panel to the front, and starts Touch ID at once with the prompt embedded in the panel (`LAAuthenticationView`). The panel shows:
   - The verification code, computed from the bytes, with "与对方显示的一致吗？" (Does it match what the other side shows?).
   - The scope.
   - The full-trust warning.
4. On a touch, the Secure Enclave signs. The signer completes the request, the daemon verifies the signature, and the RPC returns. The UI shows the result. "拒绝" (Reject) makes the RPC fail with `ErrSigningRejected`.

It is still one decision and one Touch ID (trusted-devices.md §4.6). The UI is where the owner configures; the signer is where they verify and sign.

### 5.2 What changes for the owner

- **The verification code is now compared in the signer panel**, at the moment of signing. Today it is a separate first step of the approval window. The UI may show the code for reference but no longer asks for the comparison.
- **The "quiet" path is gone** (`Expected.swift`). Today the signing window is skipped when a batch matches what the native approval window showed. The approval UI is now web and so untrusted, which means the signer always shows its panel. The panel is compact and starts Touch ID by itself, so the owner only reads and touches.
- After a device approval, the two-digit join code is shown by the signer, which computes it from the RootCert it signed (as today).

### 5.3 Key creation goes through the daemon

`Setup` and `RequestJoin` stop taking `root_public_key` from the caller. The daemon asks the signer for a new key on the same stream (a key request next to `SigningRequest`), and the signer replies with the public key.

This means the UI never handles keys, and the signer has a single counterpart.

The sign-in verifier, created when the owner signs in through the browser, is kept by Electron main (0600 file, as today), never by the renderer.

### 5.4 Key storage

The root key moves from the `app/root-key.se` file into the **data-protection Keychain**:

- Created with `SecKeyCreateRandomKey` + `kSecAttrTokenIDSecureEnclave`.
- Access control `privateKeyUsage + userPresence` (kept, so Macs without Touch ID can still sign with the login password).
- Stored in keychain access group `<TEAM>.com.hermesh.signer`.

Only binaries signed by our team with that entitlement can use the key, and only the signer has it. This needs a Developer ID provisioning profile embedded in `Hermesh Signer.app`, which is the pattern Codex (`CodexCLI.app`) and Claude (`hwkey` group) ship.

Development builds without a profile fall back to a file blob per key, `app/root-keys/<key id>.se`. The product has not launched, so existing development identities are simply reset.

The signer keeps one root key per profile hermeshd has (trusted-devices.md 4.8): creating a key keeps the others, each `SigningRequest` names the key to sign with (`root_key_id`), and hermeshd asks for a key to be deleted (`delete_root_key`) once its profile left the owner.

### 5.5 Who started this?

For every RPC that leads to signing, the daemon records the calling process: its pid from `LOCAL_PEERPID` / `SO_PEERCRED`, and its executable path. That process is attached to the `SigningRequest`.

The signer shows "Requested by the Hermesh app" when the caller is our own app bundle. Otherwise it shows "Requested by <program>" with a warning.

This is a hint, not a guarantee. It still matters on an owner Mac that also runs coding agents with full access (the default), because any of them can call the socket.

### 5.6 Hardening folded into the signer

- The signer refuses a body that fails to parse, or that carries fields the signer does not know. Today such a body renders as empty, or the unknown fields are signed without being shown.
- It signs exactly the batch it displayed. Today the live list is signed 150 ms after the click.
- It removes `touchIDAuthenticationAllowableReuseDuration = 10`, so a recent Touch ID unlock of the Mac does not count as approval.
- Golden vectors: Go generates `(type, body) → expected summary` cases, and every signer implementation must pass them. This replaces and extends `--check-expected`, and keeps the macOS signer and future signers in step.

## 6. The UI process

### 6.1 Renderer security

- `sandbox: true`, `contextIsolation: true`, `nodeIntegration: false`.
- The UI is served from a bundled `app://` scheme (`protocol.handle`, path-traversal checked); there is no `file://`. CSP is `default-src 'self'`.
- Navigation and new windows are blocked. External links go through main, which opens https only, to an allowlist.
- Fuses: RunAsNode, NodeOptions and CLI inspect off; ASAR integrity, OnlyLoadAppFromAsar and cookie encryption on; GrantFileProtocolExtraPrivileges off. This is the profile Claude, Bitwarden and Codex ship.
- Agent text is rendered as Markdown without raw HTML. Diffs and file names are always treated as text.
- Code blocks that name their language, and shell commands in tool rows, are coloured by Shiki: its tokens become React elements, never HTML, and it runs on its JavaScript regex engine, as the CSP allows no WebAssembly. Each language loads the first time it is shown.

### 6.2 Talking to hermeshd

The renderer uses the generated Connect client (`@connectrpc/connect`), as the console does. Only the transport differs.

- **The gateway lives in Electron main.** It forwards to the daemon socket and drops two RPCs: `WatchSigningRequests` and `CompleteSigningRequest`. The renderer never sees the socket path.
- **Default implementation:** a privileged `hermesh-api:` scheme. Main proxies each request to the socket, over HTTP/1.1 or h2c; hermeshd already serves both. The renderer then uses stock `connect-web`.
- **Fallback, if the spike (§10, S1) shows a problem with streaming or cancellation:** a typed preload bridge, with server streams over `MessagePort`s.
- The local API has no bidi streams, so HTTP/1.1 suffices.
- **Sessions are pushed and paged, as in a chat app.** The window lists the newest page (`ListSessions` with `limit` 50), and the next older page whenever the end of the sidebar comes into view ("Loading earlier sessions…", then "No earlier sessions"). The first page also carries every session that needs the owner or runs, however old, so Needs You and In Progress are always complete. The window follows one `LocalService.Watch` ([state-sync.md](state-sync.md)): the first message carries the status, machines, grants, projects, devices, pairing requests and the first page of sessions (BASIC: without the replies no list shows), later ones what changed; a reset after a reconnect merges its sessions, a session's later state winning, so older pages stay. Nothing is polled. A session on a machine that cannot be read now shows "State unknown" and counts neither as in progress nor as needing the owner. Search and the overview's "Today" work over the sessions loaded so far.

### 6.3 Main-process services

| Need | How |
|---|---|
| Menu bar | `Tray` with a template icon that opens a small panel of the menu material under it: whether agents can reach this Mac, how many sessions need the owner (pairing requests included) and how many run (each count opens the first one), Open Hermesh, Settings, Quit Hermesh. Its renderer is closed 30 s after it hides |
| Notifications | `Notification` (UNNotification API, needs a signed build), with actions. Main runs the same notice logic as the UI (§7, `@hermesh/core`) on what its own `Watch` pushes (the first page of sessions, with everything that needs the owner or runs, then each change, and the pairing requests); a question still waiting when the app starts is notified again, since macOS drops an app's notifications when it quits. A session an agent starts is announced (silently) once it was created after the app's first look, so the sessions from before, or of a machine coming back, are no news |
| Deep links | `setAsDefaultProtocolClient("hermesh")`, plus `open-url` (macOS) / `second-instance` (Windows/Linux) |
| Login item and daemon | `setLoginItemSettings` with `type: "mainAppService"` for the app, and `type: "agentService"` + `serviceName: "com.hermesh.app.daemon.plist"` for hermeshd |
| Signer | Spawns `Contents/Helpers/Hermesh Signer.app` at start and restarts it if it exits. The signer exits when its parent does |
| Menus and shortcuts | Native `Menu` with the commands of today's `MainCommands`; native context menus via `Menu.popup` |
| Windows | Main window, plus a separate Settings window (⌘,). The renderer is destroyed when a window closes |
| OS helpers | Pick a directory (`dialog`); run one of a fixed set of commands in Terminal (e.g. `codex login`, chosen by key, never by renderer text); clipboard |
| Updates | Electron's `autoUpdater` (Squirrel.Mac) with a JSON feed per architecture on GitHub Releases; see §6.5 |

### 6.4 Feeling native on macOS

- Hidden-inset title bar with positioned traffic lights; the sidebar is the design's own inset panel.
- System font and 13 pt base size on macOS (Inter stays for the web). Default cursor on controls.
- Native menus and context menus. `nativeTheme` for dark mode; reduced motion respected; window state restored.
- No Liquid Glass: solid surfaces, as in Codex and Claude (the owner's decision).
- Platform differences live in tokens (`data-platform="mac|win|linux|web"`), not in separate components.

### 6.5 Updates

**What comparable apps do on macOS** (2026-10-19; bundles inspected on this Mac, plus their documentation):

| App | Mechanism | Checks | Download | Telling the user | Restart | Settings |
|---|---|---|---|---|---|---|
| VS Code | Squirrel.Mac | At start and in the background | In the background | "Restart to Update" in the Manage (gear) menu, with a badge | Never by itself | `update.mode`: default (background checks), start (at start only), manual, none |
| Slack | Squirrel.Mac | In the background; "Check for Updates" in the app menu | In the background | A badge on the help icon; "Restart to Apply Update" | Never by itself | IT can turn automatic updates off per user, and uses that to roll versions out gradually |
| Claude | Squirrel.Mac, `serverType: "json"`, a feed per architecture | At start and in the background | In the background | "Restart to Update to {version}" | Never by itself | None for owners; a version cap in managed configuration |
| ChatGPT (Codex) | Sparkle, EdDSA-signed appcast | Sparkle's schedule | Sparkle: a window with the release notes, or silently when automatic updates are on | Sparkle's update window | Installs on quit | Sparkle's "automatically download and install" |
| 1Password 8 | Its own | In the background; About › Check for Updates | Optional ("install updates automatically") | A restart prompt in the main window | Never by itself | Release channel (Production, Beta, Nightly) |

They agree on the essentials: check in the background, download without asking, say so quietly, and never restart under the user. None of them interrupts with a dialog.

**What Hermesh does** (`apps/desktop/src/main/updater.ts`):

- **Entry points.** Hermesh › Check for Updates… and the menu bar panel's Check for Updates… open one reusable Updates window. About Hermesh shows the installed version and links to updates and What's New; Help › What's New opens the in-app history. A ready update adds Show Update Details… and Restart to Update to the app menu. General contains only Download updates automatically.
- **Quiet discovery.** A compact Update Ready row above the sidebar account opens details. With automatic download off it says Update Available instead. There are no background dialogs, sounds or system notifications, and checking/downloading/errors never take over the main window. Later hides the sidebar row for that version for 24 hours, across launches; menu entries remain available.
- **Checks.** The first background check waits at least 30 seconds after launch, and runs only if the last successful check is at least four hours old. Last success, last attempt and failure count live in `updates.json`. Success schedules another check four hours later; failures retry after 5 minutes, 30 minutes, then 2 hours. Wake and network recovery debounce for 10 seconds and respect freshness/backoff. Manual checks bypass both; checks during checking, downloading or ready reuse the current operation. The architecture-specific feed is `https://github.com/hermeshai/hermesh/releases/latest/download/RELEASES-darwin-<arch>.json`; 404 means no update. An invalid or unavailable feed is a check failure.
- **Downloads and verification.** Automatic download defaults on. Squirrel.Mac downloads the ZIP and verifies the same Developer ID signature as the running app. A manual-download preference waits for Download Update. The Updates window uses indeterminate progress because Squirrel exposes no download percentage on macOS. Closing that window leaves checking/downloading running.
- **Window actions.** Available: Download Update / Later. Downloading or checking: Close. Ready: Restart to Update / Later. Current: Done and the last successful check time. Failure: Try Again / Close, a localized recovery explanation, and collapsed Technical Details. A notes failure never prevents downloading; the browser link remains available.
- **Release highlights.** `src/locales/en/releases.json` is the editorial source, translated in `zh-CN/releases.json`. Each version has short improvements and fixes. Before tagging a release, write what changed for users since the last tag (`git log <last tag>..`) under `unreleased`, in both catalogs, then rename `unreleased` to the exact version (no `v`) and leave an empty `unreleased` for the next one; keep previous versions. `node apps/desktop/scripts/release-notes.mjs <version>` checks this and prints the GitHub release's text; the release workflow runs it first and builds nothing without them. `build-app.mjs` bundles the catalog for offline reading and includes it as the optional `hermeshNotes` extension in the Squirrel feed; older clients keep working. The Updates window shows the target release and expandable intermediate releases when versions were skipped. What's New shows the full available catalog, with a browser link to older GitHub releases. Release notes never open automatically after an update.
- **Restart protection and recovery.** A downloaded update installs when Hermesh quits, or immediately when Restart to Update quits and relaunches it. Later only snoozes the reminder: the window explicitly says quitting still installs. Pending pairings or signer work replace Restart to Update with Return to Approval / Later; the native menu rechecks the same guard. The signer sends only a busy bit over its parent stdout pipe and accepts SIGUSR1 to bring its existing panel forward; neither action approves anything. The selected session, the session list's view (grouping, sort, metadata, archived shown), sidebar/inspector state and non-secret question drafts survive relaunch. Secret answers are never written to the profile. Ordinary running sessions continue in the background daemon.
- **hermeshd follows the app.** It is a LaunchAgent and keeps running while the bundle is replaced. When the new app sees that `GetStatus.version` differs from its own version, it restarts the agent once (`launchctl kickstart -k gui/<uid>/com.hermesh.app.daemon`: SIGTERM, then launchd starts the binary now in the bundle). It waits while approval is pending or a session on this Mac is queued, preparing, running or waiting for an answer: `Recover` keeps every session after a restart, but marks a turn in progress as crashed, and the owner then has to send it again. The signer is the app's child and exits with it, so the new app starts the new signer.
- **Only packaged builds update.** Development and test runs never check. The feed's address is a build-time constant: release builds always read GitHub Releases, and only test builds (`HERMESH_UPDATE_FEED`, used by `TestDesktopUpdate`) read another. Nothing at run time changes it, and fuses keep the bundled code from being swapped.
- **A package per architecture.** Owners install from `Hermesh-macos-arm64.dmg` or `Hermesh-macos-x64.dmg` (§6.7). Squirrel.Mac needs a zip, so each architecture also has `Hermesh-macos-<arch>.zip` with its feed. An x64 build running under Rosetta on Apple silicon keeps reading the x64 feed.
- **Publishing.** The release workflow builds each architecture in its own job; `build-app.mjs` signs, notarizes and staples the app, then its DMG, and writes the feed, whose `url` is that version's zip (`releases/download/v<version>/…`), so a feed never names another version's zip. The DMG, zip and feed are uploaded to the release.
- **Pulling a bad release:** delete its feeds, or make an earlier release the latest. Apps that already downloaded it still install it when they quit.
- **Not now:** staged rollout (a static feed cannot do percentages, and there are few owners yet) and release channels.

### 6.6 Languages

The app speaks English and Simplified Chinese. English is the source language; Chinese is a translation of it.

- **Which language.** By default the first of the system's preferred languages (System Settings › General › Language & Region) that the app has, else English: `pickLanguage` in `packages/core/src/i18n.ts`, over `app.getPreferredSystemLanguages()`. As macOS picks a localization, Traditional Chinese does not take Simplified Chinese; the next language in the list does. That list also carries the per-app language macOS offers for Hermesh (the same pane, Applications), because the bundle declares exactly two localizations: `build-app.mjs` keeps only `en.lproj` and `zh_CN.lproj` of Electron's folders and sets `CFBundleLocalizations`. Settings › General › Language can override it ("System (English)", "English", "简体中文"); the choice is a preference of the main process.
- **Once per run.** The main process picks the language when it starts (`main/language.ts`), before any menu or window exists, and hands it to each page (`--hermesh-lang`, `window.hermesh.language`) and to the signer (`-AppleLanguages`, the argument macOS itself reads). A new choice takes effect after a restart (the setting offers Restart Now), as macOS does for its per-app language. So no text is cached at a module's top level, and nothing re-renders on a change.
- **Text.** One i18next instance (`packages/core/src/i18n.ts`) serves the main process and every page: namespace `core` for `@hermesh/core` (`packages/core/src/locales`), and per part of the app for the desktop (`apps/desktop/src/locales/<language>/<namespace>.json`: main, common, firstrun, overview, session, settings, sheets, updates; releases holds editorial summaries). Pages use react-i18next (`useTranslation`, `<Trans>` for text around links). Keys are typed from the English files, so a missing or misspelt key fails `tsc`; `src/shared/locales.test.ts` checks every language has English's keys, `{{values}}` and `<tags>`. Plurals follow `Intl.PluralRules` (`_one`/`_other` in English). Dates go through `Intl` with the app's language.
- **The signer** (`apps/signer/macos`) keeps English as its keys in the code (`String(localized:bundle: .module)`) and their Chinese in `Resources/zh-Hans.lproj/Localizable.strings` (a strings file rather than a String Catalog: only the newer SwiftPM build system compiles catalogs, and CI's copies them as they are). SwiftPM puts it into the resource bundle, which `build-signer.sh` copies into `Contents/Resources`; its `Info.plist` declares both localizations, which macOS requires before a resource bundle's translation is used.
- **Tests.** The end-to-end specs run the app as on a Chinese system (`-AppleLanguages (zh-Hans)`), so they also prove the translation reproduces the Chinese text; `language.spec.ts` covers English, the choice of language and the setting. `@hermesh/core`'s unit tests read Chinese, `i18n.test.ts` English. The signer's Swift tests and `TestSignerApp` read English.
- **Adding a language:** add it to `languages` and `pickLanguage`, add its files (core's and each desktop namespace), the bundle's `.lproj` folder and `CFBundleLocalizations` (app and signer), the signer's strings file, and its name to the language setting.

### 6.7 Installation on macOS

- **Disk image.** `build-app.mjs` makes a read-only, compressed DMG with Hermesh on the left and an Applications shortcut on the right. A light background and an arrow show the drag. `build/dmg/settings.py` fixes the Finder window and icon positions; the background has 1× and 2× versions. The build uses `dmgbuild`, pinned with hashes in a separate Python virtual environment; it needs no Finder scripting on CI.
- **First launch.** Before opening a window, registering its protocol or starting the daemon, signer or updater, a packaged Mac app checks `app.isInApplicationsFolder()`. Elsewhere (including Downloads, the disk image and App Translocation), it asks Move to Applications Folder or Quit, in the app's language. Electron's `app.moveToApplicationsFolder()` handles copying, administrator authorization when needed, an existing copy and relaunching. Failure explains how to drag the app in Finder, then quits. Development and isolated test runs skip relocation and background registration.
- **Signing and Gatekeeper.** The app is Developer ID–signed, notarized and stapled before it goes into either archive. The DMG is then signed, notarized and stapled too. Stapled tickets allow Gatekeeper to check both offline. The build assesses the app and DMG with `spctl` after notarization. The release workflow requires the signing identity and App Store Connect key; local builds without notarization credentials warn that they are not ready for distribution.
- **Checks.** `TestDesktop/install` drives the startup gate and localized update failures in the real Electron app, replacing the OS dialog, relocation and login-item calls to keep the developer's installation untouched. `HERMESH_DESKTOP_APP=<app> HERMESH_DESKTOP_DMG=<dmg> go test ./e2e -run '^TestDesktopPackaged$' -v` mounts the real DMG, checks its contents and signature, and launches its bundled app and signer against an isolated daemon. `TestDesktopUpdate` installs an update between two Developer ID–signed builds.

## 7. Code layout and sharing with the console

Everything users run is open source. So the shared UI moves **from the private console into this repository**, and the console consumes it.

```
hermesh/                                 Apache-2.0
├── pnpm-workspace.yaml
├── packages/
│   ├── proto/   @hermesh/proto          TS from hermesh/{local,identity,session}/v1 (buf, protobuf-es)
│   ├── core/    @hermesh/core           pure logic: session list grouping and sorting, search filters, notices, diff parsing, text (port of SessionLogic.swift)
│   └── ui/      @hermesh/ui             brand.css (Aurora colours, motion), the mark and lockup (moved from web/console), coding agents' logos
└── apps/
    ├── desktop/                         Electron: src/main, src/preload, src/renderer (Vite + React 19), e2e/
    └── macos/                           becomes Hermesh Signer.app only

hermesh-cloud/
├── pnpm-workspace.yaml                  web/console + hermesh/packages/ui
└── web/console                          imports @hermesh/ui; keeps its own pages, routing and center API
```

- **Stack:** React 19 and TypeScript like the console; the desktop page uses the prototype's CSS ported as-is. Built with electron-vite; packaged by `apps/desktop/scripts/build-app.mjs` with `@electron/packager` (one build per architecture), `@electron/fuses`, `@electron/osx-sign` and `@electron/notarize`. The main process bundles its dependencies, so the app ships no `node_modules`.
- **`@hermesh/ui` stays framework-free.** No `next/*` imports, no server components. Links and data access are passed in.
- **Two workspaces, one package.** Both `pnpm install`s link `packages/ui/node_modules`, so `@hermesh/ui` may see the other workspace's React: the desktop renderer dedupes `react` and `react-dom` (electron.vite.config.ts), and Next.js uses its own React. The cloud workspace lists only the packages the console uses.
- **Styles:** both apps import `@hermesh/ui/brand.css`. The package uses no Tailwind classes, so the console needs no `@source` for it yet (Tailwind v4 skips `node_modules`).
- **Visual spec:** prototype v5 for the desktop app; the console keeps its current look built on the same tokens.

## 8. Testing

| Level | What |
|---|---|
| Unit | `@hermesh/core` with vitest. Port the SessionLogic tests one-to-one first |
| Signer | Swift tests plus the Go golden vectors (§5.6) |
| End-to-end | Playwright drives the real Electron app (`_electron.launch`) against the real stack used by the Go e2e suite: relay, owner daemon and `fakeacp` sessions in every state (today's `TestAppUI` setup). Tests click through what users do: sidebar sections, open a session, answer a question, interrupt, archive and undo, diffs, the list's view menu, search, settings, onboarding |
| Signing end-to-end | The real signer in a test mode: software key, decision scripted by env. It writes what its panel showed as JSON, and tests assert it shows the true scope even when the UI asked for something else |
| Packaged app | CI on a macOS runner: build, sign, launch the `.app`, check daemon registration and the gateway |
| Updates | The feed, version order and update states in `apps/desktop/src/shared/update.test.ts` (`node --test`). `TestDesktopUpdate` (opt-in, `HERMESH_UPDATE_E2E=1`, needs the Developer ID certificate, 4 to 6 minutes): two signed builds A < B, B's zip and feed served locally; the app finds B, downloads it, shows Restart to Update, and restarts into B with its signer |
| Visual | Playwright screenshots of every screen, reviewed against the prototype |

## 9. Windows and Linux (later)

**Daemon**

- Windows: hermeshd runs per user, started at logon. The local API moves to a named pipe with a user-only ACL, because Node uses named pipes on Windows.
- Linux: a systemd user service, as today.

**Signer**

The signer protocol is platform-neutral (the stream in §5.1). Each platform needs its own key store and its own panel. A spike decides between:

| Windows option | Binding | Note |
|---|---|---|
| TPM P-256 via NCrypt (Platform Crypto Provider) + `UserConsentVerifier` with our message | Policy (Hello gates the app, not the key) | Simplest; keeps ECDSA P-256 |
| WebAuthn platform credential (ES256, Windows 11 22H2+) | Cryptographic (Hello gates the key) | Signatures come as WebAuthn assertions. Needs a new `SignatureAlgorithm` that verifiers understand; the `alg` field allows it |
| `KeyCredentialManager` | Cryptographic | RSA-2048 only. Bitwarden dropped it because of prompt failures in background windows |

On Linux: a TPM2 P-256 key with a PIN (go-tpm, as ssh-tpm-agent does) when `/dev/tpmrm0` is usable. Otherwise a software key in the Secret Service with a polkit prompt, marked as lower assurance.

## 10. Plan

Every phase ends with its tests green. The current app keeps working until phase 5.

| Phase | Work | Done when |
|---|---|---|
| 0. Spikes | **S1** Electron shell: `app://`, the API gateway (unary, server stream, cancellation), fuses; packaged, signed and notarized with `Helpers/hermeshd` registered through `setLoginItemSettings(agentService)`. **S2** Signer bundle: nested app with provisioning profile and keychain group; Secure Enclave key in the Keychain; a binary without the entitlement cannot use it; whether today's `.se` blob can be used by any process of the user; embedded Touch ID panel. **S3** `@hermesh/ui` extracted; the console builds on it | Each spike answers yes, or this document changes |
| 1. Foundations | pnpm workspace, `@hermesh/proto`, `@hermesh/core` with ported tests, desktop skeleton, Playwright harness on the Go stack | An empty window lists real sessions in e2e |
| 2. Signer | Signer target extracted from today's Swift code; key requests and caller info in `hermesh.local.v1`; Keychain storage; hardening (§5.6); golden vectors | Approve, renew, add device, remove device and join all pass e2e with the signer in test mode |
| 3. Main window | Sidebar, overview, session (chat, diff, info panel, control area, question), settings, sheets, first run, all matched to prototype v5 | Every flow of the walkthrough has an e2e test; screenshots reviewed |
| 4. Shell | Tray, notifications with actions, deep links, login item, menus and shortcuts, updates; CI build, sign and notarize | The packaged-app smoke test passes on CI |
| 5. Cutover | Delete the SwiftUI main window; release; update technical-design §7, §12 and §16 | The released app is the Electron one |
| Later | Windows (§9), then Linux | Per their own plans |

**The SwiftUI redesign now on `mac-app-redesign`** ships as the interim app. Its `SessionLogic` and tests become the specification for `@hermesh/core`. No further work goes into the SwiftUI main window.

## 11. Costs and risks

| Cost or risk | Mitigation |
|---|---|
| About 300 MB on disk (today: 62 MB) and about 130 MB idle | Destroy renderers on close (the menu bar panel's 30 s after it hides); measure with `app.getAppMetrics()` in CI |
| Electron majors every ~8 weeks; only 3 are supported | Upgrade at least twice a year; fuses and the e2e suite catch regressions |
| Not quite native: web form controls | Native menus, system font, platform tokens (§6.4) |
| Nested bundle with its own provisioning profile makes packaging more complex | Spike S2 first; Codex ships the same shape |
| Two UIs during the transition | No new SwiftUI features; cutover in phase 5 |
| The signer protocol must be implemented again per OS | Platform-neutral stream; golden vectors (§5.6) |

## 12. Findings, and the trust boundary on the owner's Mac

Fixed in phase 2:

- **The root key handle was a plain file.** Verified on 2026-10-19: a CryptoKit Secure Enclave blob is not bound to the program that created it; another program that reads the file signs with the key. With the Developer ID provisioning profile, the key lives in the signer's Keychain group instead, and neither an ad-hoc program nor one signed with the same team but without the group can find or use it (also verified). Development builds without the profile still use the file.
- **Signing preview gaps:** a body that fails to parse is shown empty; unknown fields are dropped silently; the batch is signed after the click; there is a 10-second Touch ID reuse window. §5.6 fixes these.

Decided not to fix (2026-10-19):

- **The local socket admits every process of the same user**, including coding agents running with full access on the owner's Mac. Checking callers' code signatures (as Codex's and Claude's apps do for some of their sockets) would not change what such an agent can reach: it runs as the owner and can read hermeshd's files (the device key, the session database) and drive the app's UI. What it cannot do is add trust: every grant, machine, device or wider scope needs the root key, i.e. the owner's Touch ID on the signer's panel, which shows what is really signed. So letting agents work on the owner's Mac means trusting them with everything on it except the root key. Stronger isolation, if ever needed, means running the agents sandboxed or as another user, not guarding the socket.
- **`AddProject` is unsigned**, though it widens grants whose projects are `*`: the owner removed its signature on purpose.

## Appendix: sources

**Frameworks and tooling**

- Electron releases (44.9.0 stable on 2026-10-17): https://releases.electronjs.org/
- Electron 42, macOS notifications on UNNotification: https://www.electronjs.org/blog/electron-42-0
- Electron security checklist: https://www.electronjs.org/docs/latest/tutorial/security
- Electron fuses: https://www.electronjs.org/docs/latest/tutorial/fuses
- Electron `app.setLoginItemSettings` with SMAppService types: https://www.electronjs.org/docs/latest/api/app
- Tauri releases: https://github.com/tauri-apps/tauri/releases
- Tauri WebView versions: https://v2.tauri.app/reference/webview-versions/
- Tauri Linux graphics issues: https://v2.tauri.app/develop/debug/linux-graphics/
- Measured bundle and idle memory, Electron vs Tauri: https://betterstack.com/community/guides/scaling-nodejs/tauri-vs-electron-vs-deno-vs-electrobun/
- connect-node transport options (`socketPath`, `createConnection`): https://github.com/connectrpc/connect-es/blob/main/packages/connect-node/src/node-transport-options.ts
- Tailwind v4 source detection: https://tailwindcss.com/docs/detecting-classes-in-source-files

**Updates**

- Electron `autoUpdater` (Squirrel.Mac, `serverType: "json"`): https://www.electronjs.org/docs/latest/api/auto-updater
- Squirrel.Mac, how the JSON feed is read and the update verified: https://github.com/Squirrel/Squirrel.Mac/blob/master/Squirrel/SQRLUpdater.m
- VS Code `update.mode`: https://code.visualstudio.com/docs/enterprise/updates
- Slack, updating the desktop app: https://slack.com/help/articles/360048367814-Update-the-Slack-desktop-app
- Slack, deploying for macOS (automatic updates off per user): https://slack.com/help/articles/360035635174
- 1Password release channels: https://support.1password.com/mobile-device-management/
- Bundles on this Mac: Claude, Slack and VS Code ship `Squirrel.framework`; Claude's app sets a `serverType: "json"` feed per architecture and shows "Restart to Update to {updateVersion}"; ChatGPT ships `Sparkle.framework` with `SUPublicEDKey`.

**Keys and platform authentication**

- Secure Enclave access follows the data-protection Keychain rules (Apple DTS): https://developer.apple.com/forums/thread/786171
- Windows `KeyCredentialManager` (RSA-2048 only): https://learn.microsoft.com/en-us/uwp/api/Windows.Security.Credentials.KeyCredentialManager
- Bitwarden dropping KeyCredential signing: https://github.com/bitwarden/clients/pull/14953
- `UserConsentVerifier` with a message: https://learn.microsoft.com/en-us/windows/win32/api/userconsentverifierinterop/nf-userconsentverifierinterop-iuserconsentverifierinterop-requestverificationforwindowasync
- ssh-tpm-agent: https://man.archlinux.org/man/ssh-tpm-agent.1.en

**Bundles inspected on this Mac (2026-10-19, read-only)**

- Claude 2.42304: Electron 44.7.2; Secure Enclave key in a Rust N-API addon; keychain group `…claude.hwkey`.
- ChatGPT.app = Codex (`com.openai.codex`): Electron-based. The hardware key is held by `CodexCLI.app`, which has its own provisioning profile and keychain group, and the app talks to it over stdio.
- Docker Desktop 4.95: Go backend with LocalAuthentication and `LAAuthenticationView`; Electron 43 UI over Unix sockets.
- Bitwarden 2026.9.1: Electron 43 with a Rust N-API core.

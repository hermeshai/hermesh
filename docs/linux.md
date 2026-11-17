# Her on Linux

Status: accepted 2026-11-17, with the owner's decisions in §10, after a cold-start test of Her on two fresh Linux computers (§2). §3 to §6 are built on the branch `her-linux`; §7 and §8 are designs. It takes up [desktop-app.md](desktop-app.md) §9 and the "Later" row of [windows.md](windows.md) §9 for Linux, and [her-server.md](her-server.md) §12 for computer use.

## 1. Goal

On Linux, Her is what it is on a Mac, on a server with only a terminal and on a computer with a desktop:

1. **A Her Server that can do what a Mac's can.** Every tool Her has on a Mac works on Linux where the computer allows it: documents with PDFs, the browser, computer use on a desktop.
2. **A conversation where the person is.** In the Hermesh app on a Linux desktop, as on a Mac; in the terminal on a server.
3. **Nobody needs an Apple device or a Windows PC.** A person whose only computer runs Linux sets Hermesh up, approves and talks to Her there.

**Which Linux.** Ubuntu 24.04 or later, Debian 13 or later and Fedora 41 or later, x86_64 and arm64, with systemd; GNOME first (Wayland and X11), Plasma second. Servers as today (any Linux with systemd, others with `hermeshd run`).

**What does not change:** the relay, the center, the iPhone app, the wire protocols, the signature format, and Linux as a machine for coding agents.

## 2. What was measured

On 2026-11-17, from a signed local release (`scripts/release.sh`) served over HTTP, with her built for Linux as the release workflow builds it (static, musl), against a relay and an owner on a Mac:

| | Ubuntu 24.04 server, root, terminal only | Debian 13 with a desktop (X11, Chromium), a user |
|---|---|---|
| The install command, pairing, the service at boot | works | works |
| Her turned on from the owner's device; her running; `hermeshd status` and `doctor` | works | works |
| The console, through the tunnel, on the owner's Mac | works | works |
| ChatGPT sign-in started from the console (§5.4 of her-server.md) | the authorization request is made and comes back to the Mac; not finished: nobody signed in | the same |
| Chrome extension's native host registered | no Chrome, as designed | works |
| The built-in browser | no program; `her install browser` has no arm64 build | a window; after a restart, headless (fixed, §3) |
| PDFs | `her install pdf`: HTTP 404 | the same |
| Computer use | "macOS" (built since, §5: off where there is no desktop) | "macOS" (built since, §5) |
| `her` in the terminal, with no model | asks how to connect one | the same |
| `her login chatgpt` over ssh | cannot finish (fixed, §3) | works with a browser there |

`TestLinuxHer` (the install command, Her on, an answer from her-mock, an update while Her works, uninstall) passes with that her.

**Not measured:** a conversation with a real model and its tools, which waits for a sign-in; x86_64; Wayland; Plasma.

## 3. Built on `her-linux`

- **A sign-in from a server reached over ssh.** `her login chatgpt` and `her mcp login` send the browser back to `http://127.0.0.1:<port>` on the server, which the person's own browser cannot open. The terminal now says so and takes that address pasted into it (`take_pasted_redirect`, `her/crates/her-cli/src/cmd_customize.rs`); the sign-in's own state check refuses any other. Test: `a_sign_in_takes_the_address_a_browser_elsewhere_ended_on`, on a pseudo-terminal.
- **The desktop's display, for a her started before anyone logged in.** hermeshd starts at boot (lingering) and her with it, without `DISPLAY`. her now asks the user's systemd for the session's display each time (`session_display`, `her/crates/her-browser/src/pool.rs`), as Computer Control's helper does. Tests: a unit test, and `the_built_in_browser_gets_a_window_once_someone_logs_in_to_the_desktop` on Linux.
- **`hermeshd status` without `USER`** (docker exec, cron, cloud-init) no longer says the service does not start at boot (`internal/service/service_unix.go`).

## 4. PDFs

**Why it fails.** her for Linux is static and cannot load a library. Its PDF component is a second program, `her-doc-worker`, linked against glibc, with `libpdfium.so` beside it; `her install pdf` fetches it from her's own releases, which are not published. The Mac app carries PDFium in its bundle.

**Built.** The Hermesh release carries it, as it carries her:

- `her-pdf-linux-<arch>.tar.gz`: `her-doc-worker` (glibc 2.28 floor, cargo-zigbuild, as `pdfLinux` in `her/tools/release/release.mjs`), `libpdfium.so` (the bblanchon release the Mac app pins, each archive's sha256 checked: `apps/desktop/scripts/pdfium.mjs linux`) and PDFium's licence; in the signed checksum list.
- `install.sh` unpacks it into `her-pdf/` beside her on a machine with glibc; the updater replaces it with her from the same release (the worker and her must match); `hermeshd uninstall` removes it.
- her looks for the worker and the library in `her-pdf/` beside itself, before its own `~/.her/addons`.
- Without glibc (Alpine), as before: Her says PDFs are not available there.

**Tests.** `TestLinuxHer`'s releases carry `her-pdf`: there after the install, another after the update, gone after uninstall. `TestLinuxHerPDF` (`HERMESH_DOCKER=1`, with the real worker and PDFium in `HERMESH_HER_PDF_LINUX`): `her doctor` finds the component, and Her reads the line of a PDF the owner sent her. `TestInstallHerFolders` for the updater.

**Left.** The release workflow's two new steps (the worker's build with cargo-zigbuild in `rust:<toolchain>-bookworm`, and the packaging) run only on GitHub; here the worker was built with cargo-zigbuild from the Alpine image, for arm64. x86_64 is not tried.

## 5. Computer use

**What changed since her-server.md §12.** her's desktop tools run `agent-desktop`, and the release Hermesh pins (1.0.2) has Linux programs, for x86_64 and arm64, through AT-SPI. Tried on the Debian desktop of §2 with that pinned program: `status` says accessibility is granted, `list-apps` and `snapshot` read a calculator (65 elements), four `click`s on its buttons made it show 5, and `screenshot` returned its window. Only her's own `cfg!(target_os = "macos")` keeps the tools off.

**Built.**

- **Shipped with her.** `her-computer-linux-<arch>.tar.gz` is a release file: agent-desktop and its clipboard helper, taken from the npm package by integrity (`apps/desktop/scripts/agent-desktop.mjs linux`). `install.sh` unpacks it into `her-computer/` beside her, a folder of its own that only the user writes to, which the clipboard helper insists on; the updater replaces it with her, and `hermeshd uninstall` removes it. It needs glibc (2.35), so not on Alpine.
- **On where there is a desktop.** hermeshd starts her with `HER_DESKTOP=on` on Linux too, and her counts it only while someone is logged in to the desktop: on a server, and before a login, `desktop/status` says off and the tools are not offered. The owner's own `[desktop] enabled` still decides either way.
- **In the desktop's session.** agent-desktop runs with the session's display (`session_display`, §3, which also carries `XAUTHORITY`, `XDG_CURRENT_DESKTOP` and `XDG_SESSION_TYPE`); the bus and the runtime directory are the service's own.
- **Permissions.** `desktop/status` reports what agent-desktop's `permissions` says: on Linux, accessibility is the AT-SPI bus, and screen recording is not needed on X11. Her's own approvals, once per app and conversation, are the same code as on the Mac.
- **Keys.** Text typed through the clipboard is pasted with Ctrl+V where the computer is not a Mac.

**What Linux allows.** Reading and acting on elements work on X11 and Wayland. Pointer gestures (drag, hover, clicking at a point of a screenshot) and bringing a window forward work on X11 only; on Wayland a screenshot asks at the screen once. Chromium and Qt apps need a flag to be readable.

**Tests.** `TestLinuxHerComputer` (`HERMESH_DOCKER=1`): an Ubuntu 24.04 desktop under Xvfb with a calculator; the install command, Her turned on before anyone logs in, then a login, after which Her (her-mock) lists the calculator and reads its window, and uninstall. `TestInstallHerComputer` for the updater; her's desktop, status and configuration tests pass on Linux.

**Left.** Her's tool descriptions do not yet tell the model what Wayland refuses; acting (clicks, typing) with a real model, GNOME on Wayland and Plasma are to be checked in the Lima desktop VM (`hdesk`), as `TestLinuxScreenVM` runs.

## 6. The browser

- Built: the window after a restart (§3).
- Built: `her install browser` on arm64 has no Chrome for Testing, and now says to install Google Chrome or Chromium there; Google Chrome has an arm64 Linux package since July 2026, and the console's Browser page gives the command for the computer's own architecture.
- To check: x86_64 with Chrome for Testing; Chromium from snap, whose native messaging hosts live elsewhere (her-server.md §6.4).

## 7. The app on Linux

The Electron app already has one module per system (`src/main/platform/{mac,win}.ts`) and one signer core in Go behind `Confirmer` and `KeyStore` (`internal/signer`), written so that "a Linux desktop app" could follow (windows.md §9). Three steps, as on Windows:

### 7.1 The app

`platform/linux.ts`, the token `linux`:

| | Linux |
|---|---|
| Daemon | the systemd user service, owner and machine (`hermeshd service install --owner`), with lingering |
| Open at login | `~/.config/autostart/hermesh.desktop` |
| Links | `hermesh:` through the desktop entry's `x-scheme-handler`, arriving as `second-instance` |
| Window | the caption overlay where Electron draws it; else the system's frame |
| Notification area | a StatusNotifier icon where the desktop has one (GNOME has none without an extension): closing the last window keeps Hermesh only where there is one |
| Notifications | the desktop's, through Electron |
| Terminal commands, folders, processes | `xdg-terminal-exec` else the desktop's terminal; `xdg-open`; the desktop's system monitor |
| Words | `key_linux` variants: "This computer", "your password" |

Her's panel, the console window and sign-ins are the page's and the daemon's: nothing of Her's changes.

### 7.2 Packages and updates

| | Installs as | Chromium's sandbox | A protected signer (§7.3) | Updates |
|---|---|---|---|---|
| **A. `.deb` and `.rpm`** | root, once | kept (setuid helper, AppArmor profile) | possible | the package manager, from a repository Hermesh signs; the app says when one waits |
| B. AppImage or a folder in the home | the user | lost on Ubuntu 24.04, whose AppArmor refuses user namespaces to unknown programs | not possible | the app replaces itself, as on Windows |

**Choice: A**, with a `.tar.gz` of the same folder for other distributions. The package carries hermeshd, hermesh, her with its console, PDF component and agent-desktop, and hermesh-screen; hermeshd's own updater is off there, as in the Mac app.

### 7.3 A trusted device

Today a Linux trusted device runs no coding agents ([onboarding.md](onboarding.md) §9.2): any program of the user could read what is typed into `hermeshd approve` and sign. The app makes the computer a machine too, so the rule has to go or be kept by other means.

| | Key | Who can use it | What the owner checks | Verdict |
|---|---|---|---|---|
| A. The passphrase key, a panel in the app | a file of the user's, sealed with a passphrase | any program of the user that sees the passphrase typed | a panel any program of the user can alter | only under today's rule: not on a computer with coding agents |
| B. A protected signer | a file (or a TPM key) of a system service, per user | only the service, which the package installs as root | the summary the service gives polkit, shown by the desktop's own password dialog, then the password or fingerprint | the Mac's promise against programs of the user on Wayland; weaker on X11, where they can read the keyboard; none where the user has sudo without a password |
| C. No trusted device on Linux | | | | a Linux-only person cannot use the app |

**Choice: B**, as option C was on Windows: `hermesh-signerd`, a system service the package installs. It knows the caller's user (peer credentials), keeps one key per user where only it reads, computes what is signed (`internal/preview`), and asks polkit for the user's authentication with that summary as the dialog's message; the app shows the full panel beside it. The signature format does not change; the key's label says "Linux · protected" and whether the session is X11. It refuses where sudo asks no password, as the Windows signer refuses with UAC off. A server keeps `hermeshd approve` and today's rule.

**Tests.** The desktop e2e suite under Xvfb in Docker, with the test signer; the package installed in a fresh container, finding the daemon, the links and the service; the signer's refusals; and by hand in the Lima VM: GNOME on Wayland, a real polkit dialog, a program of the user that tries to read the key and to click the dialog.

## 8. Her from a server's terminal

On a server, `her` in the terminal is a conversation of its own, not the assistant the apps talk to. `hermeshd her` opens Her's own conversation there: it connects to the her that hermeshd runs (its socket) and shows `bots/open`'s conversation in her's terminal interface. Turning Her on still takes a trusted device's signature; a Linux-only person has the app of §7 on their desktop for that.

## 9. Plan

| Phase | Work | Size | Done when |
|---|---|---|---|
| L0. A real conversation | The owner signs in on the two computers of §2; Her answers and uses each tool with a real model | S | A table of what works, tool by tool |
| L1. Small ones (built, but for the last) | §3; §6's messages; the console's timeout in the person's language; the install's last lines name Her | S | Their tests |
| L2. PDFs (built) | §4 | M | `TestLinuxHer` with `her-pdf` passes; a PDF read on Linux |
| L3. Computer use (built) | §5 | M | The Docker e2e passes; the VM by hand is left |
| L4. The app | §7.1, §7.2 | L | `TestDesktop` on Linux; the package test |
| L5. A trusted device | §7.3 | L | The signer's e2e; the VM checklist |
| L6. The terminal | §8 | M | A pseudo-terminal e2e |

L0 to L3 need no decision and no new program: they make a Linux Her Server equal to a Mac's. L4 and L5 are the size of windows.md's W2 and W3.

## 10. Decisions

Taken by the owner on 2026-11-17.

| # | Decision | Decided |
|---|---|---|
| 1 | The trusted device on a Linux desktop (§7.3) | B, the protected signer, with coding agents allowed on the same computer; the risk that remains on X11 is each owner's to accept |
| 2 | Packages (§7.2) | `.deb` and `.rpm`, updated from a repository Hermesh signs, and a `.tar.gz` for other distributions |

Open, with what is proposed:

| # | Decision | Proposed |
|---|---|---|
| 3 | Which desktops | GNOME first, Wayland and X11; Plasma second |
| 4 | Her from a server's terminal (§8) | After L5 |

## 11. Costs and risks

- **Wayland limits computer use** (§5): pointer gestures are not there. It is the compositor's rule, not ours to lift.
- **agent-desktop measures GNOME on Ubuntu 24.04.** Other desktops and older accessibility bridges may read less; the VM checklist says which.
- **The protected signer is new security code**, checked only on real desktops. Until L5 a Linux-only person approves from a second Linux user or computer, as today.
- **Two more Linux release files per architecture** (PDF about 6 MB, agent-desktop about 7 MB) and one more build (the glibc worker) in the release workflow, which cannot be tried outside GitHub.

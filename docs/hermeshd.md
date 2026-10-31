# hermeshd on a machine

`hermeshd` runs on each machine where coding agents work for your agents.
It runs in the background as the user it is installed for.
`hermeshd <command> --help` says more about each command.

## Install

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh
```

- It installs `hermeshd` and `hermesh` in `~/.local/bin` and runs
  `hermeshd pair`: scan the code it shows with Hermesh on your iPhone (or
  enter it in the app), then type the two digits your device shows.
- As root, it says it installs for root: press Enter, or type another
  user to install for (creating it if needed). From then on, run the
  commands as that user: `su - dev`.
- With a connection address after `sh -s --`, the request goes to that
  owner's devices by itself: compare the verification code there.
- On a first install, it shows the default data directory and available
  space. Press Enter to accept it, paste an absolute path (spaces and
  `~/` work without quoting), or type `q` to cancel. On Synology DSM,
  writable, mounted local storage volumes with at least 2 GiB free are
  also numbered choices; the one with the most free space is recommended
  unless the user's default data directory is already under `/volumeN`.
  Other Linux machines and macOS keep their ordinary platform default.
  A low-space location shows a warning and can still be accepted.
- Choosing another folder keeps a symbolic link at the default data
  directory, so later commands, the service and updates use that choice
  without extra arguments. Existing directories are kept on reinstall;
  this does not migrate an existing installation. If the link's target
  becomes unavailable, restore its disk before running Hermesh again.
- Without a terminal, it uses the recommended default and prints the
  location. `HERMESH_DATA_DIR` overrides the default for that environment;
  `--cli` installs only the agent CLI and skips daemon data selection.
  Colors are disabled for redirected output, `TERM=dumb` or `NO_COLOR`.
- Then it installs the background service. On Linux it is a systemd user
  service with lingering on, so it starts at boot with nobody logged in.
  Without systemd (a container, WSL) run `hermeshd run` yourself, as the
  container's command or with `nohup hermeshd run >/dev/null 2>&1 &`.

## Updates

Hermesh updates itself: every few hours it looks for a new release and,
once no session is running, installs it and restarts its service. It only
installs a release signed by the Hermesh release key, and never goes back
to an older version ([machine-updates.md](machine-updates.md)).

| Command | Does |
|---|---|
| `hermeshd update` | Updates now and restarts the service; asks first when that would stop a session (`--yes` does not ask) |
| `hermeshd update --check` | Says whether a newer version exists |
| `hermeshd update auto off`, `auto on` | Turns automatic updates off or on |

`hermesh update` does the same on a computer with only the agent CLI. The
install command, run again, also updates a paired machine.

### On Windows

In PowerShell, as yourself (no administrator):

```powershell
irm https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1 | iex
```

- It installs `hermeshd`, `hermeshdw` and `hermesh` in
  `%LOCALAPPDATA%\Programs\Hermesh\bin`, adds that folder to your PATH and
  runs `hermeshd pair`, as on Linux.
- With a connection address or a name:
  `& ([scriptblock]::Create((irm https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1))) <address> -Name build-box`;
  the agents' CLI only: the same with `-Cli`.
- The background service is a Task Scheduler task of yours that starts at
  logon. It runs `hermeshdw`, `hermeshd` without a console window, which
  starts the daemon again whenever it stops unexpectedly.
- The programs are not code-signed yet ([windows.md](windows.md) §6.4):
  the installer checks them against the signed checksum list.
- Coding agents are looked for where their installers put them (Claude
  Code's own installer, WinGet, npm). Worktrees need Git for Windows.

## Every day

| Command | Does |
|---|---|
| `hermeshd doctor` | Checks the pairing, the coding agents, the service and the relay, and says what to fix (exit 1 when something is wrong) |
| `hermeshd status` | The machine, whether it is connected, the service, updates and the projects |
| `hermeshd project add <folder>` | Lets agents work in a folder: a separate worktree per session, or `--in-place`; `--agents claude` limits it |
| `hermeshd project list`, `project remove <name>` | The projects; removing one leaves its folder |
| `hermeshd service status` | Whether the service runs and starts at boot, and where its log is |
| `hermeshd service restart` | Restart it |
| `hermeshd service install`, `service uninstall` | Start the service again; stop it for good (pairing and projects stay) |

The log is `~/.local/share/hermesh/logs/hermeshd.log` on Linux
(`tail -f` it), `~/Library/Application Support/Hermesh/logs/` on macOS,
`%LOCALAPPDATA%\Hermesh\logs\` on Windows.

**Coding agents**: install Claude Code or Codex as this user and sign in
once (run `claude` or `codex login`; on a server it prints a link to open
on any device). `hermeshd doctor`, or Check Again in the app, looks again.

## A trusted device instead

A computer, or a user, that runs no coding agents can approve like your
iPhone ([onboarding.md](onboarding.md) §9). Its key is sealed by a
passphrase. Install it with the same command, ending `sh -s -- --device`:
it asks whether Hermesh is on another device already, and joins it or sets
Hermesh up.

| Command | Does |
|---|---|
| `hermeshd device join` | Joins your Hermesh: scan the code it shows with Hermesh on your iPhone, then type the two digits |
| `hermeshd device setup` | The first device, when you have no other (signs in with a code a browser shows) |
| `hermeshd approve [code]` | Approves what waits: machines, agents, devices |
| `hermeshd device list`, `device remove <name>` | The trusted devices; removing a lost one re-issues what it signed |
| `hermeshd device leave` | This computer stops being one, and its key is deleted |

## Removing Hermesh

```bash
hermeshd service uninstall
rm -rf ~/.local/bin/hermeshd ~/.local/bin/hermesh ~/.local/share/hermesh
```

On Windows, in PowerShell:

```powershell
hermeshd service uninstall
Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\Hermesh", "$env:LOCALAPPDATA\Hermesh"
```

Then remove the machine in the Hermesh app, so it is no longer listed.

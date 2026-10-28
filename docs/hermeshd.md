# hermeshd on a machine

`hermeshd` runs on each machine where coding agents work for your agents.
It runs as the user you code as, never as root, in the background.
`hermeshd <command> --help` says more about each command.

## Install

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh
```

- It installs `hermeshd` and `hermesh` in `~/.local/bin` and runs
  `hermeshd pair`: scan the code it shows with Hermesh on your iPhone (or
  enter it in the app), then type the two digits your device shows.
- As root, it asks which user to install for (creating it if needed).
  From then on, run the commands as that user: `su - dev`.
- With a connection address after `sh -s --`, the request goes to that
  owner's devices by itself: compare the verification code there.
- Then it installs the background service. On Linux it is a systemd user
  service with lingering on, so it starts at boot with nobody logged in.
  Without systemd (a container, WSL) run `hermeshd run` yourself, as the
  container's command or with `nohup hermeshd run >/dev/null 2>&1 &`.

Updating is the same command again, then `hermeshd service restart`.

## Every day

| Command | Does |
|---|---|
| `hermeshd doctor` | Checks the pairing, the coding agents, the service and the relay, and says what to fix (exit 1 when something is wrong) |
| `hermeshd status` | The machine, whether it is connected, the service and the projects |
| `hermeshd project add <folder>` | Lets agents work in a folder: a separate worktree per session, or `--in-place`; `--agents claude` limits it |
| `hermeshd project list`, `project remove <name>` | The projects; removing one leaves its folder |
| `hermeshd service status` | Whether the service runs and starts at boot, and where its log is |
| `hermeshd service restart` | After an update |
| `hermeshd service install`, `service uninstall` | Start the service again; stop it for good (pairing and projects stay) |

The log is `~/.local/share/hermesh/logs/hermeshd.log` on Linux
(`tail -f` it), `~/Library/Application Support/Hermesh/logs/` on macOS.

**Coding agents**: install Claude Code or Codex as this user and sign in
once (run `claude` or `codex login`; on a server it prints a link to open
on any device). `hermeshd doctor`, or Check Again in the app, looks again.

## A trusted device instead

A computer, or a user, that runs no coding agents can approve like your
iPhone ([onboarding.md](onboarding.md) §9). Its key is sealed by a
passphrase.

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

Then remove the machine in the Hermesh app, so it is no longer listed.

# Quickstart

## Install

```bash
curl -fsSL https://github.com/hermeshai/her-releases/releases/latest/download/install.sh | sh
```

It checks the download's SHA-256 and puts `her` in `~/.local/bin`; `--dir` or `HER_INSTALL_DIR` picks another folder, `--version V` a version. Run it again to upgrade, or run `her upgrade`.

With Homebrew:

```bash
brew install hermeshai/tap/her
```

To check a download by hand, compare it with `SHA256SUMS` from the same release.

To uninstall, delete the program. `~/.her` holds your settings and sessions, and stays until you delete it.

## Connect a model

```bash
her login
```

It asks how you want to connect, checks with a real request, and asks which model new sessions start on. In a script, pipe the key:

```bash
printf %s "$KEY" | her login deepseek --key-stdin --model deepseek/deepseek-flash
```

See [models.md](models.md) for every provider.

## Your first task

<!-- doc-test: run -->
```bash
her exec "say hello"
```

Or start a conversation with `her`, or with its first message: `her "fix the failing test"`. Inside it:

| Key | What it does |
|---|---|
| Enter | Send. While a task runs, the message joins at its next step |
| Tab | With text, send it after the task. In an empty box, move what waits one step up |
| Alt+Enter, Ctrl+J | A new line |
| Up | History; with messages waiting, take the last one back |
| Ctrl+C | Stop the task; twice to quit |

`/help` lists the commands: `/model`, `/permissions`, `/rewind`, `/resume`, `/compact` and others. Anything else that starts with a slash goes to the model as it is.

## Look around

<!-- doc-test: run -->
```bash
her status
her doctor
her models
her tools
her sessions
```

Continue the last session here with `her resume`, or `her exec --resume last "NEXT"` in a script.

## Permissions

By default her asks before changing anything. Change that for one run with `--permission-mode accept-edits`, or keep it:

<!-- doc-test: run -->
```bash
her config set permission_mode accept-edits
her config unset permission_mode
```

See [permissions.md](permissions.md).

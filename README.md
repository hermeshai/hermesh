<p align="center">
  <a href="docs/assets/hermesh.mp4"><img src="docs/assets/cover.png" alt="Hermesh: let Grok Bot and Muse hand work to Claude Code on your computer. Grok Bot, Muse and ChatGPT reach a MacBook Pro, a Mac mini and a VPS through Hermesh." width="100%"></a>
</p>
<p align="center"><a href="docs/assets/hermesh.mp4">▶ Watch Hermesh in 50 seconds</a></p>

# Hermesh

Your personal agents are always on, keeping an eye on things for you. Hermesh connects them to Claude Code, Codex and other coding agents on your own computers (a MacBook at home, a Mac mini at work, a VPS), so coding work gets done on your machine, with your code.

You approve every agent on your Mac with Touch ID, choose which projects it may use, and can cut it off at any moment. Hermesh is open source, and you can run the server yourself.

## Ask once, and it hands out the work itself

> *Yesterday*
>
> **You**: From now on, when someone reports a bug in the repo, hand it to Claude Code on my Mac mini to fix.
>
> **Grok Bot**: Got it. I'll keep an eye on it.
>
> *Today, 10:02: new issue #142 on GitHub, "Nothing happens on Sign in"*
>
> **Grok Bot**: Someone reported #142. I've handed it to Claude Code on your Mac mini.
>
> *On the Mac mini, Claude Code reads the issue, finds the cause, fixes it, runs the tests and opens PR #143.*
>
> *10:09*
>
> **Grok Bot**: Fixed: the email wasn't checked before submitting. Here's PR #143.

The same way, Muse can watch your pull requests and have Codex review every one that touches checkout. Your agent starts a session on your machine, follows it, answers the coding agent's questions or passes them to you, and brings back the result and the diff.

## Get started

1. **Install Hermesh on your Mac.** Download it for [Apple silicon](https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh-macos-arm64.dmg) or [Intel](https://github.com/hermeshai/hermesh/releases/latest/download/Hermesh-macos-x64.dmg), drag it to Applications (it keeps itself up to date), open it and click "Get Started". You sign in at [hermesh.io](https://hermesh.io) in the browser (an email code, Google or GitHub; the first sign-in creates your account) and come back to the app, which makes this Mac your trusted device and its first machine. Add the folders your agents may work in as projects.

2. **Send your agent one line.** Choose "Connect Agent…" in the app and send your agent what it gives you, like `Connect to Hermesh: follow https://hermesh.io/connect`. That page tells the agent what to do:
   - **An agent that runs on a computer** (Claude Code, Codex, Cursor, your own agent) installs the `hermesh` CLI and signs in:
     ```bash
     curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
     hermesh auth login --name "Codex on MacBook"
     ```
     It gives you a link (`https://hermesh.io/p/XXXX-XXXX-XXXX`) to open, signed in.
   - **An online assistant** (Grok Bot, Muse, ChatGPT, Claude.ai) adds the MCP server `https://mcp.hermesh.io/mcp` and sends you to sign in.

3. **Approve it on your Mac.** A window asks whether to let it in: check that the verification code matches what the agent shows, choose the projects it may use, and press Touch ID.

**More machines** (optional): copy the add-machine command from the app and run it on the machine (macOS or Linux), then approve it on your Mac the same way:

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- connect.hermesh.io/o_xxxxxxxx
```

Without a connection address (`install.sh` with no argument) the machine still reaches the official center, but nobody yet knows whose it is: choose "Enter Pairing Code…" in the Mac app and type the code it shows. On each machine, sign in to `claude` / `codex` once under the same user first; `hermeshd doctor` checks this.

Machines and agents are added only on your own devices. The web console at [hermesh.io](https://hermesh.io) can show them, revoke them, and send an agent's request to your Mac, but it can't approve anything.

## Works with

**Agents that hand out the work**: online assistants that speak MCP (Grok Bot, Muse, ChatGPT, Claude.ai, Cursor and others), and any agent that can run a command line, through the `hermesh` CLI.

**Agents that do the work**, on your machines:

| `--agent` | Tool | Integration |
|---|---|---|
| `claude` | Claude Code | `claude -p` stream-json |
| `codex` | Codex | `codex app-server` |
| `gemini` | Gemini CLI | `gemini --acp` |
| `cursor` | Cursor | `cursor-agent acp` |
| `copilot` | GitHub Copilot CLI | `copilot --acp` |
| `opencode` | OpenCode | `opencode acp` |
| `qwen` | Qwen Code | `qwen --acp` |
| `goose` | Goose | `goose acp` |
| `auggie` | Augment | `auggie --acp` |
| `kimi` / `kiro` / `qoder` / `grok` | Kimi CLI / Kiro CLI / Qoder CLI / Grok | Each one's own ACP entry point |
| `deepseek` | DeepSeek Harness | `dsh --profile acp`, or the copy inside the DSH Desktop app |
| `codebuddy` | CodeBuddy Code | `codebuddy --acp` |

All but the first two use the [Agent Client Protocol](https://agentclientprotocol.com): one long-running process with multi-turn conversation, interruption and resumption, and every tool call and permission request seen by Hermesh. Whichever is installed on a machine (and allowed by the grant) shows up in `hermesh project list`. Their sign-in can't be checked in advance, so run each CLI once under the same user first.

## Security

**Encryption you can see.** Everything between an agent and your computer is encrypted on the way. What differs is who in the middle can open it:

| How the agent connects | Who can open the content |
|---|---|
| **Directly**, through the `hermesh` CLI | Only the two ends. The center in the middle (hermesh.io, or your own relay) only passes on encrypted bytes: TLS 1.3, end to end. |
| **Over MCP** (online assistants) | The two ends, and the MCP server: it holds that connection's key and acts for the assistant within its grant. On hermesh.io we run it ([Privacy Policy](https://hermesh.io/en/privacy)); [run your own relay](#run-your-own-relay) with `--mcp` and the key stays on your server. |

**You hold the authority.** Your root key lives in your Mac's Secure Enclave. Adding a machine, an agent or a device, and changing what an agent may do, take your Touch ID; the console and the center can't sign for you, so they can't forge a command or a grant. Every time something connects, your Mac and the other side show the same verification code: approve only when they match. Each agent gets its own grant (which machines and projects, which coding agents, for how long), and you can revoke it at once.

On its first pairing with the official center, an agent takes the owner your account belongs to on the center's word; from then on it is pinned to it. Your machines never rely on that: they only accept grants signed by your root key. If you'd rather not trust the center even once, run your own relay and pair with your connection address.

**A grant is full trust.** A coding agent can do anything on that machine that you can (read credentials, use the network, change any file, push code). Hermesh adds no sandbox or permission prompts; it only holds the agent to the machines, projects, coding agents and time its grant allows. Grant access only to agents you trust: if one reads untrusted web pages or email, a prompt injection reaches as far as the grant does.

- Every Mac you add as a trusted device can approve anything. Remove a lost one from another of your devices; if none is left, start over and pair your machines and agents again.
- **Worktree projects**: a session works in its own git worktree on branch `hermesh/<session>`, never touches your working directory and never pushes for you. Review the diff before merging.
- **In-place projects**: the agent writes straight into the directory, with no copy to throw away. Choose this only for a directory you're willing to let the agent change.

The full design is in [docs/technical-design.md](docs/technical-design.md); MCP's in [docs/mcp-gateway.md](docs/mcp-gateway.md).

## Run your own relay

Instead of hermesh.io, run `hermesh-relay` on your own server (with a domain name pointing at it):

```bash
docker run -d --name hermesh-relay -p 443:443 -p 80:80 -v hermesh-relay:/data \
  ghcr.io/hermeshai/hermesh-relay:latest serve --data-dir /data --domain relay.example.com --mcp
docker logs hermesh-relay   # note the claim code
```

`--mcp` also serves MCP at `https://relay.example.com/mcp` for online assistants, with their keys on your server; leave it out if you only connect agents directly. Don't run the relay under the same system user as a machine on the same server.

In the Mac app, choose "Use Your Own Server…" on the welcome page and enter the relay's address and claim code. From then on, machines and agents use the connection address the app gives you:

```bash
curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- relay.example.com/o_xxxxxxxx
hermesh auth login relay.example.com/o_xxxxxxxx
```

`hermesh auth status` checks pairing step by step (the local identity, the grant, the relay connection, whether the grant is current) and gives the next command when something is wrong. If you were revoked or the grant expired, run `hermesh auth login` again: it applies afresh, no arguments needed. To switch owners, run `hermesh auth logout` first.

## For agents: the `hermesh` CLI

Commands are "noun + action": `auth`, `project`, `session` and `message`, each with its actions (`hermesh <noun> --help`). The agent calling the CLI is the **client**; the agent doing the work on the machine is the **coding agent** (`--agent claude`). Over MCP, the same commands are tools (`project_list`, `session_start`, …; [docs/mcp-gateway.md](docs/mcp-gateway.md) §7).

```bash
hermesh project list                # where you can work: machine/project, the owner's notes, branch, platform, usable coding agents, whether a session can start now
hermesh session start --project vps-1/demo --agent claude --wait --prompt "Fix the bug on the login page"   # demo alone is enough if only one machine has it
hermesh session start --project demo --title "Login page bug" --prompt "..."   # a title for the app and session list; default: the prompt's first line
hermesh message send --to s_xxx --wait --message "Add one more test"   # the one way to tell a session anything; while it runs, the message joins your turn
hermesh message send --to s_xxx --wait -f screenshot.png -f spec.pdf --message "Change it to match these"   # attach files
hermesh session answer s_xxx --wait --choose 2   # answer the agent's question (also --text, --decline, or just write to the session)
hermesh session diff s_xxx
hermesh session list                # your sessions (--archived for archived ones)
hermesh session interrupt s_xxx     # stop the running turn; the session stays, and a later message continues it
hermesh session archive s_xxx       # when done: read-only, frees the worktree (the branch keeps the changes); unarchive restores it
hermesh session events s_xxx --after 812   # events page by page: the latest 50 by default; --after newer, --before older
hermesh message list --wait         # your inbox: news of your sessions, messages from the owner's other agents
hermesh message ack msg_xxx         # handled: it leaves the inbox
hermesh message send --to "Grok Bot" --message "PR #143 is ready"   # to another of the owner's agents (message contacts lists them)
```

**Messages to a session.** `message send --to <session>` goes straight to its machine when it's online. If you asked for the turn running now, it joins it (Claude Code and Codex take it at the next tool boundary) and answers its question; otherwise it starts a turn of its own, or waits for one, so one agent's words never go into another's turn. Any agent whose grant covers the session and that may see it can write to it; only the agent that started it can interrupt or archive it. While the machine is offline the message waits for it (files need it online). `session send` still works for now, as an alias.

**Your inbox.** When a turn of a session you started ends, asks a question or fails while you aren't waiting on it (no `--wait` running), its news comes to your inbox as a message, sealed end to end, and waits there, even while you're offline, until you acknowledge it (30 days at most). Every command's JSON says `inbox.waiting` while messages wait. `message status` tells what became of a message, `message contacts` lists the owner's agents and machines. See [docs/agent-messages.md](docs/agent-messages.md).

**Claude Code and Codex on your computer.** `hermesh message hook claude --install` (or `codex`) adds Hermesh's hooks to the agent's user settings. Each conversation then gets the messages addressed to it, such as news of the sessions it started and replies to what it wrote: at the start of its turns and when a turn would end, and in Claude Code also between tool calls and while idle (a new turn). Messages addressed to no conversation stay in the inbox; the hooks only say how many wait. In Claude Code the conversation's commands know its key (`HERMESH_CONVERSATION`); from Codex, add `--from-session <key>` to `session start` and `message send`.

**Sessions never end on their own.** However long one has been idle, another message continues the same coding agent session.

**Questions mid-turn.** Claude Code (AskUserQuestion), Codex and ACP agents with form questions can ask in the middle of a turn. The session goes to `waiting_input`; `--wait` returns at once with exit code 8 and prints the question and options. Answer with `session answer` (`--choose` a label or number, `--text`, or `--decline`), with `session send` (the message is the answer), or on the session page in the app. Unanswered, a question expires after 30 minutes by default and the agent is told to use its own judgment and state its assumptions; `session start --question-timeout 0` means nobody will answer in this session. Answers to secret questions go only to the agent and aren't recorded in events.

**Projects** are added by the owner in the app. Any directory can be one, with a worktree switch: **separate worktree** (the default for a git repository root: each session on its own branch `hermesh/<session>`, a snapshot every turn, changes shown by `session diff`) or **in place** (the only option outside git: the agent reads and writes the directory directly, as if you ran it there yourself; no branch, no commits, no diff, and concurrent sessions coordinate among themselves).

**Files.** `-f/--file` can be repeated, up to 20 files and 32 MiB per message. They're saved in the session's attachments directory on the machine (outside the worktree, so never in the diff), and the message lists their paths; PNG, JPEG, GIF and WebP images up to 3.5 MiB also go to the agent as images.

**Waiting.** `session start`, `send` and `answer` return once the message is delivered (stderr suggests `hermesh session get <id>`; after the turn, the session's `result` is the agent's full reply). With `--wait` they wait for the turn to end and the exit code says how it went; from a tool with a time limit, add a shorter `--timeout` (exit code 6; the turn keeps going, follow up with `get`). The session ID is always the first line on stderr, so it's known even if the process is killed midway. The message comes from `--prompt` / `--message` or a file (`--prompt-file`, `--message-file`; `-` for stdin); stdin is never read unless asked for.

**Output.** Text for people by default; with `-o json` (or `HERMESH_OUTPUT=json`) stdout is one JSON document with the same outer shape for every command: `ok`, `exit_code`, `error` (`{kind, message, hint, retryable}` on failure, null on success), and data under named keys (`session`, `sessions`, `projects`, `events`). An offline machine isn't an error: lists come back with it under `offline`; a command fails with exit code 4 only when the machine it needs is offline. See [docs/cli-output.md](docs/cli-output.md).

**Exit codes.** 0 success; 1 internal error; 2 bad arguments, not found, or not allowed now (like sending to an archived session); 3 not paired, revoked or not authorized; 4 relay or the needed machine offline or unreachable (retryable); 5 session failed; 6 timeout; 7 transient failure (rate limit, network, server error, agent crashed or stuck: send again); 8 the agent is waiting for an answer. A session's latest failure is in `session.last_failure` (`kind`, whether it's retryable, a `hint`); a session that crashed, got stuck or failed to start is `crashed`, and the next message continues with its context.

## Components

| Component | Role |
|---|---|
| `Hermesh.app` | The Mac app ([docs/desktop-app.md](docs/desktop-app.md), Electron): approves, shows your machines and sessions; bundles `hermeshd` and a native signer that holds the Secure Enclave root key |
| `hermeshd` | The service on each machine (macOS, Linux) that runs coding agent sessions |
| `hermesh` | The command line for agents |
| `hermesh-relay` | The self-hosted server: pairing, admission, routing, revocation, and with `--mcp` the MCP gateway |

## Development

```bash
make generate   # Go, Swift and TypeScript code from proto
make lint test  # static checks and unit tests
make test-app   # the desktop app's types and unit tests, and the signer's
make e2e        # full user flows and malicious-relay tests with real binaries and scripted agents
make e2e-real   # the same flows with the real Claude Code and Codex on this machine (uses quota)
HERMESH_DOCKER=1 go test ./e2e -run Linux   # a Linux machine (Docker)
make app        # build and sign dist/Hermesh.app (see below for a release build)
pnpm --dir apps/desktop dev   # the desktop app against the local hermeshd (HERMESH_SOCKET to choose one)
```

A release build of the desktop app needs, besides the Developer ID certificate in the keychain, the signer's Developer ID provisioning profile (App ID `com.hermesh.app.signer`, so the root key stays in the signer's Keychain group) and an App Store Connect API key to notarize:

```bash
HERMESH_SIGNER_PROFILE=~/.config/hermesh/signer.provisionprofile \
HERMESH_NOTARY_KEY_PATH=~/.appstoreconnect/private_keys/AuthKey_<key id>.p8 \
HERMESH_NOTARY_KEY_ID=<key id> HERMESH_NOTARY_ISSUER=<issuer id> \
make app        # HERMESH_ARCH=arm64 for an Apple silicon build only
```

The release workflow reads the same from the secrets `MACOS_SIGNER_PROFILE`, `APPLE_API_KEY_P8`, `APPLE_API_KEY_ID` and `APPLE_API_ISSUER_ID`.

Releasing: first run `scripts/release-keygen.sh` to generate the release signing key (the private key goes in the GitHub secret `RELEASE_SIGNING_KEY`; commit the public key), then push a `v*` tag. Each release carries `Hermesh-macos-<arch>.dmg` for installation, and `Hermesh-macos-<arch>.zip` with `RELEASES-darwin-<arch>.json` for installed apps to update themselves (`arm64` and `x64`; [docs/desktop-app.md](docs/desktop-app.md) §6.5–6.7). The app and DMG are Developer ID–signed, notarized and stapled. A whole update, with two signed builds made here and served locally, is checked by `HERMESH_UPDATE_E2E=1 go test ./e2e -run TestDesktopUpdate -v -timeout 30m`.

## License

Apache License 2.0, see [LICENSE](LICENSE).

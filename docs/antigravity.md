# Google Antigravity through official ACP

Hermesh runs Google's **standalone ACP server**, not the Antigravity desktop
app, the `agy` terminal UI, or a third-party model proxy. Its agent ID is
`antigravity`. Google distributes the server and its companion
`localharness_external` together in the
[ACP registry](https://github.com/agentclientprotocol/registry/blob/main/antigravity-acp/agent.json).
Keep both files in the same directory.

## Install on the machine that runs the work

Download the archive for your platform from the registry. Check its SHA-256
against that entry before extracting it. Hermesh finds the server on the
login shell's PATH, or under `~/.local/share/antigravity-acp/` (the same
directory relative to the user's home on Windows).

| Platform | Registry target | Executable |
| --- | --- | --- |
| Apple silicon | `darwin-aarch64` | `agy_acp_server.par` |
| Intel Mac | `darwin-x86_64` | `agy_acp_server.par` |
| Linux ARM64 | `linux-aarch64` | `agy_acp_server.par` |
| Linux x86-64 | `linux-x86_64` | `agy_acp_server.par` |
| Windows ARM64 | `windows-aarch64` | `agy_acp_server.exe` |
| Windows x86-64 | `windows-x86_64` | `agy_acp_server.exe` |

For example, the **Apple silicon 1.4.5** build tested on 2026-11-01:

```sh
mkdir -p "$HOME/.local/share/antigravity-acp"
archive=$(mktemp -t antigravity-acp)
curl -fL https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.4.5-darwin-arm64.zip -o "$archive" &&
printf '%s  %s\n' 886fff7767d08994ca39eb02c3d01a7532c7325a94aa33aaf52d52068733284f "$archive" | shasum -a 256 -c - &&
  unzip -o "$archive" -d "$HOME/.local/share/antigravity-acp" &&
chmod +x "$HOME/.local/share/antigravity-acp/agy_acp_server.par" "$HOME/.local/share/antigravity-acp/localharness_external"
rm "$archive"
```

Other platforms must use their own archive and checksum, not this example.
On Linux, Hermesh adds the registry's `--uid=` argument. On Windows, extract
the whole ZIP, including companion executables and libraries. Do not copy
just the server into PATH. Installation and updates are explicit: Hermesh
does not download or silently update Google's runtime.

## Sign in

Run as the same OS user that runs Hermesh's machine:

```sh
hermesh agent login antigravity
```

Open the Google login link printed in the terminal and finish authorization.
The desktop app's Antigravity page also offers **Run in Terminal** when no
authentication method is configured. The iPhone shows the command to run
on the machine. The login command does not require Hermesh pairing and does
not create a model conversation. Ctrl+C stops the login process.
The packaged macOS app runs its bundled CLI for this button, so a separate
Hermesh CLI installation is not required.

The official server saves authentication itself. Hermesh does not import
desktop credentials or handle OAuth tokens. In our 1.4.5 test, the desktop
app's existing login did not replace this initial ACP authorization. A new
ACP process subsequently reused and refreshed its own login successfully.

Personal Google login is the default. To select another method advertised
by the server:

```sh
hermesh agent login antigravity --method oauth-business
hermesh agent login antigravity --method gemini-api-key
hermesh agent login antigravity --method agent-platform
```

These methods require Google's corresponding account, API-key environment,
or Cloud configuration; follow the
[Google authentication documentation](https://antigravity.google/docs/install).
Hermesh validates that the server offers the requested method before calling
`authenticate`. A configured business/API-key method is preserved during
normal sessions; Hermesh does not automatically switch it to personal OAuth.

The server uses `$GEMINI_HOME/antigravity-acp/settings.json`, defaulting to
`~/.gemini/antigravity-acp/settings.json`. Discovery checks whether an auth
method has been selected; credentials are validated by the server when a
session starts. An expired/revoked login can therefore still require signing
in again. Session authentication errors include the login command.

If installing a new macOS build still leaves Antigravity under “not installed,”
check the **machine's Hermesh version**, not just the app version. A signed
installation's registered LaunchAgent rejects an ad-hoc replacement (`launchctl`
reports exit 78), leaving the UI with its previous machine snapshot. Build the
replacement with the same Developer ID team and the signer's provisioning
profile as described in [Development](development.md#releases), then verify
that the running machine reports the new version before rescanning. The macOS
build records its version in the LaunchAgent environment so ServiceManagement
re-evaluates cached launch constraints after a replacement. The agent
icon is dimmed while it is in the not-installed list.

## Use through Hermesh

Allow `antigravity` in the project's agents and in the calling client's
grant, or use an existing grant that allows all coding agents. Refresh the
machine's agent list after installation/login.

```sh
hermesh project list
hermesh session start --project demo --agent antigravity --wait \
  --prompt "Create a README describing this project"
hermesh session send <session-id> --wait --message "Add a usage example"
hermesh session interrupt <session-id>
```

`--model` accepts an ID offered by the official server. In the tested account
these included `gemini-3.8-flash` and `gemini-4-argon`; availability depends on
Google and the account. Omitting it uses the server's default. Model IDs are
validated against the session's ACP config options, not a hard-coded list.

Each session uses a long-running stdio ACP process. Hermesh handles streamed
replies, tool events, token usage, permission requests, cancellation and
resumption. The server's **default** permission mode is selected explicitly
on both new and resumed sessions; Hermesh never chooses auto-edit/YOLO or
persistent `allow_always` permissions. Commands are extracted from Google's
`CommandLine`/`command_line` fields, and terminal output from `combinedOutput`.
ACP does not support steering a running turn in this adapter; Hermesh queues
the next message until the turn ends.

## Verification and limits

The opt-in tests use real Google requests and consume account quota:

```sh
# Official server through the real relay, daemon and hermesh CLI:
HERMESH_REAL_CLI=1 go test ./e2e -run '^TestAntigravityACP$/real$' -count=1 -v -timeout=10m

# Denied edits, cancellation, streamed replies, attachments and process resume:
HERMESH_REAL_CLI=1 go test ./internal/provider/providertest \
  -run '^(TestAntigravityPermissions|TestTwoTurns|TestDraftsAddUpToMessages|TestAttachedFiles)$/^antigravity$' \
  -count=1 -v -timeout=10m
```

Scripted ACP tests cover registration, auth-state discovery, login cancellation,
permission mode, errors and the relay flow without Google access. The desktop
onboarding test covers the Antigravity row and login command.
The iPhone `testAntigravitySetup` UI test covers joining a real test relay,
opening the machine's Antigravity page and displaying the login command.
On 2026-11-01, the real-agent tests above, desktop onboarding and iPhone UI
test all passed on Apple silicon with official server 1.4.5. Linux and Windows
runtime behavior has not been exercised on this machine.

Google ACP 1.4.5 can emit an edit under a second tool ID, then report its
original approval ID as `failed` with “Tool call was approved but never
executed,” even though the edit completed. Hermesh preserves that upstream
tool result rather than hiding errors; inspect the completed edit and the
session diff. A successful turn and verified file contents remain distinct
from that individual tool event. Desktop-app private RPCs are not used as a
fallback.

# S0 technical validation results (2026-10-16)

Environment: macOS 27.0.1 arm64; Claude Code 2.1.292; Codex CLI 0.162.0-alpha.17.2 (distributed with ChatGPT.app, at `/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex`, logged in with a ChatGPT account); Go 1.27.1; Xcode 27.0.

## Claude Code

| Question | Result |
|---|---|
| Does `--dangerously-skip-permissions` under `-p` need interactive confirmation on first use | **No**. `permissionMode` in `system/init` is `bypassPermissions` |
| Does the PreToolUse hook fire in fully unrestricted mode | **Yes**, it can be used to record audit and progress |
| In sandbox mode, can the hook intercept every call, and can it allow them | Yes (see appendix A of the technical design) |
| Does the sandbox take effect under `-p` | Yes: writes to `$HOME` are denied, network access is denied |

## Codex app-server

| Question | Result |
|---|---|
| Protocol | stdio JSONL; requests are `{id, method, params}` with no `jsonrpc` field; `initialize{clientInfo{name,version}}` → `initialized` notification |
| Session | `thread/start{cwd, sandbox, approvalPolicy, ephemeral}` returns `result.thread.id`; `turn/start{threadId, input:[{type:"text",text}]}` |
| Events | `item/started`, `item/completed` (item.type includes userMessage / agentMessage / commandExecution / fileChange, etc.), `item/agentMessage/delta`, `thread/tokenUsage/updated`, `turn/completed{turn{id, items}}` |
| sandbox values | `read-only` / `workspace-write` / `danger-full-access`; approvalPolicy takes `untrusted` / `on-request` / `never` |
| `workspace-write` + `never` | Writes inside the worktree succeed; writing to `$HOME` reports Operation not permitted; network access fails (cannot resolve domain names) |
| `danger-full-access` + `never` | Writing to `$HOME` and network access both succeed |
| Interrupt and resume | `turn/interrupt{threadId, turnId}`; `thread/resume{threadId, cwd, sandbox, approvalPolicy}` |
| Login | Reuses the login state in `~/.codex` directly; `initialize` returns `codexHome` |
| Can `-c mcp_servers={}` turn off user-configured MCP servers | **No** (there are still 8 `mcpServer/startupStatus/updated`). Under the sandbox preset, use a separate CODEX_HOME (linking auth.json) instead, to be verified in S6 |

## Impact of the results on the design

- Section 6.7: under the fully unrestricted preset, doctor doesn't need to guide the user through confirming a disclaimer.
- Section 6.8: MCP isolation under the sandbox preset changes to "separate CODEX_HOME + linked login credentials", to be verified when S6 is implemented.

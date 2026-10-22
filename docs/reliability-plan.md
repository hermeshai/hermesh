# Reliability and failure classification plan (2026-10-16, implemented)

This plan fills in three pieces of Hermesh infrastructure: **failure classification**, **sessions that can continue after a failure**, and **stall detection**, plus robustness fixes for processes and pipes.

Every "current state" in this document has been checked against the current code, and verified against the schemas of the real Claude Code 2.1.292 and Codex app-server on this machine.

---

## 0. Current problems

| # | Problem | Location | Consequence |
|---|---|---|---|
| P1 | There is only one `NoEventTimeout` (15 minutes). Claude emits no events at all while running a long command | `internal/machine/sessions.go` Janitor | If `npm test` or `docker build` runs longer than 15 minutes, the session is judged FAILED |
| P2 | If the process exits midway, stalls, or a single turn times out, the session becomes FAILED (terminal state), and `Send` is rejected | `pump`, Janitor, `fail` | One network blip or CLI crash wrecks the whole conversation; the only option is a new session |
| P3 | A failure carries only `is_error` and a piece of text | `TurnCompleted`, `Session.error` | The caller cannot tell whether to retry, wait, switch models, or have the owner log in |
| P4 | Whenever a start with resume fails, whatever the reason, it silently switches to a new session | `prepare` in `sessions.go` | Expired logins and network problems also lose the context, and nobody knows the context was lost |
| P5 | When Claude's resume is rejected, **the process starts normally**, and the rejection reason is in the result's `errors[]` (measured: `subtype=error_during_execution`, `errors=["No conversation found with session ID: …"]`). The adapter only reads the `result` field | `internal/provider/claude/claude.go` | It reports a failed turn with no reason, the dead session pointer is kept, and every later message fails the same way |
| P6 | Claude returns "success" but with `terminal_reason=prompt_too_long`, and we treat it as a normal completion | Same as above | A full context is treated as success, and every later resume fails |
| P7 | When the first event is slow to arrive, we also wait the full 15 minutes | Janitor | If the agent hangs after starting (handshake, login prompt, network), it takes a long time to notice |
| P8 | Claude writes init and the prompt synchronously **before** starting the read goroutine | `Start` in `claude.go` | With a large prompt, while the CLI first writes a large chunk to stdout, both sides block |
| P9 | When a single stdout line exceeds 32 MiB, the scanner errors and the reader exits, but the process is still alive | `Lines` in `provider/proc.go` | The process is stuck writing stdout, and is only reclaimed by the Janitor after 15 minutes |
| P10 | After the process exits, we wait to read stderr to the end. If a grandchild process inherited stderr, the end never comes | `startSpawn` in `proc.go`, the `exec` fallback path | `Done()` never closes, and the session hangs forever |
| P11 | The last 5 lines of stderr are pasted verbatim into the error message | `wait` in `proc.go` | Sensitive content such as tokens may be sent to the cloud agent |
| P12 | The CLI's `waitTurn` doesn't treat INTERRUPTED as a finished state | `internal/cli/session.go` | If the daemon restarts while waiting, the CLI waits until it times out |

---

## 1. Failure classification (Failure)

### 1.1 Classification table

Add a `FailureKind` enum to the proto. Each kind carries three attributes: **whether it can be retried**, **whether the session can continue**, and **advice for the caller**.

| kind | Meaning | Typical sources | Retryable | Session can continue | Advice (`hint`) |
|---|---|---|---|---|---|
| `AUTH` | Not logged in, credentials invalid | 401/403, "not logged in", Codex `unauthorized`, ACP `-32000` | No | Yes (after login) | Ask the owner to run the CLI once on that machine as the same user to log in |
| `QUOTA` | Quota or billing exhausted | 402, "usage limit", Codex `usageLimitExceeded` | No (not soon) | Yes | Try again once the quota is restored, or switch agent/model |
| `RATE_LIMIT` | Rate limited, insufficient capacity | 429/529, "overloaded", Codex `rateLimitExceeded`/`serverOverloaded` | Yes (with backoff) | Yes | Retry later |
| `PROVIDER_ERROR` | Model service 5xx | 5xx, Codex `internalServerError` | Yes (with backoff) | Yes | Retry later |
| `NETWORK` | Connection dropped, DNS, stream interrupted | Codex `httpConnectionFailed`/`responseStream*`, "connection closed", "dial tcp" | Yes | Yes | Retry |
| `CONTEXT_OVERFLOW` | Context window exhausted | Claude `terminal_reason=prompt_too_long`, Codex `contextWindowExceeded`, "prompt is too long" | No (as is) | Yes, with a fresh context | Sending another message continues in a new provider session (see §2.4) |
| `MODEL_UNAVAILABLE` | Model doesn't exist or no access | Claude `api_error_status=404`, plus stderr `[claude-code:unrecognized_model]`; ACP doesn't offer the model | No | No (the model is fixed when the session opens) | Open a new session with a different model |
| `INVALID_REQUEST` | Other 400s | 400, Codex `badRequest` | No | Yes | Change the request |
| `REFUSED` | Refused by the model or policy | ACP `stopReason=refusal`, Codex `cyberPolicy` | No | Yes | Describe the task differently |
| `LIMIT_REACHED` | Per-turn step or token limit | Claude `error_max_turns`, ACP `max_tokens`/`max_turn_requests` | No | Yes | Send "continue" to have it keep going |
| `RESUME_REJECTED` | The provider session no longer exists | Claude "No conversation found", ACP load/resume returns not found | — | The kernel switches to a new session automatically (see §2.3) | — |
| `AGENT_CRASHED` | Process exited unexpectedly, output over the limit | Process exits midway through the turn, scanner error | Yes | Yes (automatic resume) | Retry; if it happens repeatedly, check the CLI on the machine |
| `STALLED` | Watchdog judged no progress | §3 | Yes | Yes (automatic resume) | Retry |
| `TIMEOUT` | Turn exceeded the total duration limit | §3 | Depends | Yes | — |
| `START_FAILED` | Not installed, not executable, handshake failed | Start returns an error | No | Depends | `hermeshd doctor` |
| `UNKNOWN` | Unrecognized | — | No | Yes | Look at `message` |

Cancellation and interruption by the user are not failures; their states stay as they are now.

### 1.2 Proto changes (additions only, old clients are unaffected)

```proto
enum FailureKind {
  FAILURE_KIND_UNSPECIFIED = 0;
  FAILURE_KIND_AUTH = 1;
  FAILURE_KIND_QUOTA = 2;
  FAILURE_KIND_RATE_LIMIT = 3;
  FAILURE_KIND_PROVIDER_ERROR = 4;
  FAILURE_KIND_NETWORK = 5;
  FAILURE_KIND_CONTEXT_OVERFLOW = 6;
  FAILURE_KIND_MODEL_UNAVAILABLE = 7;
  FAILURE_KIND_INVALID_REQUEST = 8;
  FAILURE_KIND_REFUSED = 9;
  FAILURE_KIND_LIMIT_REACHED = 10;
  FAILURE_KIND_RESUME_REJECTED = 11;
  FAILURE_KIND_AGENT_CRASHED = 12;
  FAILURE_KIND_STALLED = 13;
  FAILURE_KIND_TIMEOUT = 14;
  FAILURE_KIND_START_FAILED = 15;
  FAILURE_KIND_UNKNOWN = 16;
}

message Failure {
  FailureKind kind = 1;
  string message = 2;          // redacted, truncated (≤ 2 KiB)
  bool retryable = 3;
  uint32 retry_after_seconds = 4; // filled in when known (e.g. from a rate limit header)
  string hint = 5;             // one-line advice for the caller
  uint32 http_status = 6;      // filled in when known
}

message TurnCompleted   { ... Failure failure = 7; }      // is_error is kept
message ErrorEvent      { ... Failure failure = 2; }
message SessionProgress { ... Failure last_failure = 8; } // Session.error is kept; stored in progress, persisted with the row, no migration needed
```

### 1.3 Classification in three layers, structured information first

1. **Structured mapping in the adapters** (most accurate).
   - **Claude**: in the result, `api_error_status` (measured as 404 for a bad model), `terminal_reason` (`api_error`/`prompt_too_long`/…), `subtype` (`error_during_execution`/`error_max_turns`), and `errors[]`. None of these fields are parsed today; they need to be added.
   - **Codex**: `turn.error.codexErrorInfo`. In the local schema the enum values include `contextWindowExceeded`, `usageLimitExceeded`, `rateLimitExceeded`, `serverOverloaded`, `unauthorized`, `badRequest`, `internalServerError`, `cyberPolicy`, `sandboxError`, as well as `httpConnectionFailed{httpStatusCode}`, `responseStreamDisconnected{…}`, `responseTooManyFailedAttempts{…}`. Also, `willRetry=true` in an `error` notification is not a failure (the current code already handles it this way).
   - **ACP**: the JSON-RPC error code (`-32000` means login required), `stopReason` (`refusal`, `max_tokens`, `max_turn_requests`, `cancelled`), and errors during load/resume.
2. **Text classifier** `provider.Classify(text) FailureKind`: as a fallback, it matches against the message plus the stderr tail. Rules are ordered from specific to generic:
   - First check for the concurrent request limit.
   - Then 403 plus usage limit.
   - Context overflow must come before quota, otherwise "token limit" gets grabbed by "limit".
   - Then, in order: auth, quota, capacity, 5xx, network, model not found, exec errors, process exit.
   - HTTP status codes are matched on **numeric boundaries** (`(^|[^0-9])429([^0-9]|$)`), to avoid misclassifying things like "402913 tokens".
3. **Assigned by the kernel**: `STALLED`, `TIMEOUT`, `AGENT_CRASHED` and `START_FAILED` are decided by the session kernel from facts, not from text.

**Code organization**:
- Add `internal/provider/failure.go`, containing the `Failure` struct, `Classify`, and the default attributes of each kind.
- Add a `Failure *Failure` field to `provider.Event`, used for `TurnCompleted` and `Error` events.
- Errors returned by `Start` can be wrapped in `*provider.FailureError`, carrying the kind.

### 1.4 Redaction (P11)

Before `Failure.message` and the stderr tail leave the machine, two things are always done:
- **Redaction**: `sk-…`, `Bearer …`, `api[_-]?key=…`, `token=…`, user:pass in URLs, and runs of 40 or more base64/hex characters.
- **Truncation**: keep the last 2 KiB.
- The local audit log keeps the original text.

### 1.5 CLI and app

- **JSON output**: `-o json` outputs the `failure` object. With text output, the `hint` is printed to stderr.
- **Exit codes**: **add 7** to the existing ones, meaning "transient failure, can be retried later" (kinds with `retryable=true`). 5 stays "session failed". Update the exit code description in the README accordingly.
- **`waitTurn`**: treat INTERRUPTED as a finished state (P12).
- **App**: the session list and details show the failure category and advice in Chinese.

---

## 2. Session state machine: continuing after a failure

### 2.1 Principle

**There is only one criterion: whether the provider session can be resumed, not "this turn failed".**

As long as we hold a provider session pointer (`ProviderRef`) that is still valid, a failure only affects this turn, and the session itself does not enter a terminal state.

### 2.2 New state transitions

| Event | Now | Changed to |
|---|---|---|
| Provider reports this turn failed (rate limit, 5xx…) | IDLE + `is_error` | Unchanged, with `failure` added |
| Process exits midway through the turn | **FAILED** | With a ref: snapshot the worktree first, then after `TurnCompleted{failure: AGENT_CRASHED}` enter **INTERRUPTED**. Without a ref (died on the first turn): still FAILED |
| Watchdog fires (stalled) | **FAILED** | Interrupt first, then kill the process group, then as above, with kind `STALLED` |
| Turn exceeds the total duration limit | **FAILED** | As above, with kind `TIMEOUT` |
| Start fails on the first turn | FAILED | Unchanged (no ref, nothing to continue), with `failure` added |
| Start fails on resume in a later turn | Silently switches to a new session | See §2.3 |
| Daemon restart | INTERRUPTED | Unchanged |

In the INTERRUPTED state, `SendMessage` brings the process up with resume, which is already supported. INTERRUPTED is also subject to `IdleSessionTTL`, and the session is closed when it expires.

The interrupted turn also gets a worktree snapshot, so the caller can get the "half done" diff. Concretely, reuse `completeTurn` with an extra parameter for the final state.

### 2.3 Resume: switch to a new session only on positive evidence (P4, P5)

1. **The adapter is responsible for giving explicit rejection evidence**, always reported as `RESUME_REJECTED`; better to miss a rejection than to report a false one.
   - **Claude**: the result's `errors[]` or stderr contains "no conversation found", or the returned session_id differs from the requested one. Claude's rejection shows up **in the first turn's events**, not as an error returned by `Start`, so the kernel has to recognize it in "the first turn after resume".
   - **Codex**: the error returned by `thread/resume` contains both a session-type noun (thread/rollout/session) and not found/unknown/does not exist.
   - **ACP**: look only at errors from the two calls `session/load` and `session/resume`: `-32002` (resource not found), or a session-type noun together with not found/invalid. Cancelled or timed-out calls don't count.
2. **Kernel handling**:
   - On `RESUME_REJECTED`, and if this turn has not made any tool calls yet (so no side effects are repeated), automatically switch to a new session:
     - clear the old ref;
     - restart with `NewRef`;
     - prepend a continuity notice that **appears only once** to the prompt (see below);
     - emit an `ErrorEvent{failure: RESUME_REJECTED}` saying "the context was lost; continuing in a new provider session".
   - Any other resume failure (auth, network, MCP unreachable…) **does not switch**: the ref is kept, the session enters INTERRUPTED with the failure attached, and once the problem is fixed, sending another message picks up where it left off.

Draft of the continuity notice:

> [Hermesh] The earlier context of this conversation can no longer be restored (reason: …). The changes from all previous turns are still saved in the current worktree, one commit per turn; first use `git log` and `git diff` to understand the work already done, then continue with the request below.

### 2.4 Context overflow (P6)

- Claude must recognize `terminal_reason=prompt_too_long` and treat it as a failure even when `is_error=false`.
- After receiving `CONTEXT_OVERFLOW`, mark the session as "ref invalid": the next message switches to a new session plus the continuity notice, as in §2.3. The `hint` must say so.

---

## 3. Stall detection (P1, P7)

### 3.1 Judge liveness by "activity", not by "events"

Today only normalized events refresh `lastEvent`. But Codex reasoning, command output deltas, and ACP's `agent_thought_chunk` are all dropped in the adapters. All of these actually show that "the process is still working".

- Add an event `provider.EventActivity`: not persisted and not sent downstream. The adapter emits one every time it reads a line of stdout, rate-limited to at most once every 5 seconds.
- When the kernel receives `EventActivity`, it only updates `lastActivity`.

### 3.2 Three windows

| Window | Condition | Default | When it fires |
|---|---|---|---|
| `FirstActivityTimeout` | No activity at all since this turn started (not counting EventRef) | 3 minutes | `STALLED` (explained as "no response after start") |
| `IdleTimeout` | There has been activity, and **no tool is currently running** | 15 minutes | `STALLED` |
| `ToolTimeout` | **A tool is running** (`l.tools` is non-empty; this set is already maintained: added on ToolCall, removed on ToolResult) | 2 hours, 0 means unlimited | `STALLED` (explained as "tool X has been running for more than …") |
| `TurnTimeout` | Total duration of a turn | **To be decided** (see §6) | `TIMEOUT` |

- The Janitor's check interval stays at 10 seconds.
- Once this turn's TurnCompleted enters processing (during the snapshot), the watchdog skips this session.
- Sleep: on macOS and Linux, Go's monotonic clock does not count system sleep, so a separate awake-time clock is unnecessary on these platforms.
- All windows can be configured with `hermeshd run` flags or `config.yaml`.

### 3.3 Actions when a window fires

In order: `Interrupt` (wait at most 5 seconds to see whether a TurnCompleted arrives normally) → kill the process group → snapshot → enter INTERRUPTED as in §2.2.

---

## 4. Process and pipe robustness (P8–P10)

1. **P8**: Claude's `Start` first starts `go s.read()`, then writes init and the prompt.
2. **P9**: when `Proc.Lines` hits a scan error, immediately kill the process group and produce an `AGENT_CRASHED` failure explaining "a single output line exceeded 32 MiB".
3. **P10**: after the main process exits, wait at most 2 more seconds to read stderr and stdout to the end; on timeout, close the read ends, equivalent to os/exec's `WaitDelay`. The `exec` fallback path sets `cmd.WaitDelay = 2s` directly. Also run `KillGroup` on the process group once more to clean up leftover grandchild processes. These processes should not be left behind when a session ends anyway; stopping processes on the idle TTL already works this way today.
4. **ACP**: after receiving the prompt response, wait for notifications to go quiet for 250 milliseconds (at most 2 seconds) before ending the turn, to avoid truncating the last few message chunks.

---

## 5. Observability

- **Metrics**:
  - `hermesh_turn_failures_total{agent,kind}`
  - `hermesh_watchdog_fired_total{window}`
  - `hermesh_resume_total{agent,outcome=resumed|rejected_fresh|failed_kept}`
- **Audit**: record the kind for every failed turn.
- **`hermesh status`**: the session list shows the failure category. `hermeshd doctor` shows counts of each failure kind over the last 24 hours.

---

## 6. Decisions for you to make

1. **`TurnTimeout` default**: suggest changing it to **0 (unlimited)** and relying only on the liveness windows in §3 to detect stalls. As long as it keeps producing events it is not killed. If it is kept, suggest relaxing it to 6 hours.
2. **Automatic retry**: suggest that **the kernel does not resend prompts automatically**, and only provides `retryable`, `retry_after` and exit code 7, leaving the decision to the caller. The reason is that resending risks repeating side effects. The only exception is §2.3: when resume is rejected and this turn has not called any tool yet, switch to a new session automatically.
3. **Context overflow**: whether the next message automatically continues in a new provider session (recommended), or the caller must explicitly open a new session.
4. **Default windows**: whether first activity 3 minutes, idle 15 minutes, and tool running 2 hours are right.
5. **Exit code 7**: whether to add it to the exit code conventions for agents.

---

## 7. Implementation phases and verification

Every phase comes with both unit tests and end-to-end tests with real binaries (`make e2e`). Scenarios that can be reproduced cheaply with the real CLIs on this machine go into `make e2e-real`.

| Phase | Content | Key tests |
|---|---|---|
| **1: Classification infrastructure** | Proto `Failure`; `provider/failure.go`; structured mappings for Claude/Codex/ACP; text classifier; redaction; CLI JSON output, exit code 7; `waitTurn` handles INTERRUPTED (P3, P11, P12) | Table-driven classifier tests with cases taken from **real output**: the bad-resume and bad-model Claude output captured today, Codex `codexErrorInfo` samples, ACP error codes, and string and numeric misclassification counterexamples; the Claude adapter's parsing is tested with recorded stream-json plus a fake CLI; e2e: when the fake agent returns 429, the CLI ends with exit code 7 and the kind in the JSON is correct; e2e-real: Claude and Codex given a nonexistent model yield `MODEL_UNAVAILABLE` |
| **2: Sessions that can continue** | The state transitions in §2, resume switching sessions only on evidence, the continuity notice, context overflow (P2, P4, P5, P6) | Kernel tests: add `crash`, `resume-reject` and `overflow` script commands to the fake provider; after a crash midway, the session enters INTERRUPTED, and the next message brings it up with the same ResumeRef; when resume is rejected, it switches to a new session, and the prompt carries the notice exactly once; on an auth failure the ref is kept; e2e: in a real daemon, after the agent process is killed, `session send` can continue and the diff is complete; e2e-real: replace Claude's ref with a random UUID, get `RESUME_REJECTED`, and succeed after the automatic switch to a new session |
| **3: Stall detection** | The activity event and three windows in §3 (P1, P7) | Kernel tests with millisecond windows: the idle window doesn't fire while a tool is running, firing when the tool window is exceeded, first activity timeout, no misjudgment while TurnCompleted is being processed; add `tool-sleep` and `hang` to the fake agent; e2e: a "long tool" runs past the idle window and completes normally |
| **4: Process robustness** | §4 (P8, P9, P10); the P8 change is very small and can go into phase 1 | Fake CLI: writes a large chunk first, then reads stdin (must not deadlock); when a single output line is too long, the process is reclaimed quickly; when a grandchild process holds stderr, `Done()` closes within about 2 seconds |

**Order**: 1 → 2 → 3, because the failures produced by 2 and 3 need the kinds from phase 1. The small fixes in 4 can be interleaved.

---

## 8. Implementation record (2026-10-16)

All four phases are done, settled according to the suggestions in §6: no limit on total turn duration by default; the kernel does not resend prompts automatically (the only exception: when resume is rejected and this turn has no tool calls yet, it reruns once in a new provider session); after a context overflow, the next message automatically continues in a new context; the windows are 3 minutes / 15 minutes / 2 hours (adjustable with `hermeshd run --idle-timeout/--tool-timeout/--turn-timeout`); exit code 7 was added.

Differences from the plan:

- `last_failure` lives in `SessionProgress` (§1.2 has been updated).
- The first activity window only looks at "whether there was any output after the process started", not at each turn: during long thinking, Claude's stream-json may produce no output for minutes, and counting per turn would kill it wrongly.
- Liveness uses `provider.Session.LastActivity()` (the time any stdout line was read) instead of an extra event.
- Pre-existing problems fixed along the way: `Sessions.Get` read the active session row without a lock (a data race with concurrent GetSession); at shutdown, background goroutines writing to an already-closed writer would panic; closing stdout immediately when the process exits could lose the last few lines of output (now it reads to the end, at most 2 seconds).
- Not done: the "wait for notifications to go quiet" window after an ACP prompt response (§4 item 4). By the protocol, notifications arrive before the response, and we haven't yet seen an agent that needs it.

Verification:

- Unit: classifier (real Claude/Codex output, recorded provider errors, numeric misclassification counterexamples), redaction, Claude/Codex/ACP adapter mappings, the kernel state machine and watchdog (`internal/machine/sessions_test.go`), pipes and processes (all three spawn methods).
- End-to-end: `e2e/reliability_test.go` (rate limit → exit code 7, auth → 5, recovery after a stall, long tools not misjudged, a waiting CLI ends with 7 and can continue when the daemon is killed with kill -9), `e2e/acp_test.go` (an ACP agent crashes midway and recovers via `session/load`).
- Real CLIs (`make e2e-real`): "unknown model" and "session does not exist" are classified correctly for Claude Code, Codex and claude-agent-acp; multi-turn and resume after process restart pass.

## 9. Chaos testing with the real Codex (`make e2e-chaos`, 2026-10-16)

The real Codex (0.162) on this machine goes through the real relay, daemon and CLI, with real faults injected. After each scenario, the same set of invariants is checked: the session is in a definite state; event seq numbers are contiguous with no duplicates, and every turn has a result; `session diff` matches `git diff`; the worktree content of an idle session equals the session branch head; the owner's repository was not touched; no leftover processes after the daemon exits; `hermeshd audit-verify` passes.

| Scenario | First result | Findings and fixes |
|---|---|---|
| S1 `kill -9` Codex while a tool is running | Failed | Codex runs tools in their own process group, so cleanup by process group couldn't reach them, and `sleep` became an orphan. Changed to tracking descendant processes: a global tracker samples the process table once a second, records all descendants of each agent (pid plus start time), and cleans them all up when the agent exits (`provider/tree.go`). macOS doesn't allow reading other processes' environment variables, so marking with environment variables was not used. |
| S2 Silent tool exceeding the idle window | Passed | A 79-second `sleep` was not misjudged under a 40-second idle window. |
| S3 Model service network cut | Failed (hung for the full 15 minutes) | After losing the network, Codex keeps retrying ("Reconnecting... waiting for network", exponential backoff, never gives up), and every retry notification was counted as "activity". Now the Codex adapter counts only real work as progress; retries show up in logs as classified error events; when the watchdog stops a turn, it reports the category of the last error (NETWORK, retryable) instead of a generic STALLED. |
| S4 `kill -9` the daemon while Codex is changing files | Passed | After the restart, `KillTree` cleaned up the old Codex's 7 processes (codex, node_repl, codex-code-mode-host, zsh, sleep, etc.). |
| S5 The agent commits, switches branches, and runs `reset --hard` itself | Failed | The snapshot was committed to the agent's `feature` branch, and the session branch missed the work from the last two turns. Now, when HEAD is not on the session branch, an extra commit is made on the session branch whose tree is the worktree's current content; the agent's own branch and git state are not affected. |
| S6 4 sessions, `--max-sessions 2` | Failed (false alarm) | The test queried states one by one, so the measurement itself was skewed; changed to computing from the state timestamps recorded by the daemon, with at most 2 at the same moment. |
| S7 Restart the relay during `--follow` | Failed | `peer.Client` held only one relay connection, which stayed dead after the relay restarted ("session shutdown"), so neither `--follow` nor `--wait` could recover. Now it redials automatically when the connection drops; also, a follow stream that ends before the session ends is always treated as a dropped connection. |
| S8 Codex not logged in | Passed | Classified as AUTH, exit code 5. Also fixed a false positive in the redaction rules ("basic authentication" was treated as a credential). |

Also fixed: `current_tool` was not cleared after a turn was interrupted.

Known limitations: if a tool process leaves the process group before the tracker samples it (within less than 1 second), and its parent process has also exited, it cannot be tracked. Also: when Codex loses the network, it is only stopped after the full idle window (15 minutes by default).

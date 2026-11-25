# Starting a session: what shows from Send to the first step (2026-11-25)

What a new session shows between the owner's Send and the agent's first
step, on the iPhone and the Mac, and what the machine does meanwhile. The
demo is [session-start-demo.html](session-start-demo.html).

## 0. Summary

- **Send shows the session at once.** The message is in its bubble under
  the session's title before the machine answers; the composer no longer
  waits on the call.
- **Where a message is, is said under it**, in Her's words for hers
  ([her-sending.md](her-sending.md) §5.2): nothing while it is quick,
  "Sending…" once it takes 400 ms, "Delivered when … is back" with Discard
  while its machine is offline, "Not delivered: why" with Try Again and
  Edit when the machine refused it.
- **Where the session works is a line of its own**, under the message,
  before the agent's first step: the worktree made and its branch, the
  branch the folder was switched to, or the folder kept. It is the owner's
  choice coming true, so it is said once, plainly, and opens to its details.
- **The line at the bottom says only what the agent does**: working,
  thinking, running tools, replying. Starting the agent and waiting for the
  model are "working".

## 1. Measured

On the owner's Mac (its 446 runs, and timed by hand):

| Step | Time |
|---|---|
| The phone to the machine and back, through the relay | 250–300 ms |
| A worktree of this repository | 0.42–0.59 s |
| The baseline checkpoint | 0.2–1.0 s, median 0.5 s |
| An agent started and answering `initialize` | Codex 0.06–0.38 s, Claude 0.4 s (2.7 s the first time), Antigravity about 4.5 s |
| A run's start to its first item | median 10.9 s, p90 23.3 s |

For Codex and Claude, about 1.65 s of that is the machine's; the rest is
the model's.

## 2. The machine

- `StartSession` does no git work: it answers once the session and its run
  are written. What needs no git stays: a branch to switch to is refused
  while another session works in the folder ("Not delivered" under the
  message). A worktree's branch (`hermesh/<session>`) is named in the
  answer.
- The first turn sets the folder up (`prepareWorkspace`): fetching the base
  from origin when asked, making the worktree, or switching the folder's
  branch. Each is one `WORKSPACE` turn item (`WorkspaceStep`), running while
  a step takes, then completed or failed with why. A failure ends the turn
  (`workspace_error`); the next message tries again. A turn stopped
  meanwhile leaves the item to be settled as interrupted with the run. A
  folder that is not a git repository has no item.
- The baseline checkpoint is taken while the agent starts, and waited for
  before the agent's lease is registered (an idle lease may be reaped).
- A session kept on the phone for an offline machine holds its first
  message, so its page shows it.

## 3. The apps

| Moment | Under the message | At the bottom |
|---|---|---|
| Send, until the machine answers (< 400 ms) | — (bubble faded) | — |
| … longer | Sending… | — |
| Refused | Not delivered: why · Try Again · Edit | — |
| Machine offline | Delivered when … is back · Discard (bubble faded) | — |
| Answered | — | Working |
| Folder being set up (> 400 ms) | a line of its own: Fetching / Making a worktree / Switching | Working |
| Set up | that line: New branch from main `hermesh/…`, Switched to branch `x`, Working in place, on branch `main` | Working, Thinking, Running tools, Replying |
| Set up failed | that line, failed, opening to why; the session's failure card at the end says what to do | — |

- The run's own error is not a row beside the failed line: the line says
  where it failed, the failure card what to do next.
- A message kept on the phone for an offline machine is its bubble, not
  also a row of the queue over the box (a machine writes a queued run's
  message only once the run starts).
- Try Again sends the same command id: an answer lost on the way makes no
  second session. Edit puts the message back in the composer.
- Opened right after Send, the session's page shows what Send knew (the
  session, its run, the message) and reads the session whole once.
- An older app skips the `WORKSPACE` item (an unknown kind) and shows the
  run's own error when it fails.

## 4. Tests

- `internal/kernel/branches_test.go`: every way a folder is set up, and
  each way it fails, as the item says it.
- `HermeshSessionsTests/TranscriptTests.swift`, `core/transcript.test.ts`:
  the row, its words and details, and the run's error hidden.
- iPhone `testThreadComposer`, Mac `thread-composer.spec.ts`: the message at
  once, the worktree's line before the reply, the switch's line, and a
  message sent to an offline machine.

## 5. Not doing (now)

- Starting the agent and making the worktree while the owner types
  (prewarming).
- Calls on a direct path instead of the relay
  ([peer-connectivity.md](peer-connectivity.md) §5.2).

# Hermesh performance: method, instrumentation and results

Goal: with 1000–5000 sessions running at the same time on a single machine, the latency, CPU and memory of hermeshd (and the relay) must stay under control, and the cost of every stage must be traceable. This document records the load-testing method, the instrumentation design, the bottlenecks found in each round and the results of the optimizations, as well as the limitations that remain.

Test machine: Apple Silicon, 12 cores, 64 GB memory, macOS 27, Go 1.27.

## 1. Load-testing method

### Components

- **bench/fakecodex**: an agent that simulates Codex, implementing a JSON-RPC subset of `codex app-server` (initialize, thread/start, thread/resume, turn/start, turn/interrupt). The shape of a turn is controlled by environment variables: duration, number of commands, output size per command, number of intermediate agent messages, number of text deltas, number of file writes, resident memory. It lets us measure Hermesh's own overhead without spending tokens and without model latency getting in the way.
- **bench**: a load-test driver that goes through the real full path.
  - **Network setup:** builds the relay and hermeshd and starts them as separate processes (`--debug-addr` exposes `/metrics` and pprof). The owner role issues certificates in-process with a software root key, and sets up N agent identities through the real pairing flow.
  - **Traffic:** agents establish E2E TLS tunnels through the relay and run Connect RPC over HTTP/2, the same path the real CLI takes.
  - **Scenario parameters:**

    | Parameter | Meaning |
    |---|---|
    | `-sessions` | Total number of sessions |
    | `-concurrency` | Number of concurrent workers |
    | `-turn-ms` | Duration of a single turn |
    | `-commands` | Number of commands per turn |
    | `-output-bytes` | Output size per command |
    | `-messages` | Number of intermediate agent messages |
    | `-deltas` | Number of text deltas |
    | `-writes` | Number of file writes |
    | `-watchers` | Extra event subscribers per session (simulating the app watching progress) |
    | `-no-worktree` | Don't create git worktrees; measure only the daemon itself |
    | `-hold` | How long to hold after everything is ready before cancelling all at once |

  - **Flow:** each worker runs StartSession → subscribes to the event stream until IDLE; when the hold ends, everything is cancelled at once, and it waits until all agent processes have exited.
- **Output**: `bench-results/<time>-<label>/`, including:
  - `report.md`: conclusions, per-stage latency, server-side histograms, resource table, per-second timeline;
  - `samples.json`;
  - `/metrics` snapshots from both ends;
  - CPU profile and goroutine dump from the middle of the load, and an optional execution trace (`-trace 2s`);
  - heap and goroutine profiles during the hold.

### Common scenarios

```bash
go run ./bench -sessions 5000 -concurrency 1000 -hold 5s -no-worktree -label spawn
```

```bash
go run ./bench -sessions 1000 -concurrency 500 -hold 5s -label git
```

```bash
go run ./bench -sessions 5000 -concurrency 5000 -agents 5 -turn-ms 60000 -commands 40 -messages 10 -output-bytes 2048 -writes 0 -watchers 1 -no-worktree -label stream
```

These correspond, in order, to three kinds of scenario:

1. **Startup/scheduling**: measures process creation, scheduling and SQLite writes;
2. **Real worktrees**: one worktree per session, with snapshot commits;
3. **Sustained streaming**: 5000 sessions online at once, each lasting 60 seconds, continuously producing tool calls and 2KB of output, with 2 subscribers per session.

## 2. Instrumentation: what to measure, and why

Principle: every stretch of queueing and every stretch of actual work gets its own histogram (4 buckets per doubling, quantiles interpolated within a bucket), so that "slow" can be broken down into "what it is waiting for" and "what it is doing".

| Location | Metrics | Question answered |
|---|---|---|
| Client | `start_rpc`, `turn1_to_running`, `turn1_first_event`, `turn1_turn_total`, `cancel_rpc` | Latency as the user perceives it |
| Client | `event_lag` / `watcher_event_lag` (timestamp the machine puts on an event → received by the subscriber), events per second and maximum latency per second | How real-time the real-time progress really is; at what moments stalls happen |
| RPC | `hermesh_machine_rpc_seconds{method}`, `handshake_seconds` | Server-side handling time |
| Scheduling | `queue_wait_seconds`, `provider_start_seconds`, `hermesh_provider_spawn_seconds`, `first_event_seconds`, `turn_seconds` | Whether a session is stuck in the queue, in spawning the process, or in the agent itself |
| git | `git_seconds{cmd}`, `git_wait_seconds`, `worktree_create_seconds`, `snapshot_seconds` | Cost of the git commands themselves and of queueing for them |
| Persistence | `write_enqueue_seconds` (backpressure), `write_begin_seconds` (waiting for the write connection), `write_commit_seconds`, `write_batch_seconds`, `start_commit_seconds`, `wal_checkpoint_seconds`, `wal_bytes` | Whether SQLite is the bottleneck, and at exactly which step it gets stuck |
| relay | `admission_seconds`, `tunnel_open_seconds`, `tunnels_active`, `tunnel_bytes_total` | The relay's overhead |
| Processes | ps sampling every second: CPU and RSS of the relay, hermeshd, spawn helpers, all agents, and the whole machine; goroutine count, fd count, `process_open_fds` | How resources grow with the number of sessions; marginal memory per session |

A pprof CPU profile only shows time spent "on CPU". So when wall-clock time is far larger than CPU time, three other tools have to fill in:

- goroutine dumps (to see what it is waiting for);
- `-pprof=syscall/sched` on an execution trace (to see system calls and scheduling latency);
- macOS's `sample <pid>` (to see kernel-mode thread stacks).

Most of the key findings this time came from these three tools.

## 3. Findings and optimizations (in chronological order)

| # | Symptom (metric) | Root cause | Fix |
|---|---|---|---|
| 1 | With 200 sessions, `worktree_create` p50 was 2.3s, with git taking only 47ms | One lock per repository serialized worktree creation | Removed the lock; retry on transient errors from lock conflicts |
| 2 | Occasional "branch already exists" under concurrency | With concurrent dispatch, the same session was claimed twice | Serialized dispatch, with the state check and the claim done inside the lock |
| 3 | One SQLite transaction per event | Synchronous writes | Batching writer: one transaction per commit, delivery after the commit (a crash can't make clients see events that were then lost) |
| 4 | With worktrees off and 2000 sessions, spawn p50 was 2.4s, about 160/s | On darwin, Go holds the global ForkLock while forking, and the larger the parent process the slower the fork | Switched to posix_spawn (via a libSystem trampoline, no cgo dependency) |
| 5 | `adddup2: bad file descriptor` with 5000 sessions | libc file actions reject fds ≥ OPEN_MAX (10240) | Moved the parent's ends above 10240; if the child's end is out of range, fall back to os/exec |
| 6 | About 1300 processes still alive after cancellation | The bench cancelled with the wrong agent, and the server rejected it on permissions | Fixed the bench; added a metric for the time from cancellation to everything being reclaimed |
| 7 | StartSession p50 about 630ms, `write_begin` 6.6s in total | The database had only one connection, so reads queued behind write transactions; each RPC also did 2 writes for grant anti-rollback | WAL read connection pool; the session row and idempotency key go through a merged commit; grant versions cached in memory; turn commits folded into the writer; the audit log no longer opens the file for every entry |
| 8 | With 5000 sessions, posix_spawn and close were both slow, even pwrite was slow | The kernel holds the parent's fd table lock while copying the fd table: 0.65ms each with 15k fds, 1.2ms with 30k, and all of hermeshd's fd operations are blocked meanwhile | **spawn helper**: NumCPU/2 helper processes with very few fds, receiving stdio via SCM_RIGHTS, reaping on our behalf and reporting exits; orphaned processes are still tracked when a helper dies |
| 9 | In git mode, only 16/s with 1000 sessions, `worktree add` 334ms | `git worktree add/remove` reads all existing worktrees, O(n) | Write the worktree metadata directly following gitrepository-layout, then `reset --hard`; delete the directory directly on removal; fall back to `git worktree add` for unusual repositories |
| 10 | In git mode, hermeshd itself used 306% CPU | hermeshd forked every git command itself | git is also run by the spawn helpers; skip the xcrun shim (7.8ms → 3.8ms); one fewer git process per worktree |
| 11 | In the streaming scenario, commits occasionally stuck for 70 seconds, and the WAL grew to 3.7GB | Automatic checkpoints ran on the commit path; there were always readers, so PASSIVE checkpoints could never make the WAL wrap around | Background checkpointer: mainly PASSIVE, RESTART when the WAL exceeds 64MB; added `journal_size_limit` |
| 12 | Occasional failures in the Claude sandbox tests (security issue) | After the hook returned allow, a Bash call with `dangerouslyDisableSandbox` would run outside the sandbox | Set `allowUnsandboxedCommands:false` and reject such calls in the hook; added a real CLI regression test |

## 4. Results

### Startup/scheduling (no worktrees, 200ms turns)

| | Initially | Now |
|---|---|---|
| 5000 sessions throughput | 85.6/s | **1366/s** (5000/5000 succeeded) |
| 5000 sessions spawn p50 | 5.05s | 12ms (8.5ms with enough helper concurrency) |
| 5000 sessions StartSession p50 (server side) | — | 80ms (concurrency 1000) |
| hermeshd average CPU | — | 74% |
| hermeshd memory per session | 238KB | **148KB** |
| Cancellation to all processes reclaimed | — | 2.9s (5000 of them) |

At 5000 sessions the limit is no longer in hermeshd but in macOS itself. With the system idle and 8-way parallelism, this machine can create at most about 2800 Go processes per second; it is slower while 5000 agent processes are also resident. Also, by Little's law for a closed-loop load test, throughput ≈ concurrency ÷ per-session latency (including the 200ms simulated turn).

### Real worktrees (1000 sessions, one snapshot commit per session)

| | Initially | Now |
|---|---|---|
| Throughput | 16/s | **224/s** |
| Single git command | add 578ms | add 10ms, reset 16ms, commit 16ms |
| hermeshd CPU | — | 55% |

The bottleneck now is the git processes themselves: 3 git processes per session, with `git_wait` queueing on NumCPU concurrency slots.

### Sustained streaming (5000 sessions online at once, 60-second turns, 2 subscribers per session)

| Metric | Value |
|---|---|
| Events delivered | 980k (**15.8k/s**, 831 MiB) |
| Delivery latency p50 / p90 | 3.5ms / 108ms |
| Maximum latency per second in steady state | 50–210ms |
| Actual turn duration | 62s (nominal 60s) |
| WAL peak | 68 MiB |
| hermeshd CPU | About 180–200% in steady state, peak 373% |
| hermeshd memory | 1.6 GB, 325KB per session |

The p99 of 2.2s comes from the startup storm of starting 5000 sessions within the first 4 seconds: the writer queue fills up and causes backpressure, and StartSession's merged commits are queueing too. With 2000 sessions, delivery latency is p50 2.7ms, p90 19ms, max < 0.9s.

## 5. Remaining limitations and next steps

- **APFS collapses under concurrent file creation (macOS kernel behavior)**: a single process creating files 512-way concurrently manages only 785/s; creating 2000 files 2000-way at once takes 33 seconds, while 4KB pwrites to other files stall for 10 seconds. In the load test, 2000 simulated agents writing files in the same second freeze hermeshd's SQLite writes and event delivery (reproducible with `-writes 1`). Real agents' edits are spread out over time and generally won't trigger this, but it shows that the single SQLite writer is a single point that can get stuck. Possible directions:
  - deliver before persisting, and skip seq ahead by an epoch interval after a restart;
  - or put SQLite on a separate volume.
- **Memory**: 5000 fakecodex processes total 29GB. A real Codex process is about 30–60MB, so on this machine about 1000 sessions would hit the memory limit. When memory is tight, the kernel reclaiming page cache makes pwrite stall. Suggest admission control based on `kern.memorystatus_vm_pressure_level`: when memory is under warning, new sessions stay in QUEUED.
- **StartSession under a startup storm**: when the writer falls behind, accumulate larger batches over a time window, or give StartSession's merged commits a separate priority queue.
- **Other**:
  - the codex adapter decodes JSON in two passes, currently about 10% of hermeshd CPU; the gain is limited, so it is left alone for now;
  - if git mode needs to be even faster, the snapshot (`add -A` + `commit`) could be merged into one process, or `gitSem` could be tuned for git-heavy scenarios.

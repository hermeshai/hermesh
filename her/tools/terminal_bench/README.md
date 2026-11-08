# Her × Terminal-Bench

Runs the real `her exec` kernel inside Harbor task containers, by default with **a ChatGPT subscription + GPT-6 Luna / low**.
Harbor is pinned to 0.25.0 (`uv.lock`), and the dataset for formal runs is pinned to `terminal-bench/terminal-bench@4.0.0`.

## Setup

From the repository root, build the host CLI used to log in:

```bash
cargo build -p her-cli --bin her
cd tools/terminal_bench
uv sync --locked
# Cross-compile a static Linux amd64 binary on macOS/Linux (needs Rust/rustup).
uv run --group build python build.py
# Or build in Docker: bash build.sh
uv run python run.py login
uv run python run.py models
```

`login` uses her's existing ChatGPT OAuth and needs you to authorize use of the subscription quota in a browser.
By default it selects `gpt-6-luna` from the models the connection actually returns; if it is not there, it fails and lists the available models, and never silently switches models or falls back to an API key.
Whether a model can be used on the subscription quota is decided by what the account actually returns.
`--force-build` only forces a rebuild of the agent environment; Harbor 0.25.0's separate verifier still pulls the official prebuilt image.

Evaluation credentials go in `.state/auth`, which git ignores, in a separate file store; the everyday macOS keychain is not copied.
This directory's absolute path is used in her's credential index; log in again after moving the workspace.
The adapter passes on only the ChatGPT profile/tokens; each task's sessions, personal settings and memory start from an empty environment.
Refreshed tokens are saved back, so **runs must be one at a time**; the adapter has a cross-process mutual exclusion check.
Do not start another her client with this directory at the same time. Do not upload `.state` or bake credentials into an image.
Subscription calls still use up quota; GPU/cloud sandbox resources are charged separately and are not part of the ChatGPT subscription.

## From validation to formal evaluation

```bash
# Print the commands only; do not start an evaluation
uv run python run.py smoke --dry-run
# A minimal task of our own: the model calls a tool to create a file, and the verifier checks its exact content
uv run python run.py smoke
# Pick one task and check the environment with the reference solution first (formal tasks may need large image downloads)
uv run python run.py oracle --task session-window-debug --force-build
# Trial run of the same task with her + GPT-6 Luna
uv run python run.py pilot --task session-window-debug --force-build
uv run python run.py view
```

`smoke_task` is an integration check, **not part of Terminal-Bench, and does not count toward the formal score**.
`pilot` and `full` have no turn limit by default (`--max-turns 0`), and each task keeps the dataset's time/resource settings.
To check only the integration, you can pass `--max-turns 12` explicitly; limited results should not be compared with the public leaderboard.
The fixed baseline of 5 tasks × 3 runs, finished on 2026-11-04, is in the [baseline report](BASELINE-2026-11-04.md) and the [run manifest](baseline-2026-11-04.json).
Only the one chosen task can be tried on Docker on a Mac; the full 4.0 set includes GPU and multi-container tasks and needs matching compute.

```bash
# For a cloud environment, install the extras of the same Harbor version, and set up the provider's authentication first
uv pip install 'harbor[modal]==0.25.0'
uv run python run.py oracle --sandbox modal --task '*' --attempts 5
uv run python run.py full --sandbox modal --attempts 5
```

All runs default to `-n 1 -r 0`, so that concurrency on the subscription and hidden retries do not multiply usage. Before a full run, run the reference solutions on the complete task set;
the `oracle` above takes only one task by default, and `--task '*'` selects all of them. Cloud service availability and task compatibility still have to be tested for real.

## Integration and results

The core command:

```bash
her exec - --model '<subscription-provider>/gpt-6-luna' \
  --thinking low --permission-mode bypass-permissions --output json
```

Interactive confirmation is bypassed only inside the isolated task container. The task text comes in through a file on stdin, to avoid shell interpolation; the task's default working directory is not overridden. The her process is given the Mozilla CA certificate bundle, so slim images without system certificates work.
The source repository, reference solutions and test answers are not put into the agent environment; Harbor runs the verification.

- `her_agent.py`: installs the CLI, runs the task, saves refreshed tokens and adds up token usage.
- `jobs/<time>/result.json`: Harbor's summary of results, with exceptions recorded apart from the reward.
- Each trial's `agent/her-result.json`, `her-stderr.log`, `sessions/`: the kernel's result and the full sessions.
- `.state/bin/revision.txt`, `source.patch`: the commit and source changes at build time; the adapter also records the binary's SHA-256.
- `cost_usd` stays unknown for a subscription, instead of being faked as a zero-dollar API call.

There is no ATIF conversion by default, and nothing is uploaded or submitted to a leaderboard automatically. The raw her sessions are kept for troubleshooting.
Uncommitted kernel changes in the original workspace do not get into this separate worktree automatically.

## Validation

```bash
uv run python -m unittest -v test_adapter.py
# Container/verifier check without calling a model
uv run harbor run -p smoke_task -a oracle -n 1 -k 1 -r 0 -o jobs
```

References: [run guide](https://www.tbench.ai/run), [custom agents](https://docs.harborframework.com/agents/custom-agents).

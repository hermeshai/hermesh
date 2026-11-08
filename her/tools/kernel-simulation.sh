#!/usr/bin/env bash
# Run from any directory. CARGO_TARGET_DIR is respected; outputs remain in that target dir.
set -euo pipefail
if ! command -v cargo >/dev/null 2>&1 && [[ -x "$HOME/.cargo/bin/cargo" ]]; then
  export PATH="$HOME/.cargo/bin:$PATH"
fi
cd "$(dirname "$0")/.."
results="${CARGO_TARGET_DIR:-target}/simulation-results"
mode="${1:-verify}"
case "$mode" in
  verify)
    cargo test -p her-mock --lib
    cargo test -p her-bench --bin her-sim
    cargo test --release -p her-cli --test simulation --test e2e_compaction
    cargo build --release -p her-cli -p her-bench
    "${CARGO_TARGET_DIR:-target}/release/her-sim" \
      --her "${CARGO_TARGET_DIR:-target}/release/her" \
      --protocol all --scenario suite --sessions 2 --context-window 16000 \
      --json "$results/verify.json"
    ;;
  load)
    cargo build --release -p her-cli -p her-bench
    "${CARGO_TARGET_DIR:-target}/release/her-sim" \
      --her "${CARGO_TARGET_DIR:-target}/release/her" \
      --protocol native --scenario macro --sessions 200 --concurrency 200 --repeats 3 \
      --json "$results/load.json"
    ;;
  startup)
    cargo build --release -p her-cli --bin her
    clock_args=()
    if [[ "$(uname -s)" == Linux ]]; then
      "${CC:-cc}" -O2 -Wall -Wextra -Werror tools/bench/startup_launcher.c \
        -o "${CARGO_TARGET_DIR:-target}/startup-launcher"
      clock_args=(--launcher "${CARGO_TARGET_DIR:-target}/startup-launcher")
    fi
    python3 tools/bench/bench_startup.py \
      --her "candidate=${CARGO_TARGET_DIR:-target}/release/her" \
      "${clock_args[@]}" --runs 200 --warmup 10 \
      --max-first-request-p50-ms "${HER_STARTUP_BUDGET_MS:-2}" \
      --json "$results/startup.json"
    ;;
  cli-startup|protocol-startup)
    # On Linux a static PIE, as rustc links musl by default: no dynamic loader, but it still relocates
    # itself on each command, so a new table of pointers shows up in the page faults (D-99).
    build_args=(--profile dist)
    binary="${CARGO_TARGET_DIR:-target}/dist/her"
    if [[ "$(uname -s)" == Linux ]]; then
      rust_target="${HER_STARTUP_TARGET:-$(uname -m)-unknown-linux-musl}"
      build_args+=(--target "$rust_target")
      binary="${CARGO_TARGET_DIR:-target}/$rust_target/dist/her"
    fi
    cargo build "${build_args[@]}" -p her-cli --bin her
    if [[ "$mode" == cli-startup ]]; then
      python3 tools/bench/bench_cli.py --her "candidate=$binary" \
        --hyperfine "${HYPERFINE:-hyperfine}" --runs 100 --warmup 10 \
        --json "$results/cli-startup.json"
    else
      clock_args=()
      if [[ "$(uname -s)" == Linux ]]; then
        "${CC:-cc}" -O2 -Wall -Wextra -Werror tools/bench/startup_launcher.c \
          -o "${CARGO_TARGET_DIR:-target}/startup-launcher"
        clock_args=(--launcher "${CARGO_TARGET_DIR:-target}/startup-launcher")
      fi
      python3 tools/bench/bench_protocol_startup.py --her "candidate=$binary" \
        "${clock_args[@]}" --runs 200 --warmup 10 --json "$results/protocol-startup.json"
    fi
    ;;
  *) echo "usage: $0 [verify|load|startup|cli-startup|protocol-startup]" >&2; exit 2 ;;
esac

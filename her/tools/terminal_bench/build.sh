#!/usr/bin/env bash
set -euo pipefail
bench_dir="$(cd "$(dirname "$0")" && pwd)"
repo_dir="$(cd "$bench_dir/../.." && pwd)"
mkdir -p "$bench_dir/.state/bin"
docker build --platform linux/amd64 -f "$bench_dir/Dockerfile.build" \
  --output "type=local,dest=$bench_dir/.state/bin" "$repo_dir"
git -C "$repo_dir" rev-parse HEAD > "$bench_dir/.state/bin/revision.txt"
git -C "$repo_dir" diff --binary -- crates Cargo.toml Cargo.lock rust-toolchain.toml > "$bench_dir/.state/bin/source.patch"

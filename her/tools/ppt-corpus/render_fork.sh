#!/usr/bin/env bash
# Render decks natively with her-slide against a local BetterOffice checkout.
# usage: render_fork.sh FORK_DIR OUT_DIR DECK.pptx [DECK.pptx ...]
# Builds a throwaway crate in $TMPDIR keyed by FORK_DIR; her-slide's system fonts are used.
set -euo pipefail
fork=$(cd "$1" && pwd); out=$2; shift 2
repo=$(cd "$(dirname "$0")/../.." && pwd)
key=$(printf %s "$fork" | shasum | cut -c1-10)
proj=${TMPDIR:-/tmp}/fork-render-$key
mkdir -p "$proj/src" "$proj/examples"
cat > "$proj/Cargo.toml" <<TOML
[package]
name = "fork-render"
version = "0.0.0"
edition = "2021"
[dependencies]
her-slide = { path = "$repo/crates/her-slide" }
[patch."https://github.com/Eliseowzy/betteroffice"]
betteroffice-pptx = { path = "$fork/crates/betteroffice-pptx" }
[profile.release]
debug = false
[workspace]
TOML
cp "$repo/crates/her-slide/examples/render.rs" "$proj/src/main.rs"
CARGO_TARGET_DIR="$proj/target" cargo build --release --manifest-path "$proj/Cargo.toml" -q
for deck in "$@"; do "$proj/target/release/fork-render" "$deck" "$out" 1280 || echo "FAILED: $deck" >&2; done

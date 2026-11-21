#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT"

echo "› HermeshKit Release unit tests"
swift test --package-path packages/HermeshKit -c release 2>&1 | tee "$OUT/unit.log"
# Disable SwiftPM's nested manifest sandbox, retaining the outer deny-network sandbox for all test processes.
echo "› Verify the demo with networking disabled"
sandbox-exec -p '(version 1) (allow default) (deny network*)' \
  swift test --package-path packages/HermeshKit --disable-sandbox -c release --skip-build --filter reviewDemo 2>&1 | tee "$OUT/offline.log"

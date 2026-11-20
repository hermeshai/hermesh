#!/usr/bin/env bash
# Complete local reviewer check, retaining the package and simulator logs together.
set -euo pipefail
cd "$(dirname "$0")/../../.."
OUT="${1:-$(mktemp -d)}"
packages/HermeshKit/test.sh "$OUT"
exec apps/ios/scripts/test-demo-ui.sh "$OUT" "${2:-full}"

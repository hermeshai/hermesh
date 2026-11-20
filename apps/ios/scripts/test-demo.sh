#!/usr/bin/env bash
# Release reviewer path on a new, disposable simulator. Never uses a user's signed-in simulator or device.
set -euo pipefail
cd "$(dirname "$0")/../../.."
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
DEVICE=""
SECOND_DEVICE=""
workers=()
cleanup() {
  for pid in ${workers[@]+"${workers[@]}"}; do kill "$pid" 2>/dev/null || true; done
  for device in "$DEVICE" "$SECOND_DEVICE"; do
    if [[ -n "$device" ]]; then xcrun simctl delete "$device" >/dev/null 2>&1 || true; fi
  done
}
trap cleanup EXIT
trap 'exit 130' INT TERM

swift test --package-path packages/HermeshKit -c release > "$OUT/unit.log" 2>&1
# Disable SwiftPM's nested manifest sandbox, retaining the outer deny-network sandbox for all test processes.
sandbox-exec -p '(version 1) (allow default) (deny network*)' \
  swift test --package-path packages/HermeshKit --disable-sandbox -c release --skip-build --filter reviewDemo > "$OUT/offline.log" 2>&1
RUNTIME="$(xcrun simctl list runtimes -j | python3 -c 'import json,sys; print(next(r["identifier"] for r in json.load(sys.stdin)["runtimes"] if r["isAvailable"] and r["identifier"].startswith("com.apple.CoreSimulator.SimRuntime.iOS-")))')"
DEVICE="$(xcrun simctl create 'Hermesh Release Review' com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro "$RUNTIME")"
apps/ios/scripts/build-core.sh > "$OUT/core.log" 2>&1
BUILD=(-project apps/ios/Hermesh.xcodeproj -scheme Hermesh -configuration Release
       -destination "platform=iOS Simulator,id=$DEVICE" -derivedDataPath "$OUT/derived" CODE_SIGNING_ALLOWED=NO)
xcodebuild build-for-testing "${BUILD[@]}" > "$OUT/build.log" 2>&1
APP="$OUT/derived/Build/Products/Release-iphonesimulator/Hermesh.app"
plans=("$OUT"/derived/Build/Products/*.xctestrun)
[[ ${#plans[@]} == 1 && -f "${plans[0]}" ]]
SECOND_DEVICE="$(xcrun simctl create 'Hermesh Release Review 2' com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro "$RUNTIME")"
mkdir -p "$OUT/interaction"
suite=apps/ios/scripts/test-demo-suite.sh
bash "$suite" "$DEVICE" "$OUT" "$APP" "${plans[0]}" \
  -only-testing:HermeshUITests/ScreenViewportUITests \
  -only-testing:HermeshUITests/DemoUITests/testTaskAndExit \
  -only-testing:HermeshUITests/DemoUITests/testChineseEntry \
  -only-testing:HermeshUITests/DemoUITests/testReviewPersistentSettingsIsolation \
  -only-testing:HermeshUITests/DemoUITests/testReviewGrantLifecycle &
workers+=("$!")
bash "$suite" "$SECOND_DEVICE" "$OUT/interaction" "$APP" "${plans[0]}" \
  -only-testing:HermeshUITests/HerReviewUITests \
  -only-testing:HermeshUITests/DemoUITests/testPrivacyPolicy \
  -only-testing:HermeshUITests/DemoUITests/testReviewBackgroundAndReset \
  -only-testing:HermeshUITests/DemoUITests/testReviewFreeAnswerAndInterrupt \
  -only-testing:HermeshUITests/DemoUITests/testReviewHerAndHerTeam \
  -only-testing:HermeshUITests/SessionDetailUITests &
workers+=("$!")
status=0
for pid in "${workers[@]}"; do wait "$pid" || status=$?; done
workers=()
exit "$status"

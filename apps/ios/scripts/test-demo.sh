#!/usr/bin/env bash
# Release reviewer path on a new, disposable simulator. Never uses a user's signed-in simulator or device.
set -euo pipefail
cd "$(dirname "$0")/../../.."
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
DEVICE=""
cleanup() {
  if [[ -n "$DEVICE" ]]; then
    xcrun simctl shutdown "$DEVICE" >/dev/null 2>&1 || true
    xcrun simctl delete "$DEVICE" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

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
xcrun simctl boot "$DEVICE"
xcrun simctl bootstatus "$DEVICE" -b > "$OUT/boot.log"
xcrun simctl install "$DEVICE" "$OUT/derived/Build/Products/Release-iphonesimulator/Hermesh.app"
CONTAINER="$(xcrun simctl get_app_container "$DEVICE" io.hermesh.ios data)"

# Seed synthetic preferences before this app's first launch. No account, key or existing device data is copied.
python3 - "$CONTAINER" "$OUT/expected.plist" <<'PYSEED'
import plistlib,sys
from pathlib import Path
values={"appearance":"light", "her-env":"real-env-sentinel", "herTab":"settings", "workPeriod":"week",
        "sessions.showArchived":b'true', "sessions.collapsed":b'["sentinel"]',
        "sessions.listView":b'{"group":"agent","newestFirst":false,"metadata":true}', "recentSearches":["real-search-sentinel"], "showHiddenFolders":True}
p=Path(sys.argv[1])/"Library/Preferences/io.hermesh.ios.plist"
p.parent.mkdir(parents=True,exist_ok=True)
p.write_bytes(plistlib.dumps(values))
Path(sys.argv[2]).write_bytes(plistlib.dumps(values))
PYSEED
xcodebuild test-without-building "${BUILD[@]}" -parallel-testing-enabled NO -resultBundlePath "$OUT/review.xcresult" \
  -only-testing:HermeshUITests/HerReviewUITests \
  -only-testing:HermeshUITests/DemoUITests/testTaskAndExit \
  -only-testing:HermeshUITests/DemoUITests/testChineseEntry \
  -only-testing:HermeshUITests/DemoUITests/testReviewPersistentSettingsIsolation \
  -only-testing:HermeshUITests/DemoUITests/testReviewBackgroundAndReset \
  -only-testing:HermeshUITests/DemoUITests/testReviewFreeAnswerAndInterrupt \
  -only-testing:HermeshUITests/DemoUITests/testReviewGrantLifecycle \
  -only-testing:HermeshUITests/DemoUITests/testReviewHerAndHerTeam > "$OUT/ui.log" 2>&1
xcrun simctl terminate "$DEVICE" io.hermesh.ios >/dev/null 2>&1 || true
# Xcode can migrate the data-container UUID during installation; inspect the current one.
CONTAINER="$(xcrun simctl get_app_container "$DEVICE" io.hermesh.ios data)"
python3 - "$CONTAINER" "$OUT/expected.plist" <<'PYVERIFY'
import json,plistlib,sys
from pathlib import Path
root=Path(sys.argv[1])
expected=plistlib.loads(Path(sys.argv[2]).read_bytes())
actual=plistlib.loads((root/"Library/Preferences/io.hermesh.ios.plist").read_bytes())
# Normal app startup may reorder JSON preference encoding; compare setting values.
def decoded(value):
    return json.loads(value) if isinstance(value,bytes) else value
changed=[k for k,v in expected.items() if decoded(actual.get(k))!=decoded(v)]
assert not changed, "Demo changed real preferences: "+", ".join(changed)
support=root/"Library/Application Support/Hermesh"
assert not any((support/"conversations").glob("*.pb")), "Demo saved real conversation files"
print(f"Preserved {len(expected)} synthetic real preferences; no real conversation files")
PYVERIFY
# Onboarding's empty world may be saved before or after Demo, depending on scene activation.
# Validate its decoded meaning, not file creation timing: the signed-out namespace must contain no owner or work.
cat > "$OUT/check-world.go" <<'GOCHECK'
package main

import (
    "fmt"
    "os"

    localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
    "google.golang.org/protobuf/proto"
)

func main() {
    data, err := os.ReadFile(os.Args[1])
    if os.IsNotExist(err) { fmt.Println("No real world cache"); return }
    if err != nil { panic(err) }
    var world localv1.WatchResponse
    if err = proto.Unmarshal(data, &world); err != nil { panic(err) }
    status := world.GetStatus()
    if status.GetInitialized() || status.GetOwnerId() != "" || status.GetAccount() != "" ||
        len(world.Machines)+len(world.Clients)+len(world.Devices)+len(world.Projects)+len(world.Sessions)+len(world.Pairings) != 0 {
        panic("Signed-out real cache contains an owner, account or world entities")
    }
    fmt.Println("Real world cache contains no owner, account or work")
}
GOCHECK
go run "$OUT/check-world.go" "$CONTAINER/Library/Application Support/Hermesh/world.pb"
printf 'Release demo passed. Evidence: %s\n' "$OUT"

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
APP="$OUT/derived/Build/Products/Release-iphonesimulator/Hermesh.app"
plutil -lint "$APP/PrivacyInfo.xcprivacy"
DISK_IMPORTS="$(xcrun nm -u "$APP/Hermesh" | awk '$NF ~ /^_f?stat(fs|vfs)(\$.*)?$/ {print $NF}')"
if [[ -n "$DISK_IMPORTS" ]]; then
  printf 'iOS imports unexpected disk-space APIs:\n%s\n' "$DISK_IMPORTS" >&2
  exit 1
fi
xcrun simctl install "$DEVICE" "$APP"
CONTAINER="$(xcrun simctl get_app_container "$DEVICE" io.hermesh.ios data)"

# Seed synthetic preferences before this app's first launch. No account, key or existing device data is copied.
python3 - "$CONTAINER" "$OUT/expected.plist" <<'PYSEED'
import plistlib,shutil,sqlite3,sys,tempfile
from pathlib import Path
values={"appearance":"light", "her-env":"real-env-sentinel", "herTab":"settings", "workPeriod":"week",
        "sessions.showArchived":b'true', "sessions.collapsed":b'["sentinel"]',
        "sessions.listView":b'{"group":"agent","newestFirst":false,"metadata":true}', "recentSearches":["real-search-sentinel"], "showHiddenFolders":True}
p=Path(sys.argv[1])/"Library/Preferences/io.hermesh.ios.plist"
p.parent.mkdir(parents=True,exist_ok=True)
p.write_bytes(plistlib.dumps(values))
Path(sys.argv[2]).write_bytes(plistlib.dumps(values))

# An existing profile with the first owner schema and committed, uncheckpointed WAL.
owner=Path(sys.argv[1])/"Library/Application Support/Hermesh/owner"
profile="01arz3ndektsv4rrffq69g5fav"
profile_dir=owner/"profiles"/profile
profile_dir.mkdir(parents=True)
(owner/"current").write_text(profile)
with tempfile.TemporaryDirectory() as tmp:
    source=Path(tmp)/"owner.db"
    db=sqlite3.connect(source)
    db.executescript("""
        PRAGMA journal_mode = WAL;
        CREATE TABLE kv (key TEXT PRIMARY KEY, value BLOB NOT NULL);
        CREATE TABLE grants (
            grant_id TEXT PRIMARY KEY, client_id TEXT NOT NULL, version INTEGER NOT NULL,
            signed BLOB NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL
        );
        PRAGMA user_version = 1;
    """)
    db.execute("INSERT INTO kv(key,value) VALUES(?,?)", ("sqlite-upgrade-fixture", b"committed in WAL"))
    db.commit()
    for name in ("owner.db", "owner.db-wal"):
        shutil.copyfile(source.parent/name, profile_dir/name)
    db.close()
PYSEED
xcodebuild test-without-building "${BUILD[@]}" -parallel-testing-enabled NO -resultBundlePath "$OUT/review.xcresult" \
  -only-testing:HermeshUITests/HerReviewUITests \
  -only-testing:HermeshUITests/DemoUITests/testTaskAndExit \
  -only-testing:HermeshUITests/DemoUITests/testPrivacyPolicy \
  -only-testing:HermeshUITests/DemoUITests/testChineseEntry \
  -only-testing:HermeshUITests/DemoUITests/testReviewPersistentSettingsIsolation \
  -only-testing:HermeshUITests/DemoUITests/testReviewBackgroundAndReset \
  -only-testing:HermeshUITests/DemoUITests/testReviewFreeAnswerAndInterrupt \
  -only-testing:HermeshUITests/DemoUITests/testReviewGrantLifecycle \
  -only-testing:HermeshUITests/DemoUITests/testReviewHerAndHerTeam \
  -only-testing:HermeshUITests/SessionDetailUITests > "$OUT/ui.log" 2>&1
xcrun simctl terminate "$DEVICE" io.hermesh.ios >/dev/null 2>&1 || true
# Xcode can migrate the data-container UUID during installation; inspect the current one.
CONTAINER="$(xcrun simctl get_app_container "$DEVICE" io.hermesh.ios data)"
python3 - "$CONTAINER" "$OUT/expected.plist" <<'PYVERIFY'
import json,plistlib,sqlite3,sys
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
owner=support/"owner"
assert (owner/"current").read_text().strip()=="01arz3ndektsv4rrffq69g5fav"
database=owner/"profiles"/"01arz3ndektsv4rrffq69g5fav"/"owner.db"
db=sqlite3.connect(database.as_uri()+"?mode=ro", uri=True)
assert db.execute("PRAGMA user_version").fetchone()[0]>1, "The existing owner database was not migrated"
assert db.execute("SELECT value FROM kv WHERE key='sqlite-upgrade-fixture'").fetchone()==(b"committed in WAL",)
assert db.execute("PRAGMA integrity_check").fetchone()==("ok",)
print("Migrated existing owner database and kept its WAL data")
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

#!/usr/bin/env bash
# Archive the iPhone app and upload it to TestFlight (docs/iphone-app.md §6):
#   VERSION=0.1.1 apps/ios/scripts/testflight.sh
#   VERSION                     the release's version, also the core's
#   BUILD                       the build number (default: the commit count, which only grows on main)
#   ASC_KEY_ID, ASC_ISSUER_ID   an App Store Connect API key of the team
#   ASC_KEY_PATH                its .p8 (default ~/.appstoreconnect/private_keys/AuthKey_$ASC_KEY_ID.p8)
set -euo pipefail
cd "$(dirname "$0")/../../.."
VERSION="${VERSION:?set VERSION, e.g. 0.1.1}"
VERSION="${VERSION#v}"
BUILD="${BUILD:-$(git rev-list --count HEAD)}"
KEY_ID="${ASC_KEY_ID:?set ASC_KEY_ID}"
ISSUER="${ASC_ISSUER_ID:?set ASC_ISSUER_ID}"
KEY="${ASC_KEY_PATH:-$HOME/.appstoreconnect/private_keys/AuthKey_$KEY_ID.p8}"
OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT
AUTH=(-allowProvisioningUpdates -authenticationKeyPath "$KEY" -authenticationKeyID "$KEY_ID" -authenticationKeyIssuerID "$ISSUER")

# FORCE rebuilds the core, stamped with VERSION.
VERSION="$VERSION" FORCE=1 xcodebuild archive -project apps/ios/Hermesh.xcodeproj -scheme Hermesh -configuration Release \
  -destination 'generic/platform=iOS' -archivePath "$OUT/Hermesh.xcarchive" "${AUTH[@]}" \
  MARKETING_VERSION="$VERSION" CURRENT_PROJECT_VERSION="$BUILD"
cat > "$OUT/export.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>method</key><string>app-store-connect</string>
  <key>destination</key><string>upload</string>
  <key>signingStyle</key><string>automatic</string>
  <key>teamID</key><string>G4LW3GC2T7</string>
</dict></plist>
EOF
xcodebuild -exportArchive -archivePath "$OUT/Hermesh.xcarchive" -exportOptionsPlist "$OUT/export.plist" -exportPath "$OUT/export" "${AUTH[@]}"
echo "✓ Hermesh $VERSION ($BUILD) uploaded; TestFlight lists it once App Store Connect has processed it"

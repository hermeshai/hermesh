#!/usr/bin/env bash
# Build and sign Hermesh Signer.app (universal) into a directory:
#   apps/signer/macos/scripts/build-signer.sh <output-dir>   →  <output-dir>/Hermesh Signer.app
# The desktop app bundles it as Contents/Helpers/Hermesh Signer.app.
#
# Environment variables:
#   HERMESH_SIGNING_IDENTITY  signing identity; ad-hoc if unset
#   HERMESH_SIGNER_PROFILE    a Developer ID provisioning profile for com.hermesh.app.signer. When set, it is
#                             embedded and the app is signed with Bundle/Signer.entitlements (its keychain group),
#                             so the root key is kept in the Keychain; without it the signer keeps the key's
#                             Secure Enclave handle in a file.
#   VERSION                   version number (default 0.1.0)
set -euo pipefail
OUT="${1:?usage: build-signer.sh <output-dir>}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
cd "$(dirname "$0")/.."
APP="$OUT/Hermesh Signer.app"
VERSION="${VERSION:-0.1.0}"
BUILD="$(date +%Y%m%d%H%M)"
SIGNING_IDENTITY="${HERMESH_SIGNING_IDENTITY:--}"
PROFILE="${HERMESH_SIGNER_PROFILE:-}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "› building HermeshSigner (universal)"
swift build -c release --arch arm64 --arch x86_64 2>&1 | grep -v "x86_64 architecture is deprecated"
BIN_DIR="$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)"
BIN="$BIN_DIR/HermeshSigner"

echo "› assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
MAIN="$APP/Contents/MacOS/HermeshSigner"
cp "$BIN" "$MAIN"
sed -e "s/__VERSION__/$VERSION/" -e "s/__BUILD__/$BUILD/" Bundle/Info.plist > "$APP/Contents/Info.plist"
# The UI strings (English and Simplified Chinese), found through Bundle.module in Contents/Resources.
mkdir -p "$APP/Contents/Resources"
cp -R "$BIN_DIR/HermeshSigner_HermeshSigner.bundle" "$APP/Contents/Resources/"
# The signing cards' strings come from the shared HermeshKit package.
cp -R "$BIN_DIR/HermeshKit_HermeshKit.bundle" "$APP/Contents/Resources/"

# SwiftPM's new build system writes the deployment target into LC_BUILD_VERSION as the linked SDK version,
# and the system then turns off the new SDK's UI behavior; change it back to the real SDK version (must happen before signing).
SDK_VERSION="$(xcrun --sdk macosx --show-sdk-version)"
BUILD_INFO="$(xcrun vtool -show-build "$MAIN")"
MIN_OS="$(awk '$1 == "minos" { print $2; exit }' <<<"$BUILD_INFO")"
RECORDED_SDK="$(awk '$1 == "sdk" { print $2; exit }' <<<"$BUILD_INFO")"
if [ -n "$MIN_OS" ] && [ -n "$SDK_VERSION" ] && [ "$RECORDED_SDK" != "$SDK_VERSION" ]; then
  for a in arm64 x86_64; do
    xcrun vtool -arch "$a" -set-build-version macos "$MIN_OS" "$SDK_VERSION" -replace -output "$MAIN" "$MAIN"
  done
  echo "› linked SDK version $RECORDED_SDK → $SDK_VERSION (min OS $MIN_OS)"
fi

SIGN_ARGS=(--force --options runtime)
if [ "$SIGNING_IDENTITY" = "-" ]; then SIGN_ARGS+=(--timestamp=none); else SIGN_ARGS+=(--timestamp); fi
if [ -n "$PROFILE" ]; then
  TEAM="$(security cms -D -i "$PROFILE" | plutil -extract TeamIdentifier.0 raw -o - -)"
  cp "$PROFILE" "$APP/Contents/embedded.provisionprofile"
  sed "s/TEAMID/$TEAM/g" Bundle/Signer.entitlements > "$TMP/Signer.entitlements"
  SIGN_ARGS+=(--entitlements "$TMP/Signer.entitlements")
  echo "› embedding the provisioning profile; keychain group $TEAM.com.hermesh.signer"
else
  echo "› no HERMESH_SIGNER_PROFILE: no keychain group; the root key's Secure Enclave handle is kept in a file" >&2
fi
echo "› signing with: $SIGNING_IDENTITY"
codesign "${SIGN_ARGS[@]}" --sign "$SIGNING_IDENTITY" "$APP"
codesign --verify --strict "$APP"
echo "✓ $APP"

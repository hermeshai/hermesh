#!/usr/bin/env bash
# Build the owner role (cmd/hermesh-mobile) as a static library for the
# iPhone app, device and simulator (arm64):
#   apps/ios/scripts/build-core.sh   →   apps/ios/build/HermeshCore.xcframework
# It does nothing when the framework is newer than every Go source file
# (the Xcode build runs it before each build). VERSION stamps the version.
set -euo pipefail
cd "$(dirname "$0")/../../.."
OUT=apps/ios/build/HermeshCore.xcframework
VERSION="${VERSION:-0.0.0-dev}"
if [[ -d "$OUT" && -z "${FORCE:-}" ]] && [[ -z "$(find cmd internal pkg gen go.mod go.sum -newer "$OUT/Info.plist" -name '*.go' -o -newer "$OUT/Info.plist" -name 'go.*' | head -1)" ]]; then
  echo "› HermeshCore.xcframework is up to date"
  exit 0
fi
# Xcode's build environment points these at its own SDK; Go needs its own.
unset SDKROOT IPHONEOS_DEPLOYMENT_TARGET
export PATH="$PATH:/usr/local/go/bin:/opt/homebrew/bin"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

build() { # sdk, clang target, name
  local sdk=$1 target=$2 dir="$TMP/$3" sysroot
  sysroot="$(xcrun --sdk "$sdk" --show-sdk-path)"
  mkdir -p "$dir/Headers"
  echo "› building the core for $3"
  CGO_ENABLED=1 GOOS=ios GOARCH=arm64 CC="$(xcrun --sdk "$sdk" -f clang)" \
    CGO_CFLAGS="-isysroot $sysroot -target $target -O2" CGO_LDFLAGS="-isysroot $sysroot -target $target" \
    go build -trimpath -buildmode=c-archive -ldflags "-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${VERSION#v}" \
    -o "$dir/libhermeshcore.a" ./cmd/hermesh-mobile
  mv "$dir/libhermeshcore.h" "$dir/Headers/HermeshCore.h"
  printf 'module HermeshCore {\n  header "HermeshCore.h"\n  export *\n}\n' > "$dir/Headers/module.modulemap"
}
build iphoneos arm64-apple-ios26.0 device
build iphonesimulator arm64-apple-ios26.0-simulator simulator
rm -rf "$OUT"
mkdir -p "$(dirname "$OUT")"
xcodebuild -create-xcframework \
  -library "$TMP/device/libhermeshcore.a" -headers "$TMP/device/Headers" \
  -library "$TMP/simulator/libhermeshcore.a" -headers "$TMP/simulator/Headers" \
  -output "$OUT" >/dev/null
echo "› $OUT"

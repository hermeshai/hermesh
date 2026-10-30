#!/bin/sh
# Checks that a release's files are all there and right, before anyone can
# install them: every program in the signed checksum list (signed by the
# release key, each hash matching), the version, install.sh and install.ps1
# with the key, the Mac app with feeds naming this version's zip, and the
# Windows app with signed feeds naming this version's package. The release
# workflow runs it on the draft before publishing, and again on the
# published files before promoting (docs/development.md, Releasing).
#   scripts/release-check.sh <dir> <version, with or without v>
#   RELEASE_PUBKEY_FILE  the public key (default scripts/release-pubkey.pem)
set -eu
DIR="${1:?usage: release-check.sh <dir> <version>}"
VERSION="${2:?usage: release-check.sh <dir> <version>}"
VERSION="${VERSION#v}"
PUB="${RELEASE_PUBKEY_FILE:-$(dirname "$0")/release-pubkey.pem}"

fail() { echo "release check: $*" >&2; exit 1; }
need() { [ -s "$DIR/$1" ] || fail "$1 is missing"; }

for f in checksums.txt checksums.txt.sig version install.sh install.ps1; do need "$f"; done
openssl dgst -sha256 -verify "$PUB" -signature "$DIR/checksums.txt.sig" "$DIR/checksums.txt" >/dev/null 2>&1 \
  || fail "checksums.txt is not signed by the release key"
[ "$(cat "$DIR/version")" = "$VERSION" ] || fail "version says $(cat "$DIR/version"), not $VERSION"
grep -q '  version$' "$DIR/checksums.txt" || fail "version is not in checksums.txt"
for os in darwin linux; do
  for arch in arm64 amd64; do
    for cmd in hermeshd hermesh hermesh-relay; do
      need "$cmd-$os-$arch"
      grep -q "  $cmd-$os-$arch\$" "$DIR/checksums.txt" || fail "$cmd-$os-$arch is not in checksums.txt"
    done
  done
done
for arch in arm64 amd64; do
  for cmd in hermeshd hermeshdw hermesh; do
    need "$cmd-windows-$arch.exe"
    grep -q "  $cmd-windows-$arch.exe\$" "$DIR/checksums.txt" || fail "$cmd-windows-$arch.exe is not in checksums.txt"
  done
done
if command -v sha256sum >/dev/null; then sum="sha256sum"; else sum="shasum -a 256"; fi
(cd "$DIR" && $sum -c checksums.txt) >/dev/null 2>&1 || fail "a file does not match checksums.txt"
for f in install.sh install.ps1; do
  ! grep -q __HERMESH_RELEASE_PUBKEY__ "$DIR/$f" || fail "$f has no release key"
  grep -qF "$(sed -n 2p "$PUB")" "$DIR/$f" || fail "$f has another key than the release key"
done
for arch in arm64 x64; do
  need "Hermesh-macos-$arch.dmg"
  need "Hermesh-macos-$arch.zip"
  need "RELEASES-darwin-$arch.json"
  grep -q "\"currentRelease\": \"$VERSION\"" "$DIR/RELEASES-darwin-$arch.json" || fail "RELEASES-darwin-$arch.json is not for $VERSION"
  grep -q "/releases/download/v$VERSION/Hermesh-macos-$arch.zip\"" "$DIR/RELEASES-darwin-$arch.json" || fail "RELEASES-darwin-$arch.json names another zip"
done
need Hermesh.ico
for arch in arm64 x64; do
  need "Hermesh-windows-$arch-setup.exe"
  need "Hermesh-windows-$arch.nupkg"
  feed="$DIR/RELEASES-win32-$arch.json"
  need "RELEASES-win32-$arch.json"
  need "RELEASES-win32-$arch.json.sig"
  openssl dgst -sha256 -verify "$PUB" -signature "$feed.sig" "$feed" >/dev/null 2>&1 || fail "RELEASES-win32-$arch.json is not signed by the release key"
  grep -q "\"currentRelease\": \"$VERSION\"" "$feed" || fail "RELEASES-win32-$arch.json is not for $VERSION"
  grep -q "/releases/download/v$VERSION/Hermesh-windows-$arch.nupkg\"" "$feed" || fail "RELEASES-win32-$arch.json names another package"
  grep -q "\"sha256\": \"$($sum "$DIR/Hermesh-windows-$arch.nupkg" | cut -d' ' -f1)\"" "$feed" || fail "Hermesh-windows-$arch.nupkg is not the package RELEASES-win32-$arch.json names"
done
echo "✓ release v$VERSION is complete"

#!/usr/bin/env bash
# Builds release assets into dist/release: binaries for darwin/linux/windows
# on arm64/amd64, their version, a signed checksum list and install.sh with
# the public key. Windows also gets hermeshdw, hermeshd without a console
# window, which its background service runs (docs/windows.md §4.4). Linux
# and Windows also get her, Her's kernel, and her console, built beforehand,
# which install.sh and install.ps1 put beside hermeshd (docs/her-server.md §6.4), and
# hermesh-screen, Computer Control's helper (docs/computer-control-linux.md).
#   RELEASE_SIGNING_KEY_FILE  path to the ECDSA P-256 private key (PEM)
#   VERSION                   e.g. v0.1.0
#   HER_LINUX_AMD64, HER_LINUX_ARM64  her for Linux, static with musl
#   HER_WINDOWS_AMD64, HER_WINDOWS_ARM64  her for Windows, which install.ps1 puts beside hermeshd
#   HER_CONSOLE               her console's built files (her/apps/console/dist)
#   SCREEN_LINUX_AMD64, SCREEN_LINUX_ARM64  hermesh-screen for Linux (apps/screen/linux/build-release.sh)
#   HER_PDF_LINUX_AMD64, HER_PDF_LINUX_ARM64  folders with her-doc-worker (her's PDF worker, against glibc) and
#                             PDFium (node apps/desktop/scripts/pdfium.mjs linux <x64|arm64> <folder> puts PDFium there)
#   HER_COMPUTER_LINUX_AMD64, HER_COMPUTER_LINUX_ARM64  folders with agent-desktop for Linux, which
#                             Her's computer use runs (node apps/desktop/scripts/agent-desktop.mjs linux <x64|arm64> <folder>)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:?set VERSION}"
OUT=dist/release
rm -rf "$OUT" && mkdir -p "$OUT"
LDFLAGS="-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${VERSION#v}"
for os in darwin linux; do
  for arch in arm64 amd64; do
    for cmd in hermeshd hermesh hermesh-relay; do
      GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/$cmd-$os-$arch" "./cmd/$cmd"
    done
  done
done
for arch in arm64 amd64; do
  for cmd in hermeshd hermesh; do
    GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/$cmd-windows-$arch.exe" "./cmd/$cmd"
  done
  GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS -H windowsgui" -o "$OUT/hermeshdw-windows-$arch.exe" ./cmd/hermeshd
done
[ -z "${HER_LINUX_AMD64:-}" ] || install -m 0755 "$HER_LINUX_AMD64" "$OUT/her-linux-amd64"
[ -z "${HER_LINUX_ARM64:-}" ] || install -m 0755 "$HER_LINUX_ARM64" "$OUT/her-linux-arm64"
[ -z "${HER_WINDOWS_AMD64:-}" ] || install -m 0644 "$HER_WINDOWS_AMD64" "$OUT/her-windows-amd64.exe"
[ -z "${HER_WINDOWS_ARM64:-}" ] || install -m 0644 "$HER_WINDOWS_ARM64" "$OUT/her-windows-arm64.exe"
[ -z "${SCREEN_LINUX_AMD64:-}" ] || install -m 0755 "$SCREEN_LINUX_AMD64" "$OUT/hermesh-screen-linux-amd64"
[ -z "${SCREEN_LINUX_ARM64:-}" ] || install -m 0755 "$SCREEN_LINUX_ARM64" "$OUT/hermesh-screen-linux-arm64"
TAR_FLAGS=()
tar --no-xattrs --no-mac-metadata -cf /dev/null -T /dev/null 2>/dev/null && TAR_FLAGS=(--no-xattrs --no-mac-metadata)
pack_tgz() { COPYFILE_DISABLE=1 tar ${TAR_FLAGS[@]+"${TAR_FLAGS[@]}"} -czf "$1" -C "$2" .; }
[ -z "${HER_CONSOLE:-}" ] || pack_tgz "$OUT/her-console.tar.gz" "$HER_CONSOLE"
[ -z "${HER_PDF_LINUX_AMD64:-}" ] || pack_tgz "$OUT/her-pdf-linux-amd64.tar.gz" "$HER_PDF_LINUX_AMD64"
[ -z "${HER_PDF_LINUX_ARM64:-}" ] || pack_tgz "$OUT/her-pdf-linux-arm64.tar.gz" "$HER_PDF_LINUX_ARM64"
[ -z "${HER_COMPUTER_LINUX_AMD64:-}" ] || pack_tgz "$OUT/her-computer-linux-amd64.tar.gz" "$HER_COMPUTER_LINUX_AMD64"
[ -z "${HER_COMPUTER_LINUX_ARM64:-}" ] || pack_tgz "$OUT/her-computer-linux-arm64.tar.gz" "$HER_COMPUTER_LINUX_ARM64"
# The version, signed with the programs, for installed ones to update (docs/machine-updates.md).
printf '%s\n' "${VERSION#v}" > "$OUT/version"
(cd "$OUT" && (command -v sha256sum >/dev/null && sha256sum her* version || shasum -a 256 her* version) > checksums.txt)
openssl dgst -sha256 -sign "${RELEASE_SIGNING_KEY_FILE:?set RELEASE_SIGNING_KEY_FILE}" -out "$OUT/checksums.txt.sig" "$OUT/checksums.txt"
PUBKEY_FILE="${RELEASE_PUBKEY_FILE:-scripts/release-pubkey.pem}"
openssl dgst -sha256 -verify "$PUBKEY_FILE" -signature "$OUT/checksums.txt.sig" "$OUT/checksums.txt"
python3 - "$OUT" "$PUBKEY_FILE" <<'PY'
import sys
pub = open(sys.argv[2]).read().strip()
for name in ("install.sh", "install.ps1"):
    src = open("scripts/" + name).read()
    open(sys.argv[1] + "/" + name, "w").write(src.replace("__HERMESH_RELEASE_PUBKEY__", pub))
PY
chmod +x "$OUT/install.sh"
echo "✓ release assets in $OUT"

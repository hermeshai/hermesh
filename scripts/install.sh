#!/bin/sh
# Hermesh installer. Downloads release binaries from GitHub Releases,
# verifies them against a signed checksum list, installs them under
# ~/.local/bin and (for machines) starts pairing.
#
#   Add a machine:   curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- [connection address]
#                    (without one: $HERMESH_CENTER, else the official center https://hermesh.io)
#   Agent CLI only:  curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
#
# Environment: HERMESH_VERSION (default latest), HERMESH_BIN_DIR (default ~/.local/bin)
set -eu

REPO="hermeshai/hermesh"
VERSION="${HERMESH_VERSION:-latest}"
BIN_DIR="${HERMESH_BIN_DIR:-$HOME/.local/bin}"

# Release signing public key (ECDSA P-256). Replaced at release time.
PUBKEY='__HERMESH_RELEASE_PUBKEY__'

die() { echo "hermesh install: $*" >&2; exit 1; }

MODE=machine
ADDRESS=""
NAME=""
while [ $# -gt 0 ]; do
  case "$1" in
    --cli) MODE=cli ;;
    --name) shift; NAME="${1:-}" ;;
    -*) die "unknown option $1" ;;
    *) ADDRESS="$1" ;;
  esac
  shift
done

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  *) die "unsupported OS $(uname -s)" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64) ARCH=amd64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac
[ "$(id -u)" != 0 ] || die "do not run as root; Hermesh runs as a normal user"

command -v curl >/dev/null || die "curl is required"
command -v openssl >/dev/null || die "openssl is required to verify the download"

if [ -n "${HERMESH_DOWNLOAD_BASE:-}" ]; then
  BASE="$HERMESH_DOWNLOAD_BASE"
elif [ "$VERSION" = latest ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if [ "$MODE" = cli ]; then BINS="hermesh"; else BINS="hermeshd hermesh"; fi

echo "› downloading checksums"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt"
curl -fsSL "$BASE/checksums.txt.sig" -o "$TMP/checksums.txt.sig"
case "$PUBKEY" in
  __HERMESH*) die "this installer has no release signing key; download a released install.sh" ;;
esac
printf '%s\n' "$PUBKEY" > "$TMP/pub.pem"
openssl dgst -sha256 -verify "$TMP/pub.pem" -signature "$TMP/checksums.txt.sig" "$TMP/checksums.txt" >/dev/null 2>&1 \
  || die "checksum signature verification FAILED; refusing to install"

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

mkdir -p "$BIN_DIR"
for b in $BINS; do
  asset="$b-$OS-$ARCH"
  echo "› downloading $asset"
  curl -fsSL "$BASE/$asset" -o "$TMP/$asset"
  want="$(grep " $asset\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
  [ -n "$want" ] || die "$asset is not in the signed checksum list"
  [ "$(sha256 "$TMP/$asset")" = "$want" ] || die "$asset checksum mismatch"
  install -m 0755 "$TMP/$asset" "$BIN_DIR/$b"
done
echo "✓ installed to $BIN_DIR"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "note: add $BIN_DIR to your PATH" ;;
esac

if [ "$MODE" = cli ]; then
  echo "Next: hermesh auth login   (the official center; or give a connection address)"
  exit 0
fi

set -- pair
[ -n "$ADDRESS" ] && set -- "$@" "$ADDRESS"
[ -n "$NAME" ] && set -- "$@" --name "$NAME"
"$BIN_DIR/hermeshd" "$@"
"$BIN_DIR/hermeshd" doctor || true

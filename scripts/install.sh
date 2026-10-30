#!/bin/sh
# Hermesh installer. Downloads release binaries from GitHub Releases,
# verifies them against a signed checksum list, installs them under
# ~/.local/bin and (for machines) starts pairing.
#
#   Add a machine:   curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- [connection address]
#                    (without one: $HERMESH_CENTER, else the official center https://hermesh.io)
#   Agent CLI only:  curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- --cli
#   Trusted device:  … | sh -s -- --device   (a computer that approves and runs no coding agents)
#   As root:         … | sh -s -- --user dev   (installs for that user, creating it if needed; without it, asks, and root is the default)
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
FOR_USER=""
while [ $# -gt 0 ]; do
  case "$1" in
    --cli) MODE=cli ;;
    --device) MODE=device ;;
    --name) shift; NAME="${1:-}" ;;
    --user) shift; FOR_USER="${1:-}" ;;
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
# A terminal to ask on: under `curl … | sh` stdin is this script.
TTY=""
if (: </dev/tty) 2>/dev/null; then TTY=/dev/tty; fi

# Hermesh runs as the user it is installed for. As root, the installer says
# so and installs for root, or for another user you name.
AS=""
if [ "$(id -u)" = 0 ] && [ -z "$FOR_USER" ] && [ -n "$TTY" ]; then
  echo "You are root: Hermesh will be installed for root."
  printf 'Press Enter to continue, or type another user to install for (q quits): '
  read -r FOR_USER <"$TTY" || true
  case "$FOR_USER" in q | Q) echo "Nothing was installed."; exit 0 ;; esac
fi
if [ "$(id -u)" = 0 ] && [ "${FOR_USER:-root}" = root ]; then
  # For root itself, whose service starts at boot with nobody logged in.
  # USER may be unset (cloud-init, docker exec) or still another's (su).
  FOR_USER=""
  export USER=root LOGNAME=root
  if command -v loginctl >/dev/null && loginctl enable-linger root 2>/dev/null; then
    echo "✓ Installing for root, with lingering on"
  else
    echo "✓ Installing for root"
  fi
elif [ "$(id -u)" = 0 ]; then
  if ! id "$FOR_USER" >/dev/null 2>&1; then
    if [ -n "$TTY" ]; then
      printf '“%s” doesn'"'"'t exist. Create it? [Y/n] ' "$FOR_USER"
      read -r yes <"$TTY" || true
      case "$yes" in n* | N*) die "nothing was installed" ;; esac
    fi
    useradd -m -s /bin/bash "$FOR_USER" || die "could not create the user $FOR_USER"
    echo "✓ Created $FOR_USER"
  fi
  uid="$(id -u "$FOR_USER")"
  home="$(getent passwd "$FOR_USER" | cut -d: -f6)"
  if loginctl enable-linger "$FOR_USER" 2>/dev/null; then
    echo "✓ Installing for $FOR_USER, with lingering on"
  else
    echo "✓ Installing for $FOR_USER"
  fi
  BIN_DIR="${HERMESH_BIN_DIR:-$home/.local/bin}"
  AS="runuser -u $FOR_USER -- env HOME=$home USER=$FOR_USER LOGNAME=$FOR_USER XDG_RUNTIME_DIR=/run/user/$uid"
elif [ -n "$FOR_USER" ]; then
  die "--user is for root; you are installing for yourself already"
fi

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
case "$MODE" in
  cli) BINS="hermesh" ;;
  device) BINS="hermeshd" ;;
  *) BINS="hermeshd hermesh" ;;
esac

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

if [ -n "$AS" ]; then
  $AS mkdir -p "$BIN_DIR"
else
  mkdir -p "$BIN_DIR"
fi
for b in $BINS; do
  asset="$b-$OS-$ARCH"
  echo "› downloading $asset"
  curl -fsSL "$BASE/$asset" -o "$TMP/$asset"
  want="$(grep " $asset\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
  [ -n "$want" ] || die "$asset is not in the signed checksum list"
  [ "$(sha256 "$TMP/$asset")" = "$want" ] || die "$asset checksum mismatch"
  if [ -n "$AS" ]; then
    install -m 0755 -o "$FOR_USER" -g "$(id -gn "$FOR_USER")" "$TMP/$asset" "$BIN_DIR/$b"
  else
    install -m 0755 "$TMP/$asset" "$BIN_DIR/$b"
  fi
done
echo "✓ installed to $BIN_DIR"
if [ -z "$AS" ]; then
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) echo "note: $BIN_DIR is not on your PATH; add it with:"
       echo "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.profile && . ~/.profile" ;;
  esac
fi

if [ "$MODE" = cli ]; then
  echo "Next: hermesh auth login   (the official center; or give a connection address)"
  exit 0
fi

if [ "$MODE" = device ]; then
  # A trusted device joins your Hermesh, or, with no other device yet,
  # sets it up (signing in with a code a browser shows).
  set -- device join
  if [ -z "$ADDRESS" ] && [ -n "$TTY" ]; then
    printf 'Is Hermesh set up on another device already (your iPhone or Mac)? [Y/n] '
    read -r yes <"$TTY" || true
    case "$yes" in n* | N*) set -- device setup ;; esac
  fi
else
  set -- pair
  [ -n "$NAME" ] && set -- "$@" --name "$NAME"
fi
[ -n "$ADDRESS" ] && set -- "$@" "$ADDRESS"
# The terminal goes to hermeshd as its stdin, for what it asks.
if [ -n "$TTY" ]; then
  $AS "$BIN_DIR/hermeshd" "$@" <"$TTY"
else
  $AS "$BIN_DIR/hermeshd" "$@"
fi
# Installed by root: its commands, as the service, are that user's.
if [ -n "$AS" ]; then
  echo
  echo "Hermesh runs as $FOR_USER: run these as $FOR_USER (su - $FOR_USER)"
  if ! su - "$FOR_USER" -c 'command -v hermeshd' >/dev/null 2>&1; then
    echo "  ($FOR_USER's PATH lacks $BIN_DIR: echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> $home/.profile)"
  fi
fi

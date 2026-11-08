#!/bin/sh
# Installs her: downloads a release from GitHub, checks its SHA-256 and puts the program in
# ~/.local/bin. Running it again upgrades. To uninstall, delete that one file; ~/.her holds your
# sessions and settings, and stays until you delete it.
#
#   curl -fsSL https://github.com/hermeshai/her-releases/releases/latest/download/install.sh | sh
#   sh install.sh --version 0.2.0 --dir /usr/local/bin
#
# HER_INSTALL_DIR sets the folder, HER_RELEASES_URL a mirror of the releases. It goes through
# HTTPS_PROXY when that is set.
set -eu

releases="${HER_RELEASES_URL:-https://github.com/hermeshai/her-releases/releases}"
dir="${HER_INSTALL_DIR:-$HOME/.local/bin}"
version=""

while [ $# -gt 0 ]; do
  case "$1" in
    --version) version="${2:?--version needs a version}"; shift 2 ;;
    --version=*) version="${1#--version=}"; shift ;;
    --dir) dir="${2:?--dir needs a folder}"; shift 2 ;;
    --dir=*) dir="${1#--dir=}"; shift ;;
    *) echo "install.sh: unknown option $1" >&2; exit 2 ;;
  esac
done

fail() {
  echo "install.sh: $1" >&2
  exit 1
}

case "$(uname -s)" in
  Darwin)
    # A terminal under Rosetta reports x86_64 on an Apple Silicon Mac.
    if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = "1" ]; then
      target=aarch64-apple-darwin
    else
      target=x86_64-apple-darwin
    fi
    ;;
  Linux)
    case "$(uname -m)" in
      x86_64 | amd64) target=x86_64-unknown-linux-musl ;;
      aarch64 | arm64) target=aarch64-unknown-linux-musl ;;
      *) fail "no builds for $(uname -m)" ;;
    esac
    ;;
  *) fail "no builds for $(uname -s)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  get() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  get() { wget -q "$1" -O "$2"; }
else
  fail "needs curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "needs sha256sum or shasum"
fi

if [ -n "$version" ]; then
  from="$releases/download/v$version"
else
  from="$releases/latest/download"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

get "$from/SHA256SUMS" "$work/SHA256SUMS" || fail "cannot download $from/SHA256SUMS"
# The list of the release's files also says which version it is.
line="$(grep -E " her-[0-9][^ ]*-$target\.tar\.gz\$" "$work/SHA256SUMS" | head -n 1 || true)"
[ -n "$line" ] || fail "this release has no build for $target"
expected="$(echo "$line" | cut -d' ' -f1)"
archive="$(echo "$line" | sed 's/^[^ ]* *//')"

echo "downloading $archive"
get "$from/$archive" "$work/$archive" || fail "cannot download $from/$archive"
found="$(sha256 "$work/$archive")"
[ "$found" = "$expected" ] || fail "the download does not match SHA256SUMS; nothing was installed"

mkdir -p "$work/unpacked"
tar -xzf "$work/$archive" -C "$work/unpacked"
[ -f "$work/unpacked/her" ] || fail "the archive has no her in it"
mkdir -p "$dir"
# The same folder, so that the file appears whole; whatever runs the old one keeps running.
cp "$work/unpacked/her" "$dir/.her.new"
chmod 755 "$dir/.her.new"
mv -f "$dir/.her.new" "$dir/her"

echo "installed $("$dir/her" --version) at $dir/her"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    echo
    echo "$dir is not in your PATH. Add this line to your shell's profile:"
    echo "  export PATH=\"$dir:\$PATH\""
    ;;
esac
echo
echo "Next: run her"

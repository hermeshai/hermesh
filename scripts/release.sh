#!/usr/bin/env bash
# Builds release assets into dist/release: binaries for darwin/linux on
# arm64/amd64, their version, a signed checksum list and install.sh with
# the public key.
#   RELEASE_SIGNING_KEY_FILE  path to the ECDSA P-256 private key (PEM)
#   VERSION                   e.g. v0.1.0
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:?set VERSION}"
OUT=dist/release
rm -rf "$OUT" && mkdir -p "$OUT"
for os in darwin linux; do
  for arch in arm64 amd64; do
    for cmd in hermeshd hermesh hermesh-relay; do
      GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X github.com/hermeshai/hermesh/internal/machine.Version=${VERSION#v}" \
        -o "$OUT/$cmd-$os-$arch" "./cmd/$cmd"
    done
  done
done
# The version, signed with the programs, for installed ones to update (docs/machine-updates.md).
printf '%s\n' "${VERSION#v}" > "$OUT/version"
(cd "$OUT" && (command -v sha256sum >/dev/null && sha256sum hermesh* version || shasum -a 256 hermesh* version) > checksums.txt)
openssl dgst -sha256 -sign "${RELEASE_SIGNING_KEY_FILE:?set RELEASE_SIGNING_KEY_FILE}" -out "$OUT/checksums.txt.sig" "$OUT/checksums.txt"
PUBKEY_FILE="${RELEASE_PUBKEY_FILE:-scripts/release-pubkey.pem}"
openssl dgst -sha256 -verify "$PUBKEY_FILE" -signature "$OUT/checksums.txt.sig" "$OUT/checksums.txt"
python3 - "$OUT/install.sh" "$PUBKEY_FILE" <<'PY'
import sys
src = open("scripts/install.sh").read()
pub = open(sys.argv[2]).read().strip()
open(sys.argv[1], "w").write(src.replace("__HERMESH_RELEASE_PUBKEY__", pub))
PY
chmod +x "$OUT/install.sh"
echo "✓ release assets in $OUT"

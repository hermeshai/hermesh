#!/usr/bin/env bash
# Creates the release signing key pair (ECDSA P-256).
# Store release-signing.key as the GitHub secret RELEASE_SIGNING_KEY and
# commit scripts/release-pubkey.pem. Keep the private key offline otherwise.
set -euo pipefail
cd "$(dirname "$0")"
[ -e release-signing.key ] && { echo "release-signing.key already exists"; exit 1; }
openssl ecparam -name prime256v1 -genkey -noout -out release-signing.key
chmod 600 release-signing.key
openssl ec -in release-signing.key -pubout -out release-pubkey.pem
echo "Wrote scripts/release-pubkey.pem (commit it) and scripts/release-signing.key (do NOT commit)."

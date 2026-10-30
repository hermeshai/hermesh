# Development

```bash
make generate   # Go, Swift and TypeScript code from proto
make lint test  # static checks and unit tests
make test-app   # the desktop app's types and unit tests, and the signer's
make e2e        # full user flows and malicious-relay tests with real binaries and scripted agents
make e2e-real   # the same flows with the real Claude Code and Codex on this machine (uses quota)
HERMESH_DOCKER=1 go test ./e2e -run Linux   # a Linux machine (Docker)
HERMESH_IPHONE_E2E=1 go test ./e2e -run TestIPhone -timeout 60m   # the iPhone app in the Simulator
make app        # build and sign dist/Hermesh.app (see below for a release build)
pnpm --dir apps/desktop dev   # the desktop app against the local hermeshd (HERMESH_SOCKET to choose one)
```

`make e2e` includes the desktop app's tests, which open the app on this Mac;
`go test ./e2e -skip 'TestDesktop$|TestSignerApp$'` leaves them out.

## Releases

A release build of the desktop app needs, besides the Developer ID certificate in the keychain, the signer's Developer ID provisioning profile (App ID `com.hermesh.app.signer`, so the root key stays in the signer's Keychain group) and an App Store Connect API key to notarize:

```bash
HERMESH_SIGNER_PROFILE=~/.config/hermesh/signer.provisionprofile \
HERMESH_NOTARY_KEY_PATH=~/.appstoreconnect/private_keys/AuthKey_<key id>.p8 \
HERMESH_NOTARY_KEY_ID=<key id> HERMESH_NOTARY_ISSUER=<issuer id> \
make app        # HERMESH_ARCH=arm64 for an Apple silicon build only
```

The release workflow reads the same from the secrets `MACOS_SIGNER_PROFILE`, `APPLE_API_KEY_P8`, `APPLE_API_KEY_ID` and `APPLE_API_ISSUER_ID`.

Releasing: before tagging, the version's highlights go in `apps/desktop/src/locales/{en,zh-CN}/releases.json` (rename `unreleased` to the version, and leave an empty `unreleased`); without them the release workflow stops. The release's text starts with which file to install for what (`install` in the same folder's `updates.json`), then the highlights. The iPhone app is not part of these releases: it is built with Xcode ([iphone-app.md](iphone-app.md) §6).

The first time, run `scripts/release-keygen.sh` to generate the release signing key (the private key goes in the GitHub secret `RELEASE_SIGNING_KEY`; commit the public key), then push a `v*` tag. Each release carries `Hermesh-macos-<arch>.dmg` for installation, and `Hermesh-macos-<arch>.zip` with `RELEASES-darwin-<arch>.json` for installed apps to update themselves (`arm64` and `x64`; [docs/desktop-app.md](desktop-app.md) §6.5–6.7). The app and DMG are Developer ID–signed, notarized and stapled. A whole update, with two signed builds made here and served locally, is checked by `HERMESH_UPDATE_E2E=1 go test ./e2e -run TestDesktopUpdate -v -timeout 30m`. The binaries' signed `checksums.txt` also lists `version`, which installed machines read to update themselves ([machine-updates.md](machine-updates.md)); `HERMESH_DOCKER=1 go test ./e2e -run TestLinuxUpdate -v` checks that on a Linux server in Docker.
